# 墨羽Canvas Skills 接入包

提供公开技能搜索、固定版本和文件读取工作流，以及只读 Skills MCP 连接。默认公共源沿用原技能市场服务；该连接不涉及原账号登录、模型渠道、投稿或桌面更新。

- `plugin.json`、`mcp.json`：Agent Plugins 接入清单。
- `.claude-plugin/plugin.json`、`mcp.claude.json`：Claude Code 接入清单。
- `skills/find-canvas-skill/SKILL.md`：搜索、读取、下载与安装流程。

MCP 只负责查询和读取；下载或安装通过随包 CLI 的 `skills fetch` / `skills install` 保存到本机。项目中保留的 `beeftv` 是兼容 CLI 命令名，不是产品显示名称。安装文件不代表宿主已启用，不执行 shell hook 或包管理器生命周期脚本。

默认源：`https://beeftv.app/api/beeftv/skillhub/v1`；MCP 在该地址下的 `/mcp`。自建源需同步修改 MCP 清单；本地后端和 CLI 可使用 `CANVAS_SKILL_MARKET_URL`。不要求配置发布包验签公钥，也不提供内容审核或投稿发布流程。下载完整性、解压路径和文件大小保护仍由客户端处理。

本目录是接入包源码，不表示已经安装、启用或被宿主目录收录。本轮仅进行代码检查，未进行宿主运行验证。
