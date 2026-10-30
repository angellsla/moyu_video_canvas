package depthcapture

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"infinite-canvas/backend/internal/depthruntime"
	"infinite-canvas/backend/internal/model"
)

const (
	depthRuntimeConfigName    = "depth-runtime.json"
	depthRuntimeConfigVersion = 1
	depthRuntimeConfigMaxSize = 64 << 10
)

type depthRuntimeConfig struct {
	Version      int    `json:"version"`
	Python       string `json:"python"`
	ToolDir      string `json:"toolDir"`
	ModelRuntime string `json:"modelRuntime"`
}

func (s *Service) resolveRuntime(ctx context.Context, task *model.Task, platform, variant string) (python string, toolDir string, modelRuntime string, err error) {
	if configured, present, configuredErr := s.configuredRuntime(ctx); present {
		if configuredErr != nil {
			return "", "", "", configuredErr
		}
		return configured.Python, configured.ToolDir, configured.ModelRuntime, nil
	}
	options, optionsErr := s.componentOptions(platform, variant)
	if optionsErr != nil {
		return "", "", "", optionsErr
	}
	options.Offline = true
	installation, ensureErr := s.ensure(ctx, options)
	if ensureErr != nil {
		if variant == "cpu" && ensureErr == depthruntime.ErrNotInstalled {
			return "", "", "", errors.New("当前需要 CPU 深度捕捉组件，请在存储设置中确认下载后重试")
		}
		return "", "", "", ensureErr
	}
	return installation.Python, installation.ToolDir, installation.ModelRuntime, nil
}

// configuredRuntime keeps status checks and task execution on the same explicit
// local component tuple. A partial tuple is an error instead of silently falling
// back to the managed runtime and showing a misleading status.
func (s *Service) configuredRuntime(ctx context.Context) (depthruntime.Installation, bool, error) {
	installation := depthruntime.Installation{
		Python:       strings.TrimSpace(s.getenv(pythonEnv)),
		ToolDir:      strings.TrimSpace(s.getenv(toolDirEnv)),
		ModelRuntime: strings.TrimSpace(s.getenv(runtimeEnv)),
	}
	configured := installation.Python != "" || installation.ToolDir != "" || installation.ModelRuntime != ""
	if configured {
		if installation.Python == "" || installation.ToolDir == "" || installation.ModelRuntime == "" {
			return installation, true, errors.New("本地深度组件配置不完整，请同时设置 BEEFTV_DEPTH_PYTHON、BEEFTV_DEPTH_TOOL_DIR 和 BEEFTV_DEPTH_RUNTIME")
		}
		return installation, true, s.validateConfiguredRuntime(ctx, installation, s.strictRuntimePaths)
	}

	installation, present, err := s.readDepthRuntimeConfig()
	if !present || err != nil {
		return installation, present, err
	}
	return installation, true, s.validateConfiguredRuntime(ctx, installation, true)
}

func (s *Service) readDepthRuntimeConfig() (depthruntime.Installation, bool, error) {
	if strings.TrimSpace(s.dataDir) == "" {
		return depthruntime.Installation{}, false, nil
	}
	configPath := filepath.Join(s.dataDir, depthRuntimeConfigName)
	file, err := os.Open(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return depthruntime.Installation{}, false, nil
	}
	if err != nil {
		return depthruntime.Installation{}, true, fmt.Errorf("读取本地深度组件配置失败: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return depthruntime.Installation{}, true, fmt.Errorf("读取本地深度组件配置失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return depthruntime.Installation{}, true, errors.New("本地深度组件配置必须是普通文件")
	}
	if info.Size() > depthRuntimeConfigMaxSize {
		return depthruntime.Installation{}, true, errors.New("本地深度组件配置超过大小限制")
	}

	var config depthRuntimeConfig
	decoder := json.NewDecoder(io.LimitReader(file, depthRuntimeConfigMaxSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return depthruntime.Installation{}, true, fmt.Errorf("解析本地深度组件配置失败: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return depthruntime.Installation{}, true, errors.New("本地深度组件配置只能包含一个 JSON 对象")
		}
		return depthruntime.Installation{}, true, fmt.Errorf("本地深度组件配置末尾内容无效: %w", err)
	}
	if config.Version != depthRuntimeConfigVersion {
		return depthruntime.Installation{}, true, fmt.Errorf("不支持的本地深度组件配置版本 %d", config.Version)
	}

	installation := depthruntime.Installation{
		Python:       strings.TrimSpace(config.Python),
		ToolDir:      strings.TrimSpace(config.ToolDir),
		ModelRuntime: strings.TrimSpace(config.ModelRuntime),
	}
	for label, path := range map[string]string{
		"python": installation.Python, "toolDir": installation.ToolDir, "modelRuntime": installation.ModelRuntime,
	} {
		if path == "" {
			return installation, true, fmt.Errorf("本地深度组件配置缺少 %s", label)
		}
		if !filepath.IsAbs(path) {
			return installation, true, fmt.Errorf("本地深度组件配置 %s 必须使用绝对路径", label)
		}
	}
	installation.Python = filepath.Clean(installation.Python)
	installation.ToolDir = filepath.Clean(installation.ToolDir)
	installation.ModelRuntime = filepath.Clean(installation.ModelRuntime)
	return installation, true, nil
}

func (s *Service) validateConfiguredRuntime(ctx context.Context, installation depthruntime.Installation, strict bool) error {
	checks := []struct {
		path  string
		label string
		kind  string
	}{
		{installation.Python, "Python", "file"},
		{filepath.Join(installation.ToolDir, "depth_capture"), "深度处理模块", "dir"},
		{filepath.Join(installation.ModelRuntime, "checkpoints", "video_depth_anything_vits.pth"), "深度模型", "file"},
		{filepath.Join(filepath.Dir(installation.ToolDir), "vda", "video_depth_anything", "video_depth.py"), "固定版本 Video Depth Anything 源码", "file"},
	}
	for _, check := range checks {
		if err := s.stat(check.path); err != nil {
			return fmt.Errorf("本地%s不存在或不可访问: %s", check.label, check.path)
		}
		if !strict {
			continue
		}
		info, err := os.Stat(check.path)
		if err != nil {
			return fmt.Errorf("本地%s不存在或不可访问: %s", check.label, check.path)
		}
		if check.kind == "file" && !info.Mode().IsRegular() {
			return fmt.Errorf("本地%s不是普通文件: %s", check.label, check.path)
		}
		if check.kind == "dir" && !info.IsDir() {
			return fmt.Errorf("本地%s不是目录: %s", check.label, check.path)
		}
	}
	if strict {
		if err := depthruntime.VerifyModel(ctx, installation.ModelRuntime); err != nil {
			return fmt.Errorf("本地深度模型大小或 SHA256 校验失败: %w", err)
		}
	}
	return nil
}

func (s *Service) componentOptions(platform, variant string) (depthruntime.EnsureOptions, error) {
	manifestURL := strings.TrimSpace(s.getenv(manifestEnv))
	modelRuntime := strings.TrimSpace(s.getenv(modelRuntimeEnv))
	if modelRuntime != "" && !filepath.IsAbs(modelRuntime) {
		return depthruntime.EnsureOptions{}, errors.New("CANVAS_DEPTH_MODEL_RUNTIME 必须使用绝对路径")
	}
	var publicKey ed25519.PublicKey
	if platform == "windows-amd64" && manifestURL != "" {
		decoded, err := base64.StdEncoding.DecodeString(s.windowsKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return depthruntime.EnsureOptions{}, ErrWindowsKey
		}
		publicKey = decoded
	}
	return depthruntime.EnsureOptions{
		ManifestURL:      manifestURL,
		DataDir:          s.dataDir,
		ModelRuntime:     modelRuntime,
		Platform:         platform,
		Variant:          variant,
		TrustedPublicKey: publicKey,
	}, nil
}
