// Package skillhubcli implements remote market commands without a desktop runtime.
package skillhubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"infinite-canvas/backend/internal/skillhub"
)

type Error struct {
	Code            int
	Reason, Message string
	RetryAfter      string
}

func (e *Error) Error() string          { return e.Message }
func fail(reason, message string) error { return &Error{Code: 2, Reason: reason, Message: message} }

type arguments struct {
	command    string
	positional []string
	options    map[string]string
}

func parse(args []string) (arguments, error) {
	a := arguments{command: "help", options: map[string]string{}}
	if len(args) == 0 {
		return a, nil
	}
	a.command = args[0]
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			a.positional = append(a.positional, arg)
			continue
		}
		key := strings.TrimPrefix(arg, "--")
		switch key {
		case "json", "help":
			a.options[key] = "true"
		case "market", "release", "target", "capabilities", "page", "page-size", "output", "agent", "scope", "project":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return a, fail("invalid_arguments", "缺少 --"+key+" 的值")
			}
			if _, exists := a.options[key]; exists {
				return a, fail("invalid_arguments", "参数重复：--"+key)
			}
			i++
			a.options[key] = args[i]
		default:
			return a, fail("invalid_arguments", "未知参数："+arg)
		}
	}
	return a, nil
}

func emit(out io.Writer, value any) error { return json.NewEncoder(out).Encode(value) }

func Run(ctx context.Context, args []string, out io.Writer) error {
	err := run(ctx, args, out)
	var remote *skillhub.Error
	if errors.As(err, &remote) {
		code := 9
		switch remote.Status {
		case 400, 422:
			code = 8
		case 401, 403:
			code = 6
		case 404:
			code = 3
		case 409:
			code = 4
		}
		message := remote.Message
		if message == "" {
			message = "技能市场请求失败"
		}
		return &Error{Code: code, Reason: remote.Reason, Message: message, RetryAfter: remote.RetryAfter}
	}
	return err
}

func run(ctx context.Context, args []string, out io.Writer) error {
	a, err := parse(args)
	if err != nil {
		return err
	}
	if a.command == "help" || a.command == "--help" || a.options["help"] == "true" {
		_, err = io.WriteString(out, "beeftv skills — 无需运行墨羽Canvas的公开技能客户端\n\nsearch QUERY [--target moyu-canvas|codex|claude-code] [--json]\nshow SKILL_ID --release RELEASE_ID\nfetch SKILL_ID --release RELEASE_ID --output DIRECTORY\ninstall SKILL_ID --release RELEASE_ID --agent codex|claude-code --scope project|user\n\n公开查询不登录。所有网络命令可使用 --market 指定可信 HTTPS 市场或本机测试服务；默认也可用 CANVAS_SKILL_MARKET_URL 覆盖。\n")
		return err
	}
	base := a.options["market"]
	if base == "" {
		base = os.Getenv("CANVAS_SKILL_MARKET_URL")
	}
	if base == "" {
		base = skillhub.DefaultURL
	}
	c, err := skillhub.New(base)
	if err != nil {
		return err
	}
	capabilities := []string{}
	if value := a.options["capabilities"]; value != "" {
		for _, v := range strings.Split(value, ",") {
			capability := strings.TrimSpace(v)
			if capability == "moyu.canvas" {
				capability = "beeftv.canvas"
			}
			capabilities = append(capabilities, capability)
		}
	}
	if a.options["target"] == "" && a.command == "search" {
		a.options["target"] = "moyu-canvas"
	}
	if len(capabilities) == 0 && a.options["target"] == "moyu-canvas" {
		capabilities = []string{"beeftv.canvas"}
	}
	// Preserve the public market's published protocol IDs only at the network
	// boundary; user-facing CLI naming remains 墨羽Canvas.
	marketTarget := a.options["target"]
	if marketTarget == "moyu-canvas" {
		marketTarget = "beeftv"
	}
	switch a.command {
	case "search":
		page, size, err := pageOptions(a)
		if err != nil {
			return err
		}
		result, err := c.Search(ctx, strings.Join(a.positional, " "), marketTarget, capabilities, page, size)
		if err != nil {
			return err
		}
		return emit(out, result)
	case "show", "fetch", "install":
		if len(a.positional) != 1 || a.options["release"] == "" {
			return fail("release_required", "请指定技能 ID 和 --release 发布版本 ID")
		}
		target := marketTarget
		if a.command == "install" {
			target = a.options["agent"]
		}
		discovery, err := c.Discover(ctx, a.positional[0], a.options["release"], target, capabilities)
		if err != nil {
			return err
		}
		r := &discovery.Release
		if a.command == "show" {
			return emit(out, discovery)
		}
		if a.command == "install" {
			if discovery.Compatibility.Status != "supported" {
				return fail("capability_check_required", "目标助手尚不满足这个版本声明的使用要求，请先检查详情")
			}
			available := map[string]bool{}
			for _, v := range capabilities {
				available[v] = true
			}
			for _, required := range r.Requirements {
				if !available[required] {
					return fail("capability_check_required", "目标助手缺少声明的能力："+required)
				}
			}
			if r.MinClientVersion != "" {
				return fail("client_version_unverified", "该技能要求特定客户端版本，请先在目标应用确认兼容性")
			}
		}
		data, err := c.Archive(ctx, r)
		if err != nil {
			return err
		}
		files, err := skillhub.VerifyArchive(data, r)
		if err != nil {
			return &Error{Code: 4, Reason: "archive_integrity", Message: err.Error()}
		}
		destination := a.options["output"]
		if a.command == "install" {
			destination, err = installDestination(a, r, files)
			if err != nil {
				return err
			}
		}
		if destination == "" {
			return fail("output_required", "fetch 需要 --output 目标目录")
		}
		installed, err := writeRelease(destination, files, r, c.BaseURL())
		if err != nil {
			return err
		}
		return emit(out, map[string]any{"skillId": r.SkillID, "releaseId": r.ReleaseID, "contentHash": r.ContentHash, "path": installed, "installed": a.command == "install", "activated": false, "integrity": "archive-content-and-file-sha256"})
	default:
		return fail("unknown_command", fmt.Sprintf("未知技能命令：%s", a.command))
	}
}
