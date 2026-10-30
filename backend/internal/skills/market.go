package skills

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"infinite-canvas/backend/internal/buildinfo"
	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/skillhub"
)

var marketLocks sync.Map
var marketResourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (s *Service) lockMarket(user string) func() {
	v, _ := marketLocks.LoadOrStore(s.dataDir+"/"+user, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
func marketClient() (*skillhub.Client, error) {
	base := strings.TrimSpace(os.Getenv("CANVAS_SKILL_MARKET_URL"))
	if base == "" {
		base = skillhub.DefaultURL
	}
	return skillhub.New(base)
}
func (s *Service) MarketCatalog(ctx context.Context, q string, page, size int) (*skillhub.Catalog, error) {
	c, e := marketClient()
	if e != nil {
		return nil, e
	}
	var result skillhub.Catalog
	e = c.GetJSON(ctx, fmt.Sprintf("catalog?q=%s&page=%d&pageSize=%d", url.QueryEscape(q), page, size), &result)
	return &result, e
}
func (s *Service) MarketRelease(ctx context.Context, id, version string) (*skillhub.Release, error) {
	if !marketResourceID.MatchString(id) || !marketResourceID.MatchString(version) {
		return nil, kernel.BadAuthRequest("技能版本地址无效")
	}
	c, e := marketClient()
	if e != nil {
		return nil, e
	}
	var result skillhub.Release
	e = c.GetJSON(ctx, "skills/"+id+"/releases/"+version, &result)
	if e == nil && (result.SkillID != id || result.ReleaseID != version) {
		return nil, kernel.BadAuthRequest("技能市场返回了其他版本")
	}
	return &result, e
}
func (s *Service) InstallMarket(ctx context.Context, user, id, version string) (*SkillItem, error) {
	unlock := s.lockMarket(user)
	defer unlock()
	r, e := s.MarketRelease(ctx, id, version)
	if e != nil {
		return nil, e
	}
	if e = checkMarketRequirements(r.Requirements); e != nil {
		return nil, e
	}
	if !marketClientCompatible(buildinfo.Current().Version, r.MinClientVersion) {
		return nil, kernel.BadAuthRequest("请先更新墨羽Canvas，再安装这个技能")
	}
	c, e := marketClient()
	if e != nil {
		return nil, e
	}
	data, e := c.Archive(ctx, r)
	if e != nil {
		return nil, e
	}
	files, e := skillhub.VerifyArchive(data, r)
	if e != nil {
		return nil, kernel.WrapAppError(409, "下载包校验失败，原有技能未改变", e)
	}
	a, e := finalizeSkillArchive(files, parseSkillPackageMetadata(files["SKILL.md"]))
	if e != nil {
		return nil, e
	}
	old, e := s.repo.MarketSkill(user, id)
	if e != nil {
		return nil, e
	}
	a.Metadata.Version = r.VersionLabel
	a.Metadata.MarketRelease = r
	if old == nil {
		sourceURL := r.SourceURL
		if sourceURL == "" {
			sourceURL = c.BaseURL() + "/skills/" + id
		}
		return s.createSkillFromArchive(user, a, SkillInstallRequest{Name: r.Name, Description: r.Description, IsPrivate: true}, "market", sourceURL, id, "", version, false)
	}
	if old.ContentHash == r.ContentHash && old.SourceCommit == r.ReleaseID {
		return s.SkillDetail(user, old.ID)
	}
	// A locally edited copy is a user's draft, never an update target.
	if old.SourceType != "market" {
		return nil, kernel.NewAppError(409, "本地修改已保留为草稿，请安装为新技能")
	}
	old.Instruction = string(a.Files["SKILL.md"])
	old.Name = r.Name
	old.Description = r.Description
	if e = s.addSkillArchiveVersion(old, a, "market", r.SourceURL, id, "", version, false); e != nil {
		return nil, e
	}
	return s.SkillDetail(user, old.ID)
}

func (s *Service) SkillVersions(user, id string) (any, error) {
	sk, e := s.visibleSkill(user, id)
	if e != nil {
		return nil, e
	}
	if sk.OwnerID != user {
		return nil, kernel.Forbidden("只能管理自己的技能版本")
	}
	return s.repo.SkillVersions(id)
}

func (s *Service) MarketVersions(user, id string) (any, error) {
	return s.SkillVersions(user, id)
}

func (s *Service) RollbackSkillVersion(user, id, version string) (*SkillItem, error) {
	unlock := s.lockMarket(user)
	defer unlock()
	sk, e := s.visibleSkill(user, id)
	if e != nil {
		return nil, e
	}
	if sk.OwnerID != user {
		return nil, kernel.Forbidden("只能管理自己的技能版本")
	}
	v, e := s.repo.SkillVersion(version)
	if e != nil || v.SkillID != id {
		return nil, kernel.BadAuthRequest("技能版本不存在")
	}
	pin := Pin{SkillID: id, VersionID: version, ContentHash: v.ContentHash}
	resource, e := s.AgentVersion(user, pin)
	if e != nil {
		return nil, e
	}
	name, description, sourceURL := sk.Name, sk.Description, sk.SourceURL
	if sk.SourceType == "market" {
		release, err := marketReleaseMetadata(v.MarketReleaseJSON)
		if err != nil {
			return nil, err
		}
		if release != nil {
			name, description, sourceURL = release.Name, release.Description, release.SourceURL
		}
	}
	if e = s.repo.SwitchSkillVersion(user, id, version, resource.Instruction, name, description, sourceURL); e != nil {
		return nil, e
	}
	return s.SkillDetail(user, id)
}

func (s *Service) RollbackMarket(user, id, version string) (*SkillItem, error) {
	return s.RollbackSkillVersion(user, id, version)
}

// ForkMarket preserves the installed release and its resources as a private editable copy.
func (s *Service) ForkMarket(user, id string) (*SkillItem, error) {
	unlock := s.lockMarket(user)
	defer unlock()
	source, e := s.visibleSkill(user, id)
	if e != nil {
		return nil, e
	}
	bundle, e := s.SkillPackageBundle(user, id)
	if e != nil {
		return nil, e
	}
	files := map[string][]byte{}
	for _, f := range bundle.Files {
		b, e := base64.StdEncoding.DecodeString(f.ContentBase64)
		if e != nil {
			return nil, e
		}
		files[f.Path] = b
	}
	a, e := finalizeSkillArchive(files, parseSkillPackageMetadata(files["SKILL.md"]))
	if e != nil {
		return nil, e
	}
	a.Metadata.MarketRelease = bundle.MarketRelease
	name := source.Name
	if len([]rune(name)) > 72 {
		name = string([]rune(name)[:72])
	}
	return s.createSkillFromArchive(user, a, SkillInstallRequest{Name: name + " · 我的改写", Description: source.Description, Tag: source.Tag, IsPrivate: true}, "markdown", source.SourceURL, "", "", "", false)
}

func marketClientCompatible(current, minimum string) bool {
	if strings.TrimSpace(minimum) == "" {
		return true
	}
	parse := func(s string) ([3]int, bool) {
		var v [3]int
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
		if len(parts) != 3 {
			return v, false
		}
		for i, p := range parts {
			n, e := strconv.Atoi(p)
			if e != nil || n < 0 {
				return v, false
			}
			v[i] = n
		}
		return v, true
	}
	currentParts := strings.SplitN(current, "-", 2)
	a, ok := parse(currentParts[0])
	if !ok {
		return false
	}
	b, ok := parse(minimum)
	if !ok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return len(currentParts) == 1
}
