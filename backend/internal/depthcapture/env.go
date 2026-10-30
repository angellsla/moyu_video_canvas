package depthcapture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"infinite-canvas/backend/internal/desktopnet"
	"infinite-canvas/backend/internal/mediatools"
)

func WorkerEnv(toolDir string) []string {
	return WorkerEnvForPlatform(toolDir, runtime.GOOS)
}

func WorkerEnvForPlatform(toolDir, platform string) []string {
	env := workerEnv(toolDir, platform, os.Environ(), os.Getenv)
	// Published Python workers invoke ffprobe/ffmpeg by name. Finder's PATH
	// does not contain the signed tools bundled beside the desktop executable.
	var mediaDirs []string
	for _, resolve := range []func() (string, error){mediatools.ResolveFFprobe, mediatools.ResolveFFmpeg} {
		if path, err := resolve(); err == nil {
			mediaDirs = append(mediaDirs, filepath.Dir(path))
		}
	}
	if len(mediaDirs) > 0 {
		var inheritedPath string
		filtered := make([]string, 0, len(env))
		for _, entry := range env {
			if strings.HasPrefix(entry, "PATH=") {
				inheritedPath = strings.TrimPrefix(entry, "PATH=")
			} else {
				filtered = append(filtered, entry)
			}
		}
		mediaDirs = append(mediaDirs, inheritedPath)
		env = append(filtered, "PATH="+strings.Join(mediaDirs, string(os.PathListSeparator)))
	}
	// Keep the desktop proxy environment available for subprocesses that need
	// it; the depth worker itself stays offline and never repairs assets here.
	return append(env, desktopnet.HTTPSProxyEnvironment()...)
}

func workerEnv(toolDir, platform string, environ []string, getenv func(string) string) []string {
	if getenv == nil {
		getenv = os.Getenv
	}
	// The published worker must use the source snapshot installed beside it and
	// fail locally when an asset is missing. It must never clone or download while
	// executing a user task. Remove inherited values before appending the fixed
	// settings so Windows' case-insensitive environment cannot override them.
	env := make([]string, 0, len(environ)+3)
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "BEEFTV_VDA_SOURCE") || strings.EqualFold(key, "CANVAS_DEPTH_OFFLINE") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"PYTORCH_ENABLE_MPS_FALLBACK=1",
		"BEEFTV_VDA_SOURCE="+filepath.Join(filepath.Dir(toolDir), "vda"),
		"CANVAS_DEPTH_OFFLINE=1",
	)
	if platform == "windows" {
		env = append(env, "PYTHONIOENCODING=utf-8", "PATH="+filepath.Join(filepath.Dir(toolDir), "bin")+string(os.PathListSeparator)+getenv("PATH"))
	}
	return env
}
