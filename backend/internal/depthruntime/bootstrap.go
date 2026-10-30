package depthruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"infinite-canvas/backend/internal/depthruntime/bootstrap_assets"
	"infinite-canvas/backend/internal/desktopnet"
	"infinite-canvas/backend/internal/desktopstorage"
)

const (
	pythonReleaseManifestURL = "https://www.python.org/ftp/python/3.11.9/windows-3.11.9.json"
	pypiJSONBase             = "https://pypi.org/pypi"
	pythonEmbedID            = "pythonembed-3.11-64"
	pythonEmbedVersion       = "3.11.9"
	pipBootstrapVersion      = "25.3"
	bootstrapEstimateMargin  = int64(256 << 20)
	cpuBootstrapDownloadCap  = int64(2 << 30)
	cpuBootstrapSpaceCap     = int64(6 << 30)
	cudaBootstrapDownloadCap = int64(5 << 30)
	cudaBootstrapSpaceCap    = int64(18 << 30)
)

type bootstrapPlan struct {
	manifest Manifest
	runtime  Artifact
	pip      Artifact
	packages []Artifact
}

type pythonReleaseManifest struct {
	Versions []struct {
		ID          string `json:"id"`
		SortVersion string `json:"sort-version"`
		URL         string `json:"url"`
		Hash        struct {
			SHA256 string `json:"sha256"`
		} `json:"hash"`
	} `json:"versions"`
	Files []struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		Hash struct {
			SHA256 string `json:"sha256"`
		} `json:"hash"`
	} `json:"files"`
}

type pypiRelease struct {
	URLs []struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Size     int64  `json:"size"`
		Digests  struct {
			SHA256 string `json:"sha256"`
		} `json:"digests"`
		Packagetype string `json:"packagetype"`
	} `json:"urls"`
}

type bootstrapPackage struct {
	name    string
	version string
}

var depthPackages = []bootstrapPackage{
	{name: "colorama", version: "0.4.6"},
	{name: "filelock", version: "4.0.4"},
	{name: "fsspec", version: "2026.9.0"},
	{name: "Jinja2", version: "3.1.6"},
	{name: "MarkupSafe", version: "3.0.3"},
	{name: "networkx", version: "3.6.1"},
	{name: "sympy", version: "1.14.0"},
	{name: "mpmath", version: "1.3.0"},
	{name: "typing-extensions", version: "4.16.0"},
	{name: "setuptools", version: "84.0.0"},
	{name: "numpy", version: "2.4.6"},
	{name: "opencv-python", version: "5.0.0.93"},
	{name: "pillow", version: "12.3.0"},
	{name: "matplotlib", version: "3.11.2"},
	{name: "contourpy", version: "1.3.3"},
	{name: "cycler", version: "0.12.1"},
	{name: "fonttools", version: "4.66.0"},
	{name: "kiwisolver", version: "1.5.1"},
	{name: "packaging", version: "26.3"},
	{name: "pyparsing", version: "3.3.3"},
	{name: "python-dateutil", version: "2.9.0.post0"},
	{name: "six", version: "1.17.0"},
	{name: "imageio", version: "2.37.4"},
	{name: "easydict", version: "1.13"},
	{name: "einops", version: "0.8.2"},
	{name: "tqdm", version: "4.70.1"},
}

func windowsBootstrap(options EnsureOptions) bool {
	return options.Platform == "windows-amd64" && strings.TrimSpace(options.ManifestURL) == ""
}

func resolveArtifacts(ctx context.Context, options EnsureOptions) (Manifest, Artifact, *bootstrapPlan, error) {
	if windowsBootstrap(options) {
		bootstrap, err := fetchBootstrapPlan(ctx, options.Variant)
		if err != nil {
			return Manifest{}, Artifact{}, nil, err
		}
		return bootstrap.manifest, bootstrap.runtime, bootstrap, nil
	}
	manifest, err := fetchManifestSources(ctx, options)
	if err != nil {
		if ctx.Err() != nil {
			return Manifest{}, Artifact{}, nil, ctx.Err()
		}
		if options.Platform == "windows-amd64" || options.FallbackManifest == nil {
			return Manifest{}, Artifact{}, nil, err
		}
		manifest = *options.FallbackManifest
	}
	if options.Platform == "windows-amd64" && manifest.Version != 2 || options.Platform != "windows-amd64" && manifest.Version != 1 {
		return Manifest{}, Artifact{}, nil, fmt.Errorf("不支持的深度组件清单版本 %d", manifest.Version)
	}
	artifact := manifest.Runtime
	if options.Platform == "windows-amd64" {
		var ok bool
		artifact, ok = manifest.Runtimes[options.Platform+"/"+options.Variant]
		if !ok {
			return Manifest{}, Artifact{}, nil, errors.New("深度组件清单缺少对应的 Windows 运行包")
		}
		if artifact.Files <= 0 || artifact.ExpandedSize <= 0 {
			return Manifest{}, Artifact{}, nil, errors.New("Windows 深度组件清单缺少解压体积或文件数")
		}
	}
	if artifact.Size <= 0 || manifest.Model.Size <= 0 {
		return Manifest{}, Artifact{}, nil, errors.New("深度组件清单缺少下载大小")
	}
	return manifest, artifact, nil, nil
}

func fetchBootstrapPlan(ctx context.Context, variant string) (*bootstrapPlan, error) {
	if variant != "cpu" && variant != "cuda" {
		return nil, fmt.Errorf("不支持的 Windows 深度组件设备: %s", variant)
	}
	var pythonManifest pythonReleaseManifest
	if err := fetchJSON(ctx, pythonReleaseManifestURL, &pythonManifest); err != nil {
		return nil, fmt.Errorf("读取 Python 运行时发布元数据失败: %w", err)
	}
	var pythonURL, pythonSHA string
	for _, release := range pythonManifest.Versions {
		if release.ID == pythonEmbedID && release.SortVersion == pythonEmbedVersion {
			pythonURL, pythonSHA = release.URL, strings.ToLower(release.Hash.SHA256)
			break
		}
	}
	if pythonURL == "" {
		for _, release := range pythonManifest.Files {
			if release.ID == pythonEmbedID {
				pythonURL, pythonSHA = release.URL, strings.ToLower(release.Hash.SHA256)
				break
			}
		}
	}
	if !validHTTPSArtifactURL(pythonURL, "www.python.org") || len(pythonSHA) != 64 {
		return nil, errors.New("Python 发布元数据缺少可信的 Windows embeddable SHA-256")
	}
	pythonSize, err := probeArtifactSize(ctx, pythonURL)
	if err != nil {
		return nil, fmt.Errorf("读取 Python 运行时大小失败: %w", err)
	}
	python := Artifact{URLs: []string{pythonURL}, Size: pythonSize, SHA256: pythonSHA}
	pip, err := pypiWheel(ctx, "pip", pipBootstrapVersion)
	if err != nil {
		return nil, fmt.Errorf("读取 pip 引导包元数据失败: %w", err)
	}
	packages := make([]Artifact, 0, len(depthPackages)+2)
	torch, err := pytorchWheel(ctx, "torch", variant)
	if err != nil {
		return nil, err
	}
	torchvision, err := pytorchWheel(ctx, "torchvision", variant)
	if err != nil {
		return nil, err
	}
	packages = append(packages, torch, torchvision)
	for _, pkg := range depthPackages {
		artifact, err := pypiWheel(ctx, pkg.name, pkg.version)
		if err != nil {
			return nil, fmt.Errorf("读取 %s %s wheel 元数据失败: %w", pkg.name, pkg.version, err)
		}
		packages = append(packages, artifact)
	}
	return &bootstrapPlan{
		manifest: bootstrapManifest(variant),
		runtime:  python, pip: pip, packages: packages,
	}, nil
}

func bootstrapManifest(variant string) Manifest {
	return Manifest{Version: 2, Runtimes: map[string]Artifact{"windows-amd64/" + variant: {}}, Model: Artifact{
		URLs: []string{
			"https://hf-mirror.com/depth-anything/Video-Depth-Anything-Small/resolve/main/video_depth_anything_vits.pth",
			"https://huggingface.co/depth-anything/Video-Depth-Anything-Small/resolve/main/video_depth_anything_vits.pth",
		},
		Size:   modelSize,
		SHA256: modelSHA,
	}}
}

func fetchJSON(ctx context.Context, source string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	response, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("metadata service returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	return decoder.Decode(target)
}

func pypiWheel(ctx context.Context, name, version string) (Artifact, error) {
	var metadata pypiRelease
	endpoint := fmt.Sprintf("%s/%s/%s/json", pypiJSONBase, url.PathEscape(name), url.PathEscape(version))
	if err := fetchJSON(ctx, endpoint, &metadata); err != nil {
		return Artifact{}, err
	}
	var selected *struct {
		Filename string `json:"filename"`
		URL      string `json:"url"`
		Size     int64  `json:"size"`
		Digests  struct {
			SHA256 string `json:"sha256"`
		} `json:"digests"`
		Packagetype string `json:"packagetype"`
	}
	for index := range metadata.URLs {
		file := &metadata.URLs[index]
		if file.Packagetype != "bdist_wheel" || !validWheelURL(file.URL) || len(file.Digests.SHA256) != 64 || file.Size <= 0 {
			continue
		}
		universal := strings.Contains(file.Filename, "py3-none-any") || strings.Contains(file.Filename, "py2.py3-none-any")
		windowsWheel := strings.Contains(file.Filename, "-cp311-cp311-win_amd64") || strings.Contains(file.Filename, "-abi3-win_amd64")
		if !universal && !windowsWheel {
			continue
		}
		if selected == nil || wheelPreference(file.Filename) > wheelPreference(selected.Filename) {
			selected = file
		}
	}
	if selected == nil {
		return Artifact{}, fmt.Errorf("没有兼容 Windows CPython 3.11 的 wheel: %s %s", name, version)
	}
	return Artifact{URLs: []string{selected.URL}, Size: selected.Size, SHA256: strings.ToLower(selected.Digests.SHA256)}, nil
}

func wheelPreference(filename string) int {
	switch {
	case strings.Contains(filename, "cp311-cp311-win_amd64"):
		return 4
	case strings.Contains(filename, "cp311-abi3-win_amd64"):
		return 3
	case strings.Contains(filename, "py3-none-any"):
		return 2
	case strings.Contains(filename, "py2.py3-none-any"):
		return 1
	default:
		return 0
	}
}

func validWheelURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && (parsed.Hostname() == "files.pythonhosted.org" || strings.HasSuffix(parsed.Hostname(), ".pythonhosted.org"))
}

func pytorchWheel(ctx context.Context, name, variant string) (Artifact, error) {
	index := "cpu"
	if variant == "cuda" {
		index = "cu128"
	}
	indexURL := fmt.Sprintf("https://download.pytorch.org/whl/%s/%s/", index, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return Artifact{}, err
	}
	response, err := downloadClient.Do(req)
	if err != nil {
		return Artifact{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Artifact{}, fmt.Errorf("PyTorch %s 索引返回 HTTP %d", name, response.StatusCode)
	}
	root, err := html.Parse(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return Artifact{}, err
	}
	wanted := fmt.Sprintf("%s-2.14.0+%s-cp311-cp311-win_amd64.whl", name, index)
	if name == "torchvision" {
		wanted = fmt.Sprintf("torchvision-0.29.0+%s-cp311-cp311-win_amd64.whl", index)
	}
	var foundURL, foundSHA string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			for _, attribute := range node.Attr {
				if attribute.Key != "href" {
					continue
				}
				parsed, parseErr := url.Parse(attribute.Val)
				if parseErr != nil {
					continue
				}
				candidate := parsed.Path
				if decoded, decodeErr := url.PathUnescape(candidate); decodeErr == nil {
					candidate = decoded
				}
				if filepath.Base(candidate) == wanted {
					foundURL = response.Request.URL.ResolveReference(parsed).String()
					foundSHA = strings.TrimPrefix(parsed.Fragment, "sha256=")
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if !validHTTPSArtifactURL(foundURL, "pytorch.org") || len(foundSHA) != 64 {
		return Artifact{}, fmt.Errorf("PyTorch 索引缺少兼容的 %s wheel 或 SHA-256", wanted)
	}
	size, err := probeArtifactSize(ctx, foundURL)
	if err != nil {
		return Artifact{}, fmt.Errorf("读取 %s wheel 大小失败: %w", name, err)
	}
	return Artifact{URLs: []string{foundURL}, Size: size, SHA256: strings.ToLower(foundSHA)}, nil
}

func validHTTPSArtifactURL(raw, host string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && (parsed.Hostname() == host || strings.HasSuffix(parsed.Hostname(), "."+host))
}

func probeArtifactSize(ctx context.Context, source string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, source, nil)
	if err != nil {
		return 0, err
	}
	response, err := downloadClient.Do(req)
	if err != nil {
		return 0, err
	}
	if response.StatusCode == http.StatusOK {
		if raw := response.Header.Get("Content-Length"); raw != "" {
			response.Body.Close()
			size, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr == nil && size > 0 {
				return size, nil
			}
		}
	}
	response.Body.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Range", "bytes=0-0")
	response, err = downloadClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("artifact size probe returned HTTP %d", response.StatusCode)
	}
	contentRange := response.Header.Get("Content-Range")
	_, totalText, ok := strings.Cut(contentRange, "/")
	if !ok || totalText == "" || totalText == "*" {
		return 0, errors.New("artifact size probe returned no total length")
	}
	size, err := strconv.ParseInt(totalText, 10, 64)
	if err != nil || size <= 0 {
		return 0, errors.New("artifact size probe returned an invalid length")
	}
	return size, nil
}

func bootstrapPlanSizes(plan *bootstrapPlan) (download, expanded int64) {
	if plan == nil {
		return 0, 0
	}
	download = plan.runtime.Size + plan.pip.Size
	for _, artifact := range plan.packages {
		download += artifact.Size
	}
	// PyTorch and binary dependencies unpack larger than their wheel archives;
	// the extra margin covers resolver-selected transitive wheels as an estimate.
	download += bootstrapEstimateMargin
	expanded = download*2 + bootstrapEstimateMargin
	return download, expanded
}

func installWindowsBootstrap(ctx context.Context, options EnsureOptions, stage string, plan *bootstrapPlan) error {
	if plan == nil {
		return errors.New("Windows 深度组件引导信息缺失")
	}
	pythonRoot := filepath.Join(stage, ".python")
	if err := os.MkdirAll(pythonRoot, 0o750); err != nil {
		return err
	}
	pythonArchive := filepath.Join(options.DataDir, "downloads", "depth-runtime-v1-windows-amd64-python.zip")
	if err := Download(ctx, plan.runtime, pythonArchive, componentProgress(options.Progress, "runtime")); err != nil {
		return err
	}
	if err := extractRuntimeArchiveContext(ctx, pythonArchive, pythonRoot, plan.runtime, 12<<30); err != nil {
		return err
	}
	if !pathRegular(filepath.Join(pythonRoot, "python.exe")) {
		return errors.New("Python embeddable 包缺少 python.exe")
	}
	if err := bootstrap_assets.Write(stage); err != nil {
		return fmt.Errorf("安装深度捕捉源快照失败: %w", err)
	}
	pipWheel := filepath.Join(options.DataDir, "downloads", "depth-runtime-v1-pip.whl")
	if err := Download(ctx, plan.pip, pipWheel, componentProgress(options.Progress, "dependencies")); err != nil {
		return err
	}
	sitePackages := filepath.Join(pythonRoot, "Lib", "site-packages")
	if err := os.MkdirAll(sitePackages, 0o750); err != nil {
		return err
	}
	if err := extractRuntimeArchiveContext(ctx, pipWheel, sitePackages, plan.pip, 1<<30); err != nil {
		return fmt.Errorf("解压 pip 引导包失败: %w", err)
	}
	pth := strings.Join([]string{"python311.zip", ".", "../worker", "Lib/site-packages", "import site", ""}, "\r\n")
	if err := os.WriteFile(filepath.Join(pythonRoot, "python311._pth"), []byte(pth), 0o640); err != nil {
		return err
	}
	wheelsDir := filepath.Join(options.DataDir, "downloads", "depth-runtime-v1-wheels")
	if err := os.MkdirAll(wheelsDir, 0o750); err != nil {
		return err
	}
	wheelPaths := make([]string, 0, len(plan.packages))
	for _, artifact := range plan.packages {
		name, err := artifactFilename(artifact)
		if err != nil {
			return err
		}
		path := filepath.Join(wheelsDir, name)
		if err := Download(ctx, artifact, path, componentProgress(options.Progress, "dependencies")); err != nil {
			return err
		}
		wheelPaths = append(wheelPaths, path)
	}
	if err := installDepthPackages(ctx, pythonRoot, stage, options.DataDir, options.Variant, wheelPaths); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, ".bootstrap-ready"), []byte("python=3.11.9\nvda="+bootstrap_assets.VDACommit+"\n"), 0o640)
}

func installDepthPackages(ctx context.Context, pythonRoot, runtimeRoot, dataDir, variant string, wheelPaths []string) error {
	python := filepath.Join(pythonRoot, "python.exe")
	sitePackages := filepath.Join(pythonRoot, "Lib", "site-packages")
	args := []string{"-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--no-warn-script-location", "--no-compile", "--only-binary=:all:", "--no-index", "--find-links", filepath.Join(dataDir, "downloads", "depth-runtime-v1-wheels"), "--target", sitePackages}
	args = append(args, wheelPaths...)
	command := exec.CommandContext(ctx, python, args...)
	command.Dir = filepath.Join(runtimeRoot, "worker")
	command.Env = bootstrapEnvironment(os.Environ(), dataDir)
	if optionsProgress := ctx.Err(); optionsProgress != nil {
		return optionsProgress
	}
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("安裝 Windows 深度捕捉二进制依赖失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func artifactFilename(artifact Artifact) (string, error) {
	if len(artifact.URLs) == 0 {
		return "", errors.New("组件下载地址缺失")
	}
	parsed, err := url.Parse(artifact.URLs[0])
	if err != nil {
		return "", errors.New("组件下载地址无效")
	}
	name, err := url.PathUnescape(filepath.Base(parsed.Path))
	if err != nil || name == "" || name == "." || name == string(filepath.Separator) || strings.ContainsAny(name, "/\\:\x00") {
		return "", errors.New("组件文件名无效")
	}
	return name, nil
}

func estimatedBootstrapPlan(ctx context.Context, options EnsureOptions, paths runtimePaths) (Plan, error) {
	plan := Plan{}
	if options.Variant != "cpu" && options.Variant != "cuda" {
		return Plan{}, fmt.Errorf("不支持的 Windows 深度组件设备: %s", options.Variant)
	}
	runtimeReady := bootstrapRuntimeReady(paths)
	modelReady := verifyFileContext(ctx, filepath.Join(modelRuntimePath(options), "checkpoints", "video_depth_anything_vits.pth"), Artifact{Size: modelSize, SHA256: modelSHA}) == nil
	if runtimeReady {
		if !modelReady {
			plan.DownloadBytes = modelSize
			plan.RequiredBytes = modelSize + bootstrapEstimateMargin
		}
	} else {
		plan.Estimated = true
		if options.Variant == "cpu" {
			plan.DownloadBytes = cpuBootstrapDownloadCap
			plan.RequiredBytes = cpuBootstrapSpaceCap
		} else {
			plan.DownloadBytes = cudaBootstrapDownloadCap
			plan.RequiredBytes = cudaBootstrapSpaceCap
		}
		if modelReady {
			plan.DownloadBytes -= modelSize
			plan.RequiredBytes -= modelSize
		}
	}
	free, err := desktopstorage.FreeBytes(options.DataDir)
	plan.FreeBytes = free
	return plan, err
}

func bootstrapEnvironment(inherited []string, dataDir string) []string {
	blocked := map[string]bool{
		"PYTHONHOME": true, "PYTHONPATH": true, "PYTHONUSERBASE": true, "PYTHONNOUSERSITE": true,
		"VIRTUAL_ENV": true, "PIP_CONFIG_FILE": true, "PIP_INDEX_URL": true, "PIP_EXTRA_INDEX_URL": true,
		"PIP_FIND_LINKS": true, "PIP_NO_INDEX": true, "PIP_CACHE_DIR": true,
	}
	environment := make([]string, 0, len(inherited)+8)
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if blocked[strings.ToUpper(key)] || strings.EqualFold(key, "HTTPS_PROXY") || strings.EqualFold(key, "https_proxy") || strings.EqualFold(key, "NO_PROXY") || strings.EqualFold(key, "no_proxy") || strings.EqualFold(key, "ALL_PROXY") || strings.EqualFold(key, "all_proxy") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment,
		"PYTHONNOUSERSITE=1",
		"PIP_CONFIG_FILE=NUL",
		"PIP_CACHE_DIR="+filepath.Join(dataDir, "downloads", "pip-cache"),
	)
	return append(environment, desktopnet.HTTPSProxyEnvironment()...)
}
