package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"infinite-canvas/backend/internal/desktopstorage"
	"infinite-canvas/backend/internal/runtimeinfo"
)

func (a *DesktopApp) AssetStorageSettings() (desktopstorage.AssetUsage, error) {
	a.mu.RLock()
	dataDir := a.dataDir
	a.mu.RUnlock()
	location, err := desktopstorage.ResolveAssetLocation(dataDir)
	if err != nil {
		return desktopstorage.AssetUsage{}, err
	}
	usage, err := desktopstorage.Inspect(location.ResourceDir)
	if err != nil {
		return desktopstorage.AssetUsage{}, err
	}
	result := desktopstorage.AssetUsage{
		ResourceDir: location.ResourceDir,
		UsedBytes:   usage.UsedBytes,
		FreeBytes:   usage.FreeBytes,
		PreviousDir: location.PreviousDir,
	}
	if location.PreviousDir != "" {
		previous, inspectErr := desktopstorage.Inspect(location.PreviousDir)
		if inspectErr == nil {
			result.PreviousBytes = previous.UsedBytes
		}
	}
	return result, nil
}

func (a *DesktopApp) ChooseAssetStorageDirectory() (string, error) {
	ctx, err := a.dialogContext()
	if err != nil {
		return "", err
	}
	selected, err := wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{Title: "选择素材文件保存文件夹"})
	if err != nil || selected == "" {
		return selected, err
	}
	entries, readErr := os.ReadDir(selected)
	if readErr == nil && len(entries) > 0 {
		return filepath.Join(selected, "MoyuCanvasAssets"), nil
	}
	return selected, nil
}

func (a *DesktopApp) MigrateAssetStorage(target string) (desktopstorage.AssetUsage, error) {
	if !a.migrationMu.TryLock() {
		return desktopstorage.AssetUsage{}, errors.New("存储操作正在进行，请等待完成")
	}
	defer a.migrationMu.Unlock()
	a.mu.RLock()
	dataDir := a.dataDir
	a.mu.RUnlock()
	location, err := desktopstorage.ResolveAssetLocation(dataDir)
	if err != nil {
		return desktopstorage.AssetUsage{}, err
	}
	if location.PreviousDir != "" {
		return desktopstorage.AssetUsage{}, errors.New("请先清理上一次迁移留下的旧素材目录")
	}
	source, err := desktopstorage.ResolveAssetDir(dataDir)
	if err != nil {
		return desktopstorage.AssetUsage{}, err
	}
	target, err = desktopstorage.ValidateAssetTarget(dataDir, source, target)
	if err != nil {
		return desktopstorage.AssetUsage{}, err
	}
	ctx, cancelMigration := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancelMigration()
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return desktopstorage.AssetUsage{}, errors.New("应用正在关闭，素材迁移未开始")
	}
	a.migrating = true
	a.migrationCancel = cancelMigration
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.migrating = false
		a.migrationCancel = nil
		a.mu.Unlock()
	}()
	currentRuntime := a.runtime()
	if currentRuntime == nil || !currentRuntime.Ready() {
		return desktopstorage.AssetUsage{}, errors.New("当前工作区尚未就绪，暂不能迁移素材")
	}
	prepareCtx, prepareCancel := context.WithTimeout(ctx, 10*time.Second)
	a.setMigrationProgress(desktopstorage.Progress{Phase: "preparing", Detail: "正在暂停任务并准备素材迁移"})
	resume, prepareErr := currentRuntime.PrepareStorageMigration(prepareCtx)
	prepareCancel()
	if prepareErr != nil {
		return desktopstorage.AssetUsage{}, prepareErr
	}
	defer resume()
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = a.stop(stopCtx)
	cancel()
	if err != nil {
		a.mu.Lock()
		a.closing = true
		a.mu.Unlock()
		return desktopstorage.AssetUsage{}, fmt.Errorf("工作区未能完整关闭，素材目录未更改。请重启墨羽Canvas后重试\n\n%w", err)
	}
	var sourceLock *os.File
	switched := false
	restore := func(cause error) (desktopstorage.AssetUsage, error) {
		if sourceLock != nil {
			_ = sourceLock.Close()
			sourceLock = nil
		}
		a.mu.RLock()
		closing := a.closing
		a.mu.RUnlock()
		if closing {
			return desktopstorage.AssetUsage{}, cause
		}
		if switched {
			if err := desktopstorage.SaveAssetLocation(dataDir, desktopstorage.AssetLocation{ResourceDir: source}); err != nil {
				return desktopstorage.AssetUsage{}, fmt.Errorf("素材迁移未完成，旧素材目录仍完整，但无法恢复位置设置: %w", errors.Join(cause, err))
			}
		}
		old, openErr := openDesktopRuntime(context.Background(), dataDir, a.runtimeLogger)
		if openErr == nil {
			a.installStorageRuntime(dataDir, old)
			return desktopstorage.AssetUsage{}, fmt.Errorf("素材迁移未完成，已恢复原素材目录。原文件与数据库均未更改\n\n%w", cause)
		}
		return desktopstorage.AssetUsage{}, fmt.Errorf("素材迁移未完成，原素材仍保留，但工作区未能重新打开。请重启墨羽Canvas\n\n%w", errors.Join(cause, openErr))
	}
	sourceLock, err = runtimeinfo.LockWorkspace(dataDir)
	if err != nil {
		return restore(err)
	}
	a.setMigrationProgress(desktopstorage.Progress{Phase: "processing", Detail: "正在检查原素材文件"})
	files, err := desktopstorage.Snapshot(source)
	if err != nil {
		return restore(err)
	}
	if err := desktopstorage.CopyClosedWorkspace(ctx, source, target, a.setMigrationProgress); err != nil {
		return restore(fmt.Errorf("素材复制或校验失败，原目录已保留: %w", err))
	}
	if err = desktopstorage.VerifySnapshot(source, files, false); err != nil {
		return restore(err)
	}
	if err = desktopstorage.VerifySnapshot(target, files, false); err != nil {
		return restore(err)
	}
	if err = ctx.Err(); err != nil {
		return restore(err)
	}
	if err = desktopstorage.SaveAssetLocation(dataDir, desktopstorage.AssetLocation{
		ResourceDir: target, PreviousDir: source, PreviousFiles: files,
	}); err != nil {
		return restore(fmt.Errorf("素材已复制，但位置设置未保存: %w", err))
	}
	switched = true
	if err = sourceLock.Close(); err != nil {
		sourceLock = nil
		return restore(err)
	}
	sourceLock = nil
	a.setMigrationProgress(desktopstorage.Progress{Phase: "processing", Detail: "正在重新打开工作区并切换素材目录"})
	next, err := openDesktopRuntime(ctx, dataDir, a.runtimeLogger)
	if err != nil {
		return restore(fmt.Errorf("新素材目录无法打开: %w", err))
	}
	if err = ctx.Err(); err != nil {
		_ = next.Close(context.Background())
		return restore(err)
	}
	a.installStorageRuntime(dataDir, next)
	a.setMigrationProgress(desktopstorage.Progress{Phase: "done", Detail: "素材迁移完成，正在重新载入"})
	return a.AssetStorageSettings()
}

func (a *DesktopApp) CleanupPreviousAssetStorage() (desktopstorage.AssetUsage, error) {
	if !a.migrationMu.TryLock() {
		return desktopstorage.AssetUsage{}, errors.New("存储操作正在进行，请等待完成")
	}
	defer a.migrationMu.Unlock()
	a.mu.RLock()
	dataDir := a.dataDir
	a.mu.RUnlock()
	if a.runtime() == nil || !a.runtime().Ready() {
		return desktopstorage.AssetUsage{}, errors.New("当前工作区尚未就绪，暂不能清理旧素材目录")
	}
	if err := desktopstorage.CleanupPreviousAssetDir(dataDir); err != nil {
		return desktopstorage.AssetUsage{}, err
	}
	return a.AssetStorageSettings()
}

func rebaseMigratedAssetLocation(source, target string) error {
	location, err := desktopstorage.ResolveAssetLocation(source)
	if err != nil || location.PreviousDir == "" {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	previous := filepath.Clean(location.PreviousDir)
	relative, err := filepath.Rel(filepath.Clean(source), previous)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}
	location.PreviousDir = filepath.Join(target, relative)
	return desktopstorage.SaveAssetLocation(target, location)
}
