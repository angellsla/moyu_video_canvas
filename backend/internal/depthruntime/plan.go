package depthruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"infinite-canvas/backend/internal/desktopstorage"
)

var ErrNotInstalled = errors.New("深度捕捉组件尚未安装，请在设置的存储页面下载后再使用")

const modelSize = int64(116440756)
const modelSHA = "13379300b739e659f076a59d52e9801bd8d38c541a7e71f73bbca4dcfb013609"

type Plan struct {
	DownloadBytes int64  `json:"downloadBytes"`
	RequiredBytes int64  `json:"requiredBytes"`
	FreeBytes     uint64 `json:"freeBytes"`
	Estimated     bool   `json:"estimated"`
}

func modelRuntimePath(options EnsureOptions) string {
	if configured := filepath.Clean(options.ModelRuntime); options.ModelRuntime != "" && configured != "." {
		return configured
	}
	return filepath.Join(options.DataDir, "models", "video-depth-anything-small", "v1")
}

// ModelRuntimePath reports the same model root used by status, planning and installation.
func ModelRuntimePath(options EnsureOptions) string {
	return modelRuntimePath(options)
}

// VerifyModel validates the fixed Small checkpoint used by every depth worker.
// External runtime configuration uses this instead of trusting file presence.
func VerifyModel(ctx context.Context, modelRuntime string) error {
	model := filepath.Join(modelRuntime, "checkpoints", "video_depth_anything_vits.pth")
	return verifyFileContext(ctx, model, Artifact{Size: modelSize, SHA256: modelSHA})
}

// Inspect uses installed files only. It never fetches a manifest or runs Python.
func Inspect(ctx context.Context, options EnsureOptions) (Installation, error) {
	paths, err := runtimeLayout(options.DataDir, options.Platform, options.Variant)
	if err != nil {
		return Installation{}, err
	}
	modelRuntime := modelRuntimePath(options)
	model := filepath.Join(modelRuntime, "checkpoints", "video_depth_anything_vits.pth")
	expected := Artifact{Size: modelSize, SHA256: modelSHA}
	if raw, err := os.ReadFile(filepath.Join(paths.Root, ".installation.json")); err == nil {
		var installed Artifact
		if json.Unmarshal(raw, &installed) == nil && installed.Size > 0 && len(installed.SHA256) == 64 {
			expected = installed
		}
	}
	runtimeReady := installedRuntime(paths, options.Platform)
	if windowsBootstrap(options) {
		runtimeReady = bootstrapRuntimeReady(paths)
	}
	if !runtimeReady {
		return Installation{}, ErrNotInstalled
	}
	if err := verifyFileContext(ctx, model, expected); err != nil {
		return Installation{}, ErrNotInstalled
	}
	return Installation{Python: paths.Python, ToolDir: paths.ToolDir, ModelRuntime: modelRuntime}, nil
}

// DownloadPlan fetches metadata only, using the same signature checks as Ensure.
func DownloadPlan(ctx context.Context, options EnsureOptions) (Plan, error) {
	paths, err := runtimeLayout(options.DataDir, options.Platform, options.Variant)
	if err != nil {
		return Plan{}, err
	}
	if windowsBootstrap(options) {
		return estimatedBootstrapPlan(ctx, options, paths)
	}
	manifest, artifact, err := loadArtifacts(ctx, options)
	if err != nil {
		return Plan{}, err
	}
	runtimeReady := installedRuntime(paths, options.Platform)
	archiveReady := runtimeReady || verifyFileContext(ctx, paths.Archive, artifact) == nil
	modelPath := filepath.Join(modelRuntimePath(options), "checkpoints", "video_depth_anything_vits.pth")
	return installationPlan(options.DataDir, artifact, manifest.Model, archiveReady, verifyFileContext(ctx, modelPath, manifest.Model) == nil, runtimeReady)
}

func installedRuntime(paths runtimePaths, platform string) bool {
	pythonReady := pathExecutable(paths.Python) && pathExecutable(paths.BundledPython)
	if platform == "windows-amd64" {
		pythonReady = pathRegular(paths.Python) && pathRegular(paths.BundledPython)
	}
	info, err := os.Stat(filepath.Join(paths.ToolDir, "depth_capture"))
	return pythonReady && err == nil && info.IsDir()
}

func bootstrapRuntimeReady(paths runtimePaths) bool {
	pythonRoot := filepath.Dir(paths.Python)
	sitePackages := filepath.Join(pythonRoot, "Lib", "site-packages")
	return pathRegular(paths.Python) &&
		pathRegular(filepath.Join(pythonRoot, "python311.dll")) &&
		pathRegular(filepath.Join(pythonRoot, "python311.zip")) &&
		pathRegular(filepath.Join(pythonRoot, "python311._pth")) &&
		pathRegular(filepath.Join(paths.Root, ".bootstrap-ready")) &&
		pathRegular(filepath.Join(paths.ToolDir, "depth_capture", "cli.py")) &&
		pathRegular(filepath.Join(paths.Root, "vda", "video_depth_anything", "video_depth.py")) &&
		pathRegular(filepath.Join(sitePackages, "torch", "__init__.py")) &&
		pathRegular(filepath.Join(sitePackages, "torchvision", "__init__.py")) &&
		pathRegular(filepath.Join(sitePackages, "numpy", "__init__.py")) &&
		pathRegular(filepath.Join(sitePackages, "cv2", "__init__.py")) &&
		pathRegular(filepath.Join(sitePackages, "PIL", "__init__.py"))
}

func bootstrapDownloadPlan(ctx context.Context, dataDir, modelRuntime string, paths runtimePaths, bootstrap *bootstrapPlan) (Plan, error) {
	var plan Plan
	if bootstrap == nil {
		return plan, errors.New("Windows 深度组件引导信息缺失")
	}
	runtimeReady := bootstrapRuntimeReady(paths)
	if !runtimeReady {
		pythonArchive := filepath.Join(dataDir, "downloads", "depth-runtime-v1-windows-amd64-python.zip")
		if verifyFileContext(ctx, pythonArchive, bootstrap.runtime) != nil {
			plan.DownloadBytes += bootstrap.runtime.Size
			plan.RequiredBytes += bootstrap.runtime.Size
		}
		pipWheel := filepath.Join(dataDir, "downloads", "depth-runtime-v1-pip.whl")
		if verifyFileContext(ctx, pipWheel, bootstrap.pip) != nil {
			plan.DownloadBytes += bootstrap.pip.Size
			plan.RequiredBytes += bootstrap.pip.Size
		}
		wheelsDir := filepath.Join(dataDir, "downloads", "depth-runtime-v1-wheels")
		var extracted int64 = bootstrap.runtime.Size
		for _, artifact := range bootstrap.packages {
			filename, err := artifactFilename(artifact)
			if err != nil {
				return Plan{}, err
			}
			wheel := filepath.Join(wheelsDir, filename)
			if verifyFileContext(ctx, wheel, artifact) != nil {
				plan.DownloadBytes += artifact.Size
				plan.RequiredBytes += artifact.Size
			}
			extracted += artifact.Size * 3
		}
		// Package extraction is larger than the downloaded wheel set, especially
		// for the CPU/CUDA torch distributions. This remains a labeled estimate.
		plan.RequiredBytes += extracted + bootstrapEstimateMargin
	}
	model := filepath.Join(modelRuntimePath(EnsureOptions{DataDir: dataDir, ModelRuntime: modelRuntime}), "checkpoints", "video_depth_anything_vits.pth")
	modelReady := verifyFileContext(ctx, model, bootstrap.manifest.Model) == nil
	if !modelReady {
		plan.DownloadBytes += bootstrap.manifest.Model.Size
		plan.RequiredBytes += bootstrap.manifest.Model.Size
	}
	plan.RequiredBytes += 256 << 20
	plan.Estimated = !runtimeReady
	var err error
	plan.FreeBytes, err = desktopstorage.FreeBytes(dataDir)
	return plan, err
}

// Metadata preview and installation must use the same version and trust rules.
func loadArtifacts(ctx context.Context, options EnsureOptions) (Manifest, Artifact, error) {
	manifest, artifact, _, err := resolveArtifacts(ctx, options)
	return manifest, artifact, err
}

func installationPlan(dataDir string, artifact, model Artifact, archiveReady, modelReady, runtimeReady bool) (Plan, error) {
	var plan Plan
	if !archiveReady {
		plan.DownloadBytes += artifact.Size
		plan.RequiredBytes += artifact.Size
		if len(artifact.Parts) > 0 {
			plan.RequiredBytes += artifact.Size
		}
	}
	if !runtimeReady {
		plan.RequiredBytes += artifact.ExpandedSize
	}
	if !modelReady {
		plan.DownloadBytes += model.Size
		plan.RequiredBytes += model.Size
	}
	plan.RequiredBytes += 256 << 20
	// Query the containing volume without scanning the user's media library.
	var err error
	plan.FreeBytes, err = desktopstorage.FreeBytes(dataDir)
	return plan, err
}
