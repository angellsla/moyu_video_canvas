# 本地桌面版快速开始

桌面版是本地优先工作区。项目、画布、素材、任务记录和模型配置默认保存在本机；只有执行生成时，才会按你配置的渠道请求外部模型服务。

## 下载与首次打开

从[墨羽Canvas发布页](https://github.com/glanderness/BeefTV/releases/latest)下载对应系统的安装包。Mac 的 Apple 芯片选择 `darwin-arm64`，Intel 芯片选择 `darwin-amd64`；解压后将应用程序放入「应用程序」。

目前 Mac 版本尚未完成 Apple 开发者签名和公证。首次打开时，如果系统提示无法验证应用是否包含恶意软件，请确认安装包来自墨羽Canvas发布页，再打开「系统设置 → 隐私与安全性」，找到墨羽Canvas并点击「仍要打开」，按系统提示确认。受组织管理的 Mac 可能需要联系管理员。

这是 macOS 对未公证应用的检查，换一个下载地址不会消除它。无需关闭整个系统的安全检查。具体操作见 [Apple 的打开应用说明](https://support.apple.com/zh-cn/102445)。

当前版本不提供应用内更新检测。升级时请从 GitHub Release 下载对应平台的安装包并重新安装；安装包完整性校验和 Apple 公证仍是两件独立的事。

## 构建桌面版

环境要求：Go 1.25、Bun，以及 Wails 所需的系统组件。

在仓库根目录执行：

```bash
BEEFTV_GO_DIR=/path/to/go ./scripts/build-beeftv-release.sh
```

macOS 应用输出到 `backend/cmd/desktop/build/bin/VideoCanvas.app`；应用内显示名称为“墨羽Canvas”。

Windows amd64 必须在 Windows 本机构建（需要 PATH 中的 Go、Bun，以及编译 go-sqlite3 的 GCC）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-beeftv-windows-release.ps1
```

输出为 `backend\cmd\desktop\build\bin\VideoCanvas.exe`；内置插件在旁边的 `plugin-packages\`。前提与数据目录见 `docs/desktop-release.md`。

首次打开后直接进入本地工作区，不需要注册或登录。

## 配置模型

打开“模型配置”。使用 New API 中转站时，在 New API 模型渠道中输入账号信息并同步模型；其他渠道填写各自的 Base URL 和 API Key。账号信息仅用于同步，模型 Key 保存到本地渠道配置，不会写入画布、素材或任务列表。也可添加其他兼容服务。

模型请求会访问你选择的模型服务。项目数据、素材、任务历史和模型配置默认保存在本地工作区；素材按模型渠道要求传输，不会同步到本产品的云端存储。

## 本地开发与验证

需要同时调试前后端时，可参考 `scripts/beeftv-shared-dev.sh`。验证本地发行边界：

```bash
BEEFTV_GO_DIR=/path/to/go ./scripts/verify-beeftv-local-release.sh
```

## 用命令行批量操作

安装包自带 `beeftv` 命令行工具，可以直接用 shell 脚本批量读取和整理画布、素材与任务，不需要经过助手，也不需要启动 MCP 服务。macOS 的路径是 `/Applications/VideoCanvas.app/Contents/MacOS/cli/beeftv`，Windows 是 `VideoCanvas.exe` 同级的 `cli\beeftv.exe`。

在侧栏的「外部 Agent」页选择工具并生成连接配置，配置里包含 `BEEFTV_DATA_DIR`、`BEEFTV_CLIENT_ID` 与 `BEEFTV_CLIENT_TOKEN`，把它们设为环境变量即可；只读凭据只能读。纯命令行批量操作不需要启动 MCP。完整用法见 [beeftv 命令行基础使用](docs/content/docs/backend/beeftv-cli.mdx)。

## 数据位置与备份

桌面运行时会在本地工作区保存 SQLite 数据库、资源文件、模型配置和迁移备份。升级迁移前会自动创建数据库备份；如需迁移或恢复，请先退出应用并完整复制工作区数据目录。

本地工作区无需注册产品账号。New API 登录只用于同步用户选择的模型服务 Key 与模型目录；不会改变本地项目、素材和历史记录的保存位置。详见 `docs/local-first-architecture.md`。
