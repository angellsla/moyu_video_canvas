package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/skillhub"
)

func testMarketRelease(t *testing.T, version, body string) (skillhub.Release, []byte) {
	t.Helper()
	a, e := archiveFromMarkdown([]byte("---\nname: market-test\ndescription: Repeatable test\n---\n"+body), "", "")
	if e != nil {
		t.Fatal(e)
	}
	b, e := encodeSkillArchive(a.Files)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(b)
	h := sha256.Sum256(a.Files["SKILL.md"])
	return skillhub.Release{Metadata: skillhub.Metadata{SkillID: "test-market", Name: "Market test", Description: "Repeatable test", License: "CC0-1.0", VersionLabel: version, Requirements: []string{}, Showcase: []skillhub.Showcase{}}, ReleaseID: version, ContentHash: a.ContentHash, ArchiveSHA256: hex.EncodeToString(sum[:]), ArchiveBytes: int64(len(b)), Files: []skillhub.File{{Path: "SKILL.md", SHA256: hex.EncodeToString(h[:]), Size: int64(len(a.Files["SKILL.md"])), MimeType: "text/markdown"}}, DownloadURL: "archive"}, b
}
func TestMarketInstallUpdateRollbackAndHashFailurePreservePins(t *testing.T) {
	s, _ := agentSkillFixture(t)
	r1, b1 := testMarketRelease(t, "v1", "RULE_ONE")
	r2, b2 := testMarketRelease(t, "v2", "RULE_TWO")
	current, data := r1, b1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/archive" {
			w.Write(data)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": current})
	}))
	defer server.Close()
	t.Setenv("CANVAS_SKILL_MARKET_URL", server.URL)
	first, e := s.InstallMarket(context.Background(), "owner", r1.SkillID, r1.ReleaseID)
	if e != nil {
		t.Fatal(e)
	}
	pin := Pin{SkillID: first.SkillID, VersionID: first.VersionID, ContentHash: first.ContentHash}
	if first.SourceCommit != "v1" {
		t.Fatal("market release mapping missing", first.SourceCommit)
	}
	current, data = r2, b2
	second, e := s.InstallMarket(context.Background(), "owner", r2.SkillID, r2.ReleaseID)
	if e != nil {
		t.Fatal(e)
	}
	if second.SkillID != first.SkillID || second.VersionID == first.VersionID {
		t.Fatal("update replaced identity")
	}
	old, e := s.AgentVersion("owner", pin)
	if e != nil || !strings.Contains(old.Instruction, "RULE_ONE") {
		t.Fatal("old task pin changed", e)
	}
	rolled, e := s.RollbackMarket("owner", first.SkillID, first.VersionID)
	if e != nil || rolled.VersionID != first.VersionID || !strings.Contains(rolled.Instruction, "RULE_ONE") {
		t.Fatal("rollback did not restore exact content", e)
	}
	current.ArchiveSHA256 = strings.Repeat("0", 64)
	if _, e = s.InstallMarket(context.Background(), "owner", r2.SkillID, r2.ReleaseID); e == nil {
		t.Fatal("accepted corrupt archive")
	}
	after, _ := s.SkillDetail("owner", first.SkillID)
	if after.VersionID != first.VersionID {
		t.Fatal("bad update displaced old version")
	}
	// A metadata-only release still has a distinct distribution identity.
	current, data = r1, b1
	current.ReleaseID = "v1.0.1"
	current.VersionLabel = "1.0.1"
	current.Name = "Revised public title"
	metadataOnly, e := s.InstallMarket(context.Background(), "owner", current.SkillID, current.ReleaseID)
	if e != nil || metadataOnly.VersionID == first.VersionID || metadataOnly.SourceCommit != current.ReleaseID || metadataOnly.SkillName != current.Name {
		t.Fatalf("metadata release was skipped: %+v %v", metadataOnly, e)
	}
}
func TestMarketMinimumClientVersion(t *testing.T) {
	for _, c := range []struct {
		current, minimum string
		want             bool
	}{{"dev", "", true}, {"dev", "1.0.0", false}, {"1.7.22", "1.7.21", true}, {"1.7.22", "1.8.0", false}, {"1.7.22", "garbage", false}, {"1.7.22", "1.7.22", true}, {"v2.0.1-skill.1", "2.0.0", true}, {"v2.0.1-skill.1", "2.0.1", false}} {
		if got := marketClientCompatible(c.current, c.minimum); got != c.want {
			t.Fatalf("%+v got %v", c, got)
		}
	}
}
func TestPrivateForkKeepsReferencesWhenEdited(t *testing.T) {
	s, _ := agentSkillFixture(t)
	a, e := finalizeSkillArchive(map[string][]byte{"SKILL.md": []byte("---\nname: Original\ndescription: Method\n---\nSee references/rules.md"), "LICENSE": []byte("CC0"), "references/rules.md": []byte("KEEP_REFERENCE")}, skillPackageMetadata{Name: "Original", Description: "Method"})
	if e != nil {
		t.Fatal(e)
	}
	original, e := s.createSkillFromArchive("owner", a, SkillInstallRequest{IsPrivate: true}, "market", "https://beeftv.app/skills/original", "original", "", "release", false)
	if e != nil {
		t.Fatal(e)
	}
	fork, e := s.ForkMarket("owner", original.SkillID)
	if e != nil {
		t.Fatal(e)
	}
	if !fork.IsPrivate || fork.SkillID == original.SkillID || fork.SourceType != "markdown" {
		t.Fatalf("invalid fork %+v", fork)
	}
	fork, e = s.UpdateSkill("owner", fork.SkillID, SkillMutationRequest{SkillName: fork.SkillName, Description: fork.Description, Instruction: "Updated method referencing references/rules.md", IsPrivate: true, Tag: "others"})
	if e != nil {
		t.Fatal(e)
	}
	f, e := s.SkillPackageFile("owner", fork.SkillID, "references/rules.md")
	if e != nil || !strings.Contains(f.Content, "KEEP_REFERENCE") {
		t.Fatalf("reference lost %+v %v", f, e)
	}
	if _, e = s.SkillPackageFile("owner", fork.SkillID, "LICENSE"); e != nil {
		t.Fatal("license lost", e)
	}
	unchanged, e := s.SkillDetail("owner", original.SkillID)
	if e != nil || unchanged.VersionID != original.VersionID {
		t.Fatal("fork changed upstream", e)
	}
}
