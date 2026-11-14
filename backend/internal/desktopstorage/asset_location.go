package desktopstorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const assetLocationFile = "asset-storage-location.json"

type AssetLocation struct {
	ResourceDir   string            `json:"resourceDir"`
	PreviousDir   string            `json:"previousDir,omitempty"`
	PreviousFiles map[string]string `json:"previousFiles,omitempty"`
}

type AssetUsage struct {
	ResourceDir   string `json:"resourceDir"`
	UsedBytes     int64  `json:"usedBytes"`
	FreeBytes     uint64 `json:"freeBytes"`
	PreviousDir   string `json:"previousDir,omitempty"`
	PreviousBytes int64  `json:"previousBytes,omitempty"`
}

func ResolveAssetLocation(dataDir string) (AssetLocation, error) {
	defaultDir, err := canonical(filepath.Join(strings.TrimSpace(dataDir), "resources"))
	if err != nil {
		return AssetLocation{}, err
	}
	body, err := os.ReadFile(filepath.Join(dataDir, assetLocationFile))
	if errors.Is(err, os.ErrNotExist) {
		return AssetLocation{ResourceDir: defaultDir}, nil
	}
	if err != nil {
		return AssetLocation{}, fmt.Errorf("读取素材存储位置失败: %w", err)
	}
	var location AssetLocation
	if err := json.Unmarshal(body, &location); err != nil {
		return AssetLocation{}, fmt.Errorf("素材存储位置设置损坏: %w", err)
	}
	location.ResourceDir, err = canonicalConfiguredDir(location.ResourceDir)
	if err != nil {
		return AssetLocation{}, err
	}
	workspaceDir, err := canonical(dataDir)
	if err != nil {
		return AssetLocation{}, err
	}
	if contains(location.ResourceDir, workspaceDir) {
		return AssetLocation{}, errors.New("素材目录不能包含工作区数据库目录")
	}
	if location.PreviousDir != "" {
		location.PreviousDir, err = canonicalConfiguredDir(location.PreviousDir)
		if err != nil {
			return AssetLocation{}, err
		}
		if contains(location.PreviousDir, workspaceDir) {
			return AssetLocation{}, errors.New("旧素材目录不能包含工作区数据库目录")
		}
		if location.PreviousFiles == nil {
			return AssetLocation{}, errors.New("素材迁移记录不完整")
		}
	}
	return location, nil
}

func ResolveAssetDir(dataDir string) (string, error) {
	location, err := ResolveAssetLocation(dataDir)
	if err != nil {
		return "", err
	}
	info, statErr := os.Stat(location.ResourceDir)
	if statErr == nil && !info.IsDir() {
		return "", errors.New("素材存储位置不是文件夹")
	}
	if statErr == nil {
		return location.ResourceDir, nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("素材存储位置无法访问: %w", statErr)
	}
	if location.PreviousDir != "" {
		return "", fmt.Errorf("素材存储位置无法访问，请连接对应磁盘后重试: %s", location.ResourceDir)
	}
	if _, configErr := os.Stat(filepath.Join(dataDir, assetLocationFile)); configErr == nil {
		return "", fmt.Errorf("素材存储位置无法访问，请检查磁盘或目录: %s", location.ResourceDir)
	} else if !errors.Is(configErr, os.ErrNotExist) {
		return "", configErr
	}
	return location.ResourceDir, nil
}

func ValidateAssetTarget(dataDir, source, target string) (string, error) {
	dataDir, err := canonical(dataDir)
	if err != nil {
		return "", err
	}
	source, err = canonical(source)
	if err != nil {
		return "", err
	}
	target, err = canonical(target)
	if err != nil {
		return "", err
	}
	if contains(source, target) || contains(target, source) {
		return "", errors.New("新素材目录不能与当前素材目录重叠")
	}
	if contains(dataDir, target) || contains(target, dataDir) {
		return "", errors.New("新素材目录不能与工作区数据目录重叠，请选择其他文件夹")
	}
	return Validate(source, target)
}

func SaveAssetLocation(dataDir string, location AssetLocation) error {
	resourceDir, err := canonicalConfiguredDir(location.ResourceDir)
	if err != nil {
		return err
	}
	location.ResourceDir = resourceDir
	workspaceDir, err := canonical(dataDir)
	if err != nil {
		return err
	}
	if contains(resourceDir, workspaceDir) {
		return errors.New("素材目录不能包含工作区数据库目录")
	}
	if location.PreviousDir == "" {
		location.PreviousFiles = nil
	} else {
		location.PreviousDir, err = canonicalConfiguredDir(location.PreviousDir)
		if err != nil {
			return err
		}
		if contains(location.PreviousDir, workspaceDir) {
			return errors.New("旧素材目录不能包含工作区数据库目录")
		}
		if location.PreviousFiles == nil {
			return errors.New("素材迁移记录不完整")
		}
	}
	body, err := json.Marshal(location)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dataDir, ".asset-location-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporaryPath, filepath.Join(dataDir, assetLocationFile)); err != nil {
		return err
	}
	return nil
}

func CleanupPreviousAssetDir(dataDir string) error {
	location, err := ResolveAssetLocation(dataDir)
	if err != nil {
		return err
	}
	if location.PreviousDir == "" {
		return errors.New("没有可清理的旧素材目录")
	}
	if contains(location.ResourceDir, location.PreviousDir) || contains(location.PreviousDir, location.ResourceDir) {
		return errors.New("旧素材目录与当前目录重叠，无法清理")
	}
	if _, err := os.Stat(location.PreviousDir); errors.Is(err, os.ErrNotExist) {
		return SaveAssetLocation(dataDir, AssetLocation{ResourceDir: location.ResourceDir})
	} else if err != nil {
		return err
	}
	if err := VerifySnapshot(location.PreviousDir, location.PreviousFiles, true); err != nil {
		return err
	}
	for relative := range location.PreviousFiles {
		if !filepath.IsLocal(relative) || filepath.Clean(relative) != relative {
			return errors.New("素材迁移记录无效，无法清理")
		}
	}
	for relative := range location.PreviousFiles {
		if err := os.Remove(filepath.Join(location.PreviousDir, relative)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("旧素材目录清理未完成，当前素材不受影响: %w", err)
		}
	}
	var directories []string
	if err := filepath.WalkDir(location.PreviousDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && entry.IsDir() {
			directories = append(directories, path)
		}
		return walkErr
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Remove(directories[index]); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return SaveAssetLocation(dataDir, AssetLocation{ResourceDir: location.ResourceDir})
}

func canonicalConfiguredDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Clean(path)) == filepath.Clean(path) {
		return "", errors.New("素材存储位置必须是磁盘中的绝对文件夹")
	}
	return canonical(path)
}
