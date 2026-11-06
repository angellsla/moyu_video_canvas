import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { CanvasTopBar } from "@/pages/canvas/canvas-project-top-bar";

test("CanvasTopBar keeps sync conflicts visible in the compact chrome without redundant view switches", () => {
    const noop = () => {};
    const html = renderToStaticMarkup(<CanvasTopBar
        libtvChrome syncStatus={<button>画布已在别处更新 · 使用最新版本</button>}
        workspaceView="workflow" onWorkspaceViewChange={noop} versionsOpen={false} onToggleVersions={noop}
        title="未命名工作区" titleDraft="未命名工作区" isTitleEditing={false} onTitleDraftChange={noop} onStartTitleEditing={noop} onFinishTitleEditing={noop} onCancelTitleEditing={noop}
        canUndo={false} canRedo={false} onCreateCanvas={noop} onDeleteProject={noop} onExportAssets={noop} onSave={noop} onForceSave={noop} onImportImage={noop} onImportLibTV={noop} onImportTapNow={noop} onUndo={noop} onRedo={noop}
        shortcutRequestNonce={0} mediaPerformanceMode="auto" onMediaPerformanceModeChange={noop} onOpenSearch={noop} onEnterFocusMode={noop}
    />);

    expect(html).not.toContain('aria-label="工作流"');
    expect(html).not.toContain('aria-label="故事板"');
    expect(html).toContain("画布已在别处更新 · 使用最新版本");
});

test("CanvasTopBar places collaboration status immediately after the canvas navigation", () => {
    const noop = () => {};
    const html = renderToStaticMarkup(<CanvasTopBar
        collaborationStatus={<div role="status">协作已连接</div>}
        workspaceView="workflow" onWorkspaceViewChange={noop} versionsOpen={false} onToggleVersions={noop}
        title="测试工作区" titleDraft="测试工作区" isTitleEditing={false} onTitleDraftChange={noop} onStartTitleEditing={noop} onFinishTitleEditing={noop} onCancelTitleEditing={noop}
        canUndo={false} canRedo={false} onCreateCanvas={noop} onDeleteProject={noop} onExportAssets={noop} onSave={noop} onForceSave={noop} onImportImage={noop} onImportLibTV={noop} onImportTapNow={noop} onUndo={noop} onRedo={noop}
        mediaPerformanceMode="auto" onMediaPerformanceModeChange={noop} onOpenSearch={noop} onEnterFocusMode={noop}
    />);

    const navigationEnd = html.indexOf('class="canvas-topbar-collaboration-status');
    expect(navigationEnd).toBeGreaterThan(html.indexOf("canvas-topbar-project-cluster"));
    expect(html.indexOf("协作已连接")).toBeGreaterThan(navigationEnd);
    expect(html.indexOf("协作已连接")).toBeLessThan(html.indexOf('class="canvas-topbar-cluster canvas-topbar-local-cluster'));
});
