// Package bootstrap_assets contains the small, audited source snapshot used by
// the managed Windows depth runtime. The Python embed and model weights remain
// downloads; only code that must be tied to the application release is embedded.
package bootstrap_assets

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Assets is intentionally immutable at runtime. Keep this tree in sync with
// the worker and the pinned Video-Depth-Anything commit recorded in SNAPSHOT.
//
//go:embed all:worker all:vda LICENSE SNAPSHOT.txt
var Assets embed.FS

const VDACommit = "4f5ae23172ba60fd7bc11ef671cca678842c7072"

// Write installs the embedded source snapshot below root. Existing files are
// replaced only after their parent is created; callers publish the containing
// runtime atomically after this function returns.
func Write(root string) error {
	if strings.TrimSpace(root) == "" || filepath.IsAbs(root) == false {
		return errors.New("深度组件源代码目录必须是绝对路径")
	}
	return fs.WalkDir(Assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		destination := filepath.Join(root, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o750)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("深度组件源快照包含非普通文件: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return err
		}
		data, err := Assets.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o640)
	})
}
