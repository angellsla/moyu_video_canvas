# OpenClaw 启动入口失效 Node 路径：读回、验证与恢复记录（2026-10-28）

本文件记录 2026-10-28 一次本机启动故障的现场读回、实际执行过的验证和恢复方法。任务目标只有一个：恢复用户已批准的 `openclaw` / Mem 云端 Claude Pro 官方 OAuth / `mem-claude/opus-5-5` 复查流程能正常启动（#97）。本文件不包含凭据、完整私密配置或客户数据。

## 一、结论

1. **直接原因**：`/Users/sunny/.local/bin/openclaw` 是两行启动脚本，第 2 行把解释器固定为 `/Users/sunny/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node`。该路径属于 Codex CLI 的「primary runtime」依赖缓存目录（`~/.cache/codex-runtimes`），会由 Codex 自己创建/清理；本次整个 `~/.cache/codex-runtimes` 已不存在，于是每次启动都是 `exec: ...: cannot execute: No such file or directory`，进程以 126 退出，桥接脚本和模型都没跑起来。
2. **时间线**（`~/.tds/daemon.log`）：最后一次 openclaw 探测成功 `2026-10-27T17:16:20Z`；第一次失败 `2026-10-27T18:02:14Z`；此后累计 106 次 `openclaw probe failed`，最近一次 `2026-10-28T03:05:08Z`（北京时间 11:05）。
3. **本轮我改了什么**：**没有修改任何启动脚本**。勘察期间（北京时间 11:05:47–11:05:57）已有另一个进程把 `openclaw`、`memoh`、`memoh-chat` 三个启动脚本重写成「候选 Node 列表」形式；按本次指令保留该现状，我只做只读验证，并另建本地备份目录。我唯一的写入是：新增备份目录，以及清掉我自己验证时临时写进会话路由表的 3 个测试键。
4. **修复是否有效**：有效。平台侧在 `2026-10-28T03:09:29Z` 探测回 `openclaw probed in 0.6s: ok, 91 model(s)`（11:05 改动之后的第一次成功），之后 `daemon.log` 再无 openclaw 失败行。用户 11:29:34 重试 #97 后，`openclaw` 桥接已正常进入代码复查（见第四节「实际运行证据」），不再是 126 错误。

## 二、当前启动入口（读回，未改动）

`/Users/sunny/.local/bin/openclaw`（sha256 `62e7415eb95052a19293ff1da4539347421aa1a3f027469af189dccfa08d8a35`，mtime 2026-10-28 11:05:47）：

```sh
#!/bin/sh
for N in /opt/homebrew/bin/node /Users/sunny/.local/bin/node /Users/sunny/.nvm/versions/node/v24.22.1/bin/node "$(command -v node 2>/dev/null)"; do
  if [ -n "$N" ] && [ -x "$N" ]; then
    exec "$N" "/Users/sunny/.local/share/memoh/memoh-openclaw-acp.mjs" "$@"
  fi
done
exec node "/Users/sunny/.local/share/memoh/memoh-openclaw-acp.mjs" "$@"
```

要点：桥接脚本路径、`"$@"` 参数转发、标准输入输出行为与原来完全一致，只有解释器从单一失效缓存路径换成候选列表。`/Users/sunny/.local/bin/memoh` 指向同一桥接脚本，内容相同；`/Users/sunny/.local/bin/memoh-chat` 同形，指向 `memoh-chat.mjs`。

被替换掉的原内容（我读回后另存备份，未回写）：

```sh
#!/bin/sh
exec "/Users/sunny/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node" "/Users/sunny/.local/share/memoh/memoh-openclaw-acp.mjs" "$@"
```

**桥接本体未变**：`/Users/sunny/.local/share/memoh/memoh-openclaw-acp.mjs` mtime 仍为 2026-10-27 20:11:31，sha256 `2dfa8f66caddc22b3caa2c707153ef96818c497dff492feac05133515efb4d90`。模型目录、路由标签、`/model` 处理、云端主机选择等逻辑均未改动。

### 谁改的（只做读回，未追溯）

- 改动时间：`openclaw` 11:05:47、`memoh` 11:05:49、`memoh-chat` 11:05:52、`memoh-chat.mjs` 11:05:57；同批还有 `wecom-inbox`（与本次故障无关）。同一时间窗内有并行的 Codex 会话在跑，未定位到具体作者，按指令不再追溯。
- 生成器：本机没有找到写这些启动脚本的安装脚本（`~/.local/share/memoh/` 下的 `provision-cloud.mjs`、`build-cloud-bundle.mjs` 只处理云端机 `/home/user/.local/bin`），因此这更像手工创建后被缓存清理打穿；这一点是推测，不是已验证事实。
- 附带修复：`memoh-chat.mjs` 里调用 Codex 缓存 python 的地方已改为 `/usr/bin/python3`（系统 Python 3.9.6，存在）。该文件不属于本次桥接链路。

## 三、实际执行的检查（本机只读/无付费）

| 检查 | 命令/方式 | 结果 |
| --- | --- | --- |
| 候选 Node 可用性 | `-v` 逐个执行 | `/opt/homebrew/bin/node` = v26.0.0；`~/.local/bin/node` → `~/.hermes/node/bin/node` = v22.23.4；`~/.nvm/versions/node/v24.22.1/bin/node` = v24.22.1；全部存在且可执行 |
| 桥接脚本自述 | `openclaw --version` | `openclaw-memoh-bridge 4.0.0`，退出 0 |
| 平台探测命令 | `openclaw models list --json` | 退出 0，91 个模型；含 `mem-claude/opus-5-5`、`mem-claude/sonnet-5-5` |
| 网关 | `openclaw gateway status --json` | `{"rpc":{"ok":true},"provider":"memoh+local-hub","accounts":[...]}` |
| ACP 握手 | 对 `openclaw acp` 发 `initialize` | 三个候选 Node 各自都返回 `agentInfo.name = memoh-openclaw-acp`、`protocolVersion 1` |
| ACP 完整入口 | `initialize` → `session/new` → `/model mem-claude/opus-5-5 -s` | 返回 `Model set to mem-claude/opus-5-5`，退出 0，stderr 为空（`-s` 与平台 `selectModel` 实际发送的命令一致，回复命中平台的 `Model set to ` 判定） |
| 不依赖继承 PATH | `env -i HOME=/Users/sunny PATH=/usr/bin:/bin openclaw --version` / `acp` 握手 | 均成功，说明入口不再依赖调用方 PATH |
| 云端主机可达性 | 对两台云端 CLI 主机（Neko、Mem）的 `13425` 端口做 TCP 连接 + `GET /health` | 两台都 TCP 开放、`/health` 200 |
| 平台侧探测 | `~/.tds/daemon.log` | 11:05 改动后 `03:09:29Z` 回 `ok, 91 model(s)`；此后无 openclaw 失败行 |
| 会话路由表 | `~/.tds/openclaw-home/memoh-sessions.json` | 未被本次验证污染（测试键已清理，见第五节） |

未做：没有发任何真实模型请求或付费请求，没有执行 #97 的复查本身，没有重启 Todos 桌面应用、Gateway 或 memoh runtime，没有升级 Todos 应用。

## 四、实际运行证据（用户 11:29:34 重试 #97）

- `ps`：pid 57832，启动于 2026-10-28 11:29:37，命令 `/opt/homebrew/bin/node /Users/sunny/.local/share/memoh/memoh-openclaw-acp.mjs acp`，父进程为 Todos Helper（pid 8189）。即平台确实通过新入口拉起了桥接，而不是报 126。
- `~/.tds/openclaw-home/tds-sessions/01a18070-8026-7816-a61b-a5d656ecd324.json`：`{"model":"mem-claude/opus-5-5", ...}`，即该会话已把模型切到用户指定的 `mem-claude/opus-5-5`。
- `~/.tds/daemon.log` `03:29:34Z`：`step rdWNLV35NR7MkSkXYtSLv for conv 01a18070-7fcd-7352-b544-36b5fc91d21d`（#97 的会话），随后工作树被复用并进入代码复查。

## 五、我做的写入与恢复方法

1. **备份目录**：`/Users/sunny/.local/state/openclaw-launcher-backup-2026-10-28/`
   - `openclaw.broken-original`：我读回的两行失效脚本（原始内容，供对照）。
   - `openclaw.after-1105`：11:05 那份候选列表版本的副本。
2. **恢复方法**：把要用的版本复制回入口即可，例如
   `cp /Users/sunny/.local/state/openclaw-launcher-backup-2026-10-28/openclaw.after-1105 /Users/sunny/.local/bin/openclaw && chmod +x /Users/sunny/.local/bin/openclaw`。
   若要回到失效前的行为，用 `openclaw.broken-original`，但那会重新依赖已被清理的 Codex 缓存路径，不建议。
3. **会话路由表**：验证期间我临时写入 3 个测试键（`model:repair-check-*`、`model:tds-repair-platcheck`），已全部删除；当前表内容与验证前一致，仅多出一条平台自己在 11:29 写入的真实键 `model:tds-01a18070-8026-7816-a61b-a5d656ecd324 = mem-claude/opus-5-5`（#97 的会话，不属于我的验证残留）。

## 六、剩余限制与风险

1. **候选列表里有一个会腐烂的路径**：`~/.nvm/versions/node/v24.22.1/bin/node` 是写死的 nvm 版本目录，nvm 升级/清理后即失效。它只排在第三位，前两位（Homebrew、`~/.local/bin/node`）失效前不影响启动；但如果哪天要重写这个脚本，建议改成「按优先级探测 + 明确失败提示」，不要再写死版本号。
2. **末行 `exec node`** 依赖调用方 PATH，只在前面候选全部不存在时才走到，属于兜底，不是当前生效路径。
3. **写入者未知**：本机没有找到生成这些启动脚本的安装脚本，所以无法保证下次系统/工具更新不会再把路径写回缓存目录。若再出现同样报错，先看 `/Users/sunny/.local/bin/openclaw` 第 2 行指向哪。
4. **未验证范围**：没有跑真实模型回合（无付费请求），因此「云端 Claude Pro OAuth 本身是否仍有效」未由本任务确认；只有云端主机 `13425 /health` 返回 200 与平台会话记录证明链路可达、模型键可选。
5. **无关的既有噪声**：`daemon.log` 里 `opencode 1.18.34 is too old` 探测失败与本次故障无关，未处理。
6. **同类桥接入口**：`memoh`、`memoh-chat` 在 11:05 同批被改成同形脚本，现状一致；`memoh-chat.mjs` 已改用系统 Python，属于同批改动，未再改动。
