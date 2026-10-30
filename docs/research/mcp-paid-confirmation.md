# 外部 MCP / CLI 如何确认付费：对照 BeefTV 重构方案

调研日期：2026-10-26。范围：外部调用方的费用告知、确认交互与授权可信度；不重新设计任务状态或付费防重系统。本文仅为调研和修改建议，未改业务代码、未开 PR、未执行真实付费操作。

“出处”表示对规范、官方文档或源码的中文转述；“推断 / 建议”表示本报告据此作出的判断，不表示规范要求或用户已批准实施。示例金额、字段和新增命令都是设计示例。

## 1. 结论

1. **用户可以在 Codex、Claude Code 等调用方自己的界面确认，无须一律返回 BeefTV 桌面。** MCP elicitation 就是服务端经客户端向用户询问的机制；Codex 和 Claude Code 都有 form / URL 路径。#17 的“只有桌面能申请确认 ID，外部一律不行”应修改。[M1] [C2] [A1] [B5]
2. **普通工具审批不能保证用户看见了本次金额。** 它常发生在执行前，而价格可能执行后才返回；客户端也可能记住允许规则、使用自动审核或自动回答 elicitation。应先产生费用预览，再把这份费用写进本次确认信息；不能只给工具说明加一句“可能收费”。这是根据调用时序及客户端配置得出的建议。[C1] [C3] [A2] [A3] [U2]
3. **标准 MCP 没有通用的“真人已确认这笔金额”的可验证证明。** 注解只是提示；form 的 `accept`、CLI 的 `--yes` 或调用方的确认声明都来自调用程序。它们可以承担受委托客户端的审批责任，不能被描述为 BeefTV 独立证明了真人点击。需要更强保证时，采用独立认证的确认页面，或另行验证可信签名凭据。[M2] [M3] [A3] [C4]
4. **推荐保留现有“外部由调用方审批”的产品分工，补足费用预览和绑定校验。** BeefTV 决定是否会重新提交上游、价格是否已知、输入是否变化；调用方负责向用户呈现并取得同意；后台将同意限定到本次输入及一次接纳。这是本文建议，来源是用户“在他们页面确认”的要求和已有实现，不是 MCP 规定。[B1] [B2] [M1]
5. **Chief 的“先返回需确认与费用，再同意后重提”方向正确，但“带声明”须说明信任谁，“一次同意只扣一次”须限定保证范围。** 服务器签发的报价 ID 只能证明报价未被换掉，不能证明人点过确认；本地只创建一个任务，也不自动证明上游全链路只计费一次。[M3] [S2] [B5]

## 2. BeefTV 与 #17 的实际情况

本工作树基线为 `542124baadcdbe5ce34ba9029ef7f1d192b49d16`；#17 按任务记录所指分支取回，审阅文件所在提交为 `c064b32902f7ad67395b67380062906b719c294d`。以下是代码阅读结果，不是运行验收。

| 出处 | 已存在的行为 | 对本题的意义 |
| --- | --- | --- |
| `operations/generation_submit.go` | `canvas.generation.submit` 明说外部 MCP/CLI 由调用方审批；外部必须给画布版本、模型配置版本和稳定 `operationId`。内置助手走已确认制作授权。[B1] | 现有产品已经接受“委托调用方审批”。版本检查不等于费用确认。 |
| `operations/ops.go` | `canvas.generation.propose` 返回模型、节点、`proposalId` 和两个版本；这里的返回结构没有具体金额或价格上限。[B2] | 可以扩展已有预览能力，不必另建一套制作方案系统。 |
| `cmd/beeftv/main.go`、`business.go` | MCP 工具只填 `ReadOnlyHint`；返回 JSON 放在文本内容。未见 elicitation 调用、费用确认对话或 `structuredContent` 实现。CLI 写操作已有操作 ID。[B3] [B4] | MCP SDK 的存在不表示已经实现了用户确认。 |
| `handler/agent_ops.go` | `/ops/:op` 校验本机来源、用户和登记客户端身份；CLI、MCP 和内置宿主进入同一操作层，但调用者身份有区分。[B6] | 新增审批方式必须依靠服务器识别的身份，不能让请求正文自称桌面或内置助手。 |
| `handler/business_catalog.go` | 另有直接业务接口工具：`/tasks`、`/tasks/:id/retry` 等被标记 `submitsGeneration=true`、`cost=selected-channel`，说明由外部审批；`/tasks` 子路径广泛可发现。[B7] | 只改 `canvas.generation.submit` 不够；直接业务接口也得经过同一付费检查。 |
| 两份插件配置 | 都启动 `beeftv mcp serve`，即本地 stdio MCP。[B8] [B10] | 当前接入不等同于已经拥有远程 OAuth、独立网页确认或新协议支持。 |
| #17 第 3.1、3.3、5.3、7 节 | 确认 ID 仅桌面申请；外部遇到 `confirm` 返回 409、要求去桌面；一次确认绑定操作并在接纳事务中消费。[B5] | 后半部分可保留；桌面独占这一条与本次用户要求冲突。 |

**需要纠正的补充事实：** 现有 `canvas.generation.submit` 的说明只接受尚未关联任务的节点，不能据此说外部已经有一套统一的“已存在任务再次生成”确认工具；该路径还涉及业务接口中的 `/retry`。本报告不沿用“只需给现有 submit 加一个确认字段就够”的假设。[B1] [B7]

## 3. MCP 规范提供什么

### 3.1 先分清三件事

| 机制 | 出处明确说明 | 对付费确认的判断 |
| --- | --- | --- |
| 工具调用审批 | Tools 章节建议保留人工拒绝能力、展示确认；不规定唯一交互界面。[M4] | 由宿主控制，可在调用方 UI 出现；不是每次都弹窗的协议保证。 |
| form elicitation | 服务端描述问题和表单，客户端询问用户，返回 `accept` / `decline` / `cancel` 和数据。[M1] | 适合确认“按这份预览再生成一次”；不收卡号、支付凭据、密钥。 |
| URL elicitation | 引导用户到隔离的外部页面完成敏感交互；`accept` 仅表示同意进入流程，不表示完成。[M1] | 适合支付页面、第三方认证；必须查服务器自己的完成记录，不能拿 `accept` 直接执行。 |
| MCP Authorization | 处理 HTTP 客户端访问受限服务的授权；stdio 使用其环境提供的凭据机制。[M3] | 解决谁能调用、能调用什么，不自动解决用户是否同意了本次金额。 |

上述最后一列是场景判断；规范没有要求所有花已有额度的操作都必须跳转支付页面。只询问已存账户的消费意愿、且不收集凭据，可以使用 form；收集支付凭据等敏感信息必须使用 URL 等隔离流程。[M1] [A4]

### 3.2 工具注解不是“付费审批开关”

规范对 `ToolAnnotations` 的原话是：**“all properties in ToolAnnotations are hints”**。不能把来自不可信服务的提示当作可靠行为保证。下表含义及默认值均来自 schema。[M2]

| 注解 | 含义及默认值 | BeefTV 应怎么用（建议） |
| --- | --- | --- |
| `readOnlyHint` | 是否不修改环境；默认 `false` | 状态查询、纯预览可为 `true`；真正提交生成为 `false`。免费取回会写资源或任务状态，不能因为免费就标只读。 |
| `destructiveHint` | 非只读工具是否可能破坏性更新；默认 `true` | 按覆盖、删除等实际行为标记；它不是 `paidHint`。只新增资源但收费的工具，也不能因设置 `false` 就免审批。 |
| `idempotentHint` | 相同参数重复调用是否无额外影响；默认 `false` | 只有确实通过稳定操作 ID 等实现重放安全才标 `true`。标记本身不会产生防重能力。 |
| `openWorldHint` | 是否与开放的外部实体交互；默认 `true` | 按实际可访问的对象范围填写；仅有网络访问不必然是开放世界。允许任意自定义上游与限定私人工作区的行为需分别判断；该字段不表示费用或同意。[C7] |

**推断：** 这组标准注解没有通用的金额、币种、重复付费原因或强制真人批准字段。这些内容应放入工具参数、结果和确认信息；自定义 `_meta` 只能在明确支持它的客户端上使用。[M2] [A1]

### 3.3 不能混用两个协议版本

调研时官方 `latest` 指向 **`2026-07-28`**，不能再称 `2025-11-25` 为当前最新版。[M0]

| 连接使用的协议 | elicitation 的承载方式 | 实现注意 |
| --- | --- | --- |
| `2025-11-25` | 初始化声明 `elicitation.form/url`；工具执行过程中发服务端 `elicitation/create` 请求；URL 流程有 `elicitationId`，可发完成通知。[M5] | 只向声明支持该模式的客户端请求。`elicitation: {}` 相当于仅支持 form。 |
| `2026-07-28` | 每次请求携带客户端能力；通过 `resultType: "input_required"`、`inputRequests` 返回询问，客户端重发并附 `inputResponses`、`requestState`。[M6] [M7] | MRTR 取代旧服务端反向请求，属于破坏性变化；重发 JSON-RPC ID 要更换，业务操作 ID 要保留。 |

**建议：** 把“取得本次批准”留在一个业务合同里，由协议适配层选择两种承载方式，不把业务任务改造成 MCP 任务状态机。是否升级当前 SDK 另行评估；本文没有验证当前 `go-sdk v1.8.0` 的完整新版兼容性。[B9] [M7]

## 4. 客户端实际支持和限制

本表的“支持”仅指下列官方文档或源码证据。没有安装、启动四种客户端逐一实测，也没有验证具体账号的灰度开关。

| 客户端 | 普通工具审批 | form / URL elicitation | 实际限制与证据 |
| --- | --- | --- | --- |
| Codex | 当前配置允许服务器级或工具级 `auto / prompt / writes / approve`；`writes` 对非只读工具询问。[C1] | App Server 明确提供 form 与 URL 请求；TUI 有布尔表单及通用 URL 流程实现和测试。[C2] [C3] [C5] | 不能承诺“所有 Codex 默认每笔都问”。源码还有记住本会话/以后允许的选项。当前 TUI 数字表单存在不支持后的降级测试，宜用文字金额 + 简单布尔/枚举确认。[C3] |
| Claude Code | 权限规则与模式控制审批；服务端可在工具 `_meta` 加 `anthropic/requiresUserInteraction: true`，要求每次人工审批，普通 allow 规则和 `auto` / `bypassPermissions` 不能跳过。[A1] [A2] | 官方明确支持 form 和 URL；v2 运行时可与支持的服务使用 `2026-07-28`。[A1] | 该 `_meta` 是 Anthropic 扩展，不是全体 MCP 客户端合同；SDK `canUseTool` 仍需应用正确呈现 UI。普通 elicitation 可由 `Elicitation` hook 自动回答。[A1] [A3] |
| Cursor | 可按工具批准；3.6 引入 Auto-review，允许列表直接运行，其余可由分类器判断或询问。[U1] [U2] | 1.5 官方更新明确支持 schema 表单 elicitation。[U3] | **本次未找到可确认 URL mode 与 `2026-07-28` 全部行为的官方承诺，列为未核实。** 不能由“支持 elicitation”推导所有模式均可用。 |
| Claude Desktop | 官方连接器权限有 `Always allow / Needs approval / Blocked`；远程连接器和本地扩展均可使用。[D1] | 本次官方资料未提供足够明确、按版本和传输区分的 form / URL 支持合同，列为**待实测**。Anthropic 的技术文章只明确点名 Claude Code 支持 URL mode。[D1] [A4] | 不能把 Claude Code 的支持直接套给 Desktop，也不能把未核实写成不支持。准备工具审批与结构化结果的备用路径。 |

### 4.1 Codex 如何配合

**出处：** Codex 的每工具策略可覆盖服务器默认策略；App Server 将服务端询问交给宿主，返回接受、拒绝或取消。源码在“需要审批，但审批策略为 never”时拒绝工具调用，而不是因为禁止弹窗就放行。[C1] [C2] [C6]

**建议：** 安装说明可以给用户一个明确的配置示例，对最终付费工具设 `approval_mode = "prompt"`；不可静默替用户改配置。服务端仍要检查报价、权限和批准条件。普通工具审批只看得到本次参数，所以不能仅传一个看不懂的 `quoteId`，然后指望批准框自动显示金额。[C1] [C6]

```toml
# 配置建议示例；task.regenerate 是本文建议新增的工具
[mcp_servers.beeftv.tools."task.regenerate"]
approval_mode = "prompt"
```

Codex 开源源码另有实验性的 `openai/userVerification`：请求携带 challenge，返回 `credentialId` 与签名；类型注明由验证方验证并消费。**这说明“可验证批准”可以通过额外协议实现，但它不是普通 MCP form 的性质，也没有证据说明任意 BeefTV 客户端已具备注册、验签和设备信任。暂不作为通用接入前提。**[C4] [C8]

### 4.2 Claude Code 如何配合

**出处：** 对最终付费工具可声明下面的私有扩展；官方说明其目的就是每次让人看到审批，且不提供“不再询问”。但普通 form elicitation 的 hook 可以直接合成 `accept`，绕过表单；URL hook 返回 `accept` 甚至可以完全不打开浏览器。[A1] [A3]

```json
{
  "name": "task.regenerate",
  "description": "按已预览的费用重新生成；可能再次收费。",
  "_meta": { "anthropic/requiresUserInteraction": true },
  "annotations": { "readOnlyHint": false }
}
```

**建议：** 支持这个扩展，但同时保留标准 elicitation 和后台校验。不能把“服务端收到了 form 的接受结果”记录成独立核实的人工点击。对普通工具权限规则，也不能假设客户端能按任意嵌套费用参数自动匹配出所需金额限制。[A1] [A2] [A3]

## 5. 可借鉴的开源与付费服务做法

| 案例 | 出处明确说明 / 源码行为 | 可借鉴之处与限制（推断） |
| --- | --- | --- |
| AWS Labs AWS API MCP | `REQUIRE_MUTATION_CONSENT=true` 时要求非只读操作同意；`request_consent` 调 `ctx.elicit`，同时检查接受结果和布尔回答，拒绝则不执行；不支持 elicitation 时失败。[W1] [W2] | 用服务端发起的询问减少漏问；没有客户端能力就不能默默跳过。它没有在该询问中提供费用报价，不能直接当作计费预览范本。 |
| AWS 的现行迁移方向 | 上述开源服务器 README 已标被官方 AWS MCP 替代；迁移说明把 mutation consent 的控制转向 IAM 权限。[W1] [W3] | 不把旧开源实现写成 AWS 全产品现行标准。IAM 限制可调用的操作，不能替代逐笔价格确认。 |
| Stripe MCP | OAuth 或 agent API key 控制访问和范围；文档要求用户开启客户端工具审批，检查调用后再执行。[S1] | 官方付费领域服务也区分账户授权与客户端审批。不能据此声称每个 Stripe MCP 写操作都有服务器强制人工弹窗。 |
| Stripe shared payment tokens | 给指定 seller 的令牌带币种、最高金额、到期时间；文档示例使用 preview API。[S3] | 借鉴“批准对象和金额被凭据约束”，而不是照搬支付基础设施。已有额度生成并不因此需要接入 Stripe。 |
| Stripe API 重放 | 使用 idempotency key 安全重试；v1 保存首次开始执行后的状态码与响应，包括 500；v2 的处理规则有区别。[S2] | 用户同意和网络重试是两个独立问题。不能因响应丢失或 500 就换键再提交，也不能照搬 v1/v2 行为为所有上游作保证。 |
| Terraform CLI | 默认先展示计划再询问；`-auto-approve` 跳过询问，传已保存计划也视作批准；`-input=false` 无法询问时保守失败。[T1] | CLI 应区分交互和自动化，绑定用户看过的计划。但保存的计划不是密码学上的“人读过证明”，费用也不是 Terraform apply 的保证。 |

这些案例足以回答本题：**客户端负责交互，服务端负责权限和执行约束；需要更强的消费范围保证时，用受限凭据或独立认证。** 它们没有提供一种标准化字段，让任意 MCP 服务端识别所有客户端的“人确实同意”。这是案例归纳，不是规范新增要求。[W2] [S1] [S3] [M2]

## 6. BeefTV 应如何暴露“这次会再扣钱”

以下全部为建议。Basis：用户要求在调用方界面确认；沿用 BeefTV 已有预览、操作层和调用者身份；借鉴 MCP elicitation、AWS consent 与 Stripe 受限令牌，不引入另一套任务执行系统。[B1] [B2] [B6] [M1] [W2] [S3]

### 6.1 后台给出可确认的费用预览

在已有 `canvas.generation.propose` 或共用的预览服务中增加再生成场景，输出一份冻结的报价。已有 `proposalId` 可继续表示制作方案；报价可用 `quoteId` 表示，**两者都不能单凭“拿到了 ID”视作用户已同意**。是否合并 ID 应在实施时根据现有存储结构决定。[B2] [B5]

报价至少包含：

- 原任务、画布、全部目标节点、生成数量、模型、渠道及输入摘要；后台保存完整输入指纹。
- 本次动作是取回原结果还是新提交上游；`willSubmitUpstream`、`mayChargeAgain` 由后台计算，不能由模型自填。
- 费用单位、预计值、可执行的上限、价格来源/版本和是否未知。积分、人民币、供应商美元费用分别表示，不能假设兑换比例。
- 原任务是否可能已经扣费，以及旧结果仍可能存在的说明；不能把“本次要收费”写成“已确定原任务扣过费”。
- 报价有效期、画布/模型配置版本、原任务的相关版本、稳定 `operationId` 和当前调用者。

上述字段是建议的业务数据，不是 MCP 标准。示例特意使用费用未知，避免把当前不存在的报价能力写成已有功能：

```json
{
  "reason": "paid_confirmation_required",
  "quoteId": "q_example",
  "operationId": "op_example",
  "action": "regenerate",
  "sourceTaskId": "task_example",
  "nodeIds": ["node_example"],
  "willSubmitUpstream": true,
  "mayChargeAgain": true,
  "charge": {
    "status": "unknown",
    "unit": "provider_currency",
    "estimated": null,
    "maximum": null,
    "limitEnforcement": "none",
    "pricingRevision": null
  },
  "expectedRevision": 42,
  "expectedModelConfigRevision": 7,
  "expiresAt": "2026-10-26T12:30:00Z",
  "message": "将新建一次生成。原任务可能已收费；本次费用暂时未知。是否继续？"
}
```

**费用不确定时的建议：** 显示“未知”及计费依据，不能显示 0；仅有估算不应叫“最高扣费”。后台只能承诺自己或上游确实能执行的上限。若无法限制上游金额，须明确没有硬上限并取得相应同意，不能靠一个 `maxCost` 字段制造保证。金额上升、计费单位改变或输入变化后重新预览和确认。[S3] [B1]

### 6.2 工具分工：预览、免费取回、付费执行

| 工具面 | 建议行为 | 注解/审批 |
| --- | --- | --- |
| `task.get` / 费用预览 | 返回事实、费用和报价；不发生成请求 | 真正纯读才能 `readOnlyHint=true`。签发报价或记录提议若产生用户可见状态，也应按实际副作用标记。[M2] |
| `task.recover`（建议名称） | 只取回原结果，绝不转成新生成 | 免费不等于只读；重放安全需实际保证。[M2] [B5] |
| `canvas.generation.submit` / `task.regenerate`（后者为建议名称） | 最终付费工具携带报价与稳定操作 ID；共用后台的接纳检查 | `readOnlyHint=false`；Claude Code 可加 `requiresUserInteraction`；是否 `idempotentHint=true` 以重放合同为准。[A1] [M2] |
| 直接 business 工具 / 原始 HTTP | 经过同一费用和授权检查 | 不能因为没有走新 MCP 工具就免确认。只读/费用标记不得只按 HTTP 方法推断。[B7] [M2] |

工具说明应直接说“新建生成，按所选渠道收费；原任务可能已经收费”。每次的金额放在报价、elicitation 信息和执行参数里，不能不断修改静态 `tools/list` 来冒充逐笔报价。这是基于工具发现与调用分离作出的设计建议。[M4] [B7]

### 6.3 调用方自己的界面如何问

**推荐路径：** 调用方取得预览；执行付费工具时，MCP 适配层用服务器冻结的报价组成 form 信息。金额和风险是展示文字，回答只用简单布尔/枚举。没有收到有效同意前，不进入上游提交。拒绝、取消、超时、断线均不解释成同意。[M1] [W2] [C3]

下例是 `2025-11-25` 的 `elicitation/create` 参数示例；`2026-07-28` 要放入 `inputRequests`，不能照抄成反向请求。数字均为虚构展示：

```json
{
  "mode": "form",
  "message": "将为“镜头 3”新建 1 个视频任务。本次预计 12 积分，后台可限制最高 15 积分。原任务可能已收费。是否仍然重新生成？",
  "requestedSchema": {
    "type": "object",
    "properties": {
      "confirmed": {
        "type": "boolean",
        "title": "同意这次重新生成及上述费用"
      }
    },
    "required": ["confirmed"]
  }
}
```

只有 `action=accept` **且** `content.confirmed=true` 才视为该客户端返回批准；不预选同意。正文由后台依据报价生成，不能采用模型临时写的更低价格。普通工具审批若已经完整呈现同一份费用，可信集成可把它当作本次调用方审批，避免重复问两遍；但服务器无法仅凭一次 `tools/call` 知道宿主问了什么。[W2] [M1] [A3]

**集成取舍：** form elicitation 与 `requiresUserInteraction` 的普通工具审批是两个不同入口，同时开启可能问两次。支持完整费用审批的 Claude Code 集成可选择“预览 + 标记为必须人工批准的最终工具”；其余支持 form 的客户端使用标准 elicitation。采用哪种方式都要标明调用方承担的责任，不能由服务端擅自关闭用户的宿主审批设置，也不能宣称必定只有一个弹窗。[A1] [C1]

**没有 elicitation 的备用路径：** 返回结构化的 `paid_confirmation_required` 与可读文字，明确本次没有提交上游。调用方用自己的对话/审批 UI 询问，再带同一报价、操作 ID 和确认声明执行。执行参数可包含服务器核对过的费用摘要，让宿主的审批框有机会展示；摘要不能覆盖真实报价。若客户端无法取得批准且没有用户预先授予的相应消费权限，就停止，不自动改成“已确认”。[M4] [T1] [B1]

MCP 结果建议同时返回 `structuredContent` 与等价文本，兼顾机器读取和只展示文本的客户端；业务错误用可识别 `reason`，不靠匹配提示语。HTTP/CLI 可沿用现有 precondition 错误分类。`paid_confirmation_required` 是建议的 BeefTV 业务原因，不冒充标准 JSON-RPC 错误；`2025-11-25` 的 `-32042` 专指 URL elicitation required，也不是所有确认错误通用码。[M4] [M5] [B3]

### 6.4 CLI：交互式确认和自动化确认分开

建议新增以下便利命令；它们目前不存在，现有 `ops call` / `business call` 仍须被后台检查。[B3] [B4]

```sh
# 只预览，给外部助手读取
beeftv task regenerate task_example --preview --json

# 人直接用终端：展示同一报价，再在 TTY 中确认
beeftv task regenerate task_example --quote q_example --op-id op_example

# 外部助手已在自己的界面取得本次同意
beeftv task regenerate task_example --quote q_example --op-id op_example --confirm --json
```

- 人用终端时展示模型、数量、费用/未知、原任务可能已收费，再明确询问；不把回车默认成同意。
- 非交互环境默认返回需确认的 JSON 和非零退出码，不等待无法操作的 stdin，也不自动接受。可沿用当前 precondition 退出码 `5`；最终编码以 CLI 合同为准。[B3]
- `--confirm` 表示**调用方声明已经取得本次同意**，不是 BeefTV 验证了终端前真人。不要用一个全局 `--yes` 解锁全部未来消费。预授权自动化若以后支持，应有用户授予的范围、额度和撤销方式；本报告不将它当作已批准功能。[T1] [S3]
- 网络失败后保留同一 `operationId`、报价和输入，先查原接纳结果；不能因为要再次询问就自动换成另一次购买。[S2] [B1]

### 6.5 后台到底信任什么

| 方式 | 后台可以验证 | 后台不能仅凭它验证 |
| --- | --- | --- |
| 登记的外部客户端 + 声明确认 / form 接受 | 客户端身份、它是否有付费权限、报价内容、输入版本、本次调用是否重复 | 真正弹过 UI、用户读过金额、回答未被 hook 合成。[B6] [A3] |
| BeefTV 控制的独立认证页面 | 已认证用户对这份报价的确认记录 | 用户没有误点；调用者已掌握同等人类会话凭据时的彻底隔离。[M1] |
| 可信签名凭据 | 在已完成可信注册、验签和防重前提下，特定凭据批准了绑定的 challenge | 任意一个字符串或未经注册的公钥代表合法用户；普通 form 自动变成这种保证。[C4] |

**本次推荐的是第一行，符合现有外部审批分工。** 由用户控制是否给某个客户端付费能力；后台依据登记身份判断可否接受其声明，记录为“调用方报告批准”，不标成“服务端独立验证人工确认”。仍不能让只读令牌通过请求体里的 `approvalSource` 或 `confirmed` 获得写权限。[B1] [B6]

如果未来要求“即便调用方恶意，也绝不能擅自消费”，那是更强的产品要求：使用第二/三行或根本不给该客户端消费权。可以由调用方发起 URL 流程，不必强制打开 BeefTV 桌面；URL 本身不能预认证成批准凭据，打开页面还要独立验身份、防跨站伪造，并核对发起与完成用户一致。GET 打开页面、客户端返回 `accept` 都不得记成完成批准。这些是独立认证方案的必要条件，不是本次要求立即新增网页系统。[M1] [C4]

### 6.6 一次批准与一次接纳的最小关系

建议沿用 #17 的事务接纳思路，但确认绑定内容补上：调用者/用户、原任务及相关版本、全部目标、输入指纹、模型/渠道配置、费用单位与有效上限、有效期和 `operationId`。换输入、换报价或涨价后原批准失效。已成功接纳的同操作重放先返回原任务，不因报价后来过期而变成新购买。[B5] [S2] [S3]

单次批准可覆盖用户明确看见数量和总费用的一个批次；部分接纳后的重放不能重做已接纳项。这里仅提出确认接口的需求，具体并发、数据库事务、崩溃恢复和上游防重保证交给并行的防重调研，不在本文宣称已经解决“恰好计费一次”。[B5] [S2]

## 7. 对 #17 和 Chief 改法逐条判断

### 7.1 #17

对照对象为第 2 节标明的文件版本。[B5]

| 原方案条款 | 判断 | 应改成什么及依据 |
| --- | --- | --- |
| 免费 recover 与会新提交上游的入口分开 | **保留** | 外部工具也要一眼看清；免费恢复绝不偷偷转成付费。本文只判断接口意图，不复核全部任务状态设计。[B5] [M4] |
| 只读到状态 token 不代表用户确认 | **保留** | 报价/状态句柄与批准分开；不能把读过报价变成已购买。[M2] [A3] |
| 确认 ID 仅桌面申请；外部确认接口全部 403 | **修改** | 保留桌面权限隔离，但允许有权限的登记客户端走委托审批；用 elicitation 或调用方自身 UI。不能只按入口是否桌面判断批准有效。[B1] [M1] [A1] |
| 外部 `confirm` 任务 409 后只能去桌面 | **删除“只能”** | 409 可作为 HTTP 表达；同时返回报价、风险和支持的确认方式。无能力的客户端可以停下或打开经认证页面，不能所有客户端强制跳桌面。[C2] [A1] [M1] |
| 确认绑定原任务版本、操作 ID、期限，接纳事务内消费 | **保留并补充** | 还要绑定本次输入与费用；客户端身份由服务器判断。令牌不能超出批准对象和金额。[B5] [S3] |
| 同确认同操作重放返回原任务，换操作拒绝 | **保留** | 这是业务接纳合同；是否上游也只计一次不能由本地结果推出。[S2] |
| 内置助手仍需制作方案卡 | **保留** | 与外部审批分开认来源，但进入同一费用和任务检查；不把外部确认接口变成助手自签权限。[B1] [B6] |
| `allowed` 可以照旧执行 | **澄清** | 它表示不触发这次“可能重复扣费”的额外检查，不表示免费，也不意味着可以越过调用方正常付费审批。[B1] [B5] |

### 7.2 Chief 的未验证建议

| 建议 | 判断 | 补充条件 / 依据 |
| --- | --- | --- |
| 后台先返回“需要确认，大概扣多少” | **正确，费用表达需补全** | 分清预计/上限/未知、积分/现金、原任务可能已扣费；后台先不提交上游。返回应可机器识别。[M4] [W2] [S3] |
| 外部在自己的页面问用户 | **正确，可实现** | 支持者用 form elicitation；普通宿主审批要先拿到报价、能看到费用。不能保证任意客户端或任意模式必弹窗。[C1] [C2] [A1] [U2] |
| 同意后带声明重提 | **在委托审批模式下成立** | 身份须有消费权限，声明与报价/操作绑定；审计写调用方声明，不写独立验证真人。更强保证改用独立认证或可信签名。[B6] [A3] [C4] |
| 一次同意只扣一次 | **应改写保证** | “一次批准最多接纳一次约定的生成操作；同操作重放返回原结果”。上游计费恰好一次另需证据，不能由 MCP 或确认 ID 保证。[S2] [B5] |
| 只对当前任务和输入有效 | **正确但还不够** | 再绑定目标批次、模型/渠道、费用单位和约定范围、版本、调用者及期限；变更后重新确认。[B1] [S3] |

可用于替换 #17 对外部调用方限制的建议文字：

> 可能再次产生费用的提交，后台先给出本次生成内容、费用与风险。用户可以在 BeefTV 界面或已获授权的外部调用方界面确认。后台按调用者权限接受相应批准方式，并核对这份批准绑定的任务、输入、费用和操作 ID；重复提交返回原接纳结果。外部调用方的确认声明表示它承担取得用户同意的责任，不是服务端独立验证的人工点击证明。无法取得有效批准时不新建付费生成；免费取回继续按原流程执行。

这段是本文建议，不是已实施合同。Basis：用户原话、现有外部审批设计及本报告规范/客户端证据。[B1] [M1] [A1] [S2]

## 8. 验证范围与后续最小验收

已做：读取本仓库关键调用链及配置；取回 #17 指定分支核对原文；查阅规范、官方文档和固定提交的 Codex / AWS 源码。未做：启动 BeefTV、运行构建/业务测试、改四种客户端配置、发真实生成、验证真实费用或验证四客户端 UI。只新增本文，未修改 #17 文件。

若进入实施，至少用不收费的假生成服务验证以下场景；这是建议的验收范围，不表示本轮测试已通过：

1. 在 Codex / Claude Code 的实际版本上看到本次模型、数量、金额与重复收费风险；接受一次只接纳约定操作，拒绝/取消/断线不提交。分别记录 form、URL 和协议版本，不用“连接成功”替代交互证据。[C2] [A1] [M7]
2. Cursor 和 Claude Desktop 实测各传输与模式的能力；未支持时展示可读报价或明确停止，不静默放行。Desktop 与 Cursor URL 的证据空缺保留到实测后再改。[U3] [D1] [M1]
3. 启用自动批准/elicitation hook 后核对服务端记录仍叫“调用方批准”；Claude Code 私有扩展的每次审批单独验证。独立页面仅打开或回传 `accept` 时不能提交。[A1] [A3] [M1]
4. 报价过期、金额上涨、改输入、换客户端、复用批准到另一操作、响应丢失后重放、批次部分接纳分别验证；直接 `/tasks/:id/retry` 和 CLI 原始 business 入口也不能绕过。[B5] [B7] [S2] [S3]

## 原始来源

外部滚动文档可能继续更新；开源实现按提交固定，不能把调研时源码主干当作用户已安装的发行版本。

- MCP：[当前版本][M0]、[新版 elicitation][M1]、[工具注解][M2]、[访问授权][M3]、[工具合同][M4]、[旧版 elicitation][M5]、[需要输入的结果][M6]、[多轮请求][M7]。
- Codex：[MCP 配置][C1]、[App Server][C2]、[TUI 表单][C3]、[实验性签名验证][C4]、[验证扩展适配][C8]、[URL 界面][C5]、[调用审批源码][C6]、[注解解释][C7]。
- Claude Code：[逐次人工审批和 elicitation][A1]、[权限规则][A2]、[elicitation hooks][A3]、[Anthropic 工具设计说明][A4]。
- Cursor / Desktop：[Cursor MCP][U1]、[Auto-review][U2]、[表单支持更新][U3]、[Claude 连接器权限][D1]。
- AWS：[配置说明][W1]、[确认源码][W2]、[迁移说明][W3]。
- 支付 / CLI：[Stripe MCP][S1]、[Stripe 重放合同][S2]、[受限支付令牌][S3]、[Terraform apply][T1]。
- BeefTV：[生成提交][B1]、[生成预览][B2]、[CLI/MCP 主入口][B3]、[business 适配][B4]、[#17 方案][B5]、[操作身份][B6]、[业务工具目录][B7]、[MCP 配置][B8]、[Claude MCP 配置][B10]、[SDK 版本][B9]。

[M0]: https://modelcontextprotocol.io/specification/2026-07-28 "MCP 当前规范"
[M1]: https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation "MCP elicitation：交互、响应和安全要求"
[M2]: https://modelcontextprotocol.io/specification/2026-07-28/schema#toolannotations "MCP ToolAnnotations 定义"
[M3]: https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization "MCP 传输授权"
[M4]: https://modelcontextprotocol.io/specification/2026-07-28/server/tools "MCP 工具、输入输出和人工审批建议"
[M5]: https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation "MCP 2025-11-25 elicitation"
[M6]: https://modelcontextprotocol.io/specification/2026-07-28/server/tools#input-required-tool-results "MCP 新版需要输入的工具结果"
[M7]: https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/mrtr "MCP 多轮请求及破坏性变化"
[C1]: https://learn.chatgpt.com/docs/extend/mcp?surface=cli "Codex MCP 配置和审批策略"
[C2]: https://learn.chatgpt.com/docs/app-server#mcp-server-elicitation-requests "Codex App Server 的 form 与 URL elicitation"
[C3]: https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/tui/src/bottom_pane/mcp_server_elicitation.rs "Codex TUI 表单、记住批准与降级测试"
[C4]: https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/app-server-protocol/src/protocol/v2/user_verification.rs "Codex 实验性用户验证和签名证明合同"
[C5]: https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/tui/src/bottom_pane/app_link_view.rs "Codex 通用 URL elicitation 实现与测试"
[C6]: https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/mcp_tool_call.rs "Codex MCP 调用策略与审批处理"
[C7]: https://developers.openai.com/plugins/build/mcp-server "OpenAI 对工具注解、私人范围与外部实体的解释"
[C8]: https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/rmcp-client/src/user_verification.rs "Codex 用户验证 elicitation 的模式与结果校验"
[A1]: https://code.claude.com/docs/en/mcp#require-approval-for-a-specific-tool "Claude Code：逐次人工审批、elicitation、客户端运行时"
[A2]: https://code.claude.com/docs/en/permissions "Claude Code 权限模式与 MCP 规则"
[A3]: https://code.claude.com/docs/en/hooks#elicitation "Claude Code elicitation hook 可程序化回答"
[A4]: https://claude.com/resources/articles/building-agents-that-reach-production-systems-with-mcp "Anthropic 关于原生表单、URL 支付和工具设计的说明"
[U1]: https://cursor.com/docs/mcp "Cursor MCP 工具审批"
[U2]: https://cursor.com/changelog/auto-review "Cursor 3.6 Auto-review 适用于 MCP"
[U3]: https://cursor.com/changelog/1-5 "Cursor 1.5 增加表单 elicitation"
[D1]: https://support.claude.com/en/articles/11176164-use-connectors-to-extend-claude-s-capabilities "Claude / Desktop 连接器权限与可用范围"
[W1]: https://github.com/awslabs/mcp/blob/24c39705d1d762cb6ebdd72b23f757a67b7a36c2/src/aws-api-mcp-server/README.md "AWS Labs MCP 配置与被替代说明"
[W2]: https://github.com/awslabs/mcp/blob/24c39705d1d762cb6ebdd72b23f757a67b7a36c2/src/aws-api-mcp-server/awslabs/aws_api_mcp_server/core/aws/service.py#L52 "AWS request_consent 源码"
[W3]: https://github.com/awslabs/mcp/blob/24c39705d1d762cb6ebdd72b23f757a67b7a36c2/src/aws-api-mcp-server/MIGRATION.md "AWS 现行迁移说明"
[S1]: https://docs.stripe.com/mcp "Stripe MCP 授权与客户端审批建议"
[S2]: https://docs.stripe.com/api/idempotent_requests "Stripe API 幂等请求"
[S3]: https://docs.stripe.com/agentic-commerce/concepts/shared-payment-tokens?agent-seller=agent "Stripe 面向代理的受限支付令牌"
[T1]: https://developer.hashicorp.com/terraform/cli/commands/apply "Terraform CLI 计划与确认"
[B1]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/internal/operations/generation_submit.go "BeefTV 外部审批及内置制作授权"
[B2]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/internal/operations/ops.go "BeefTV 生成预览及其返回字段"
[B3]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/cmd/beeftv/main.go "BeefTV CLI 与 MCP 工具注册和错误"
[B4]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/cmd/beeftv/business.go "BeefTV business CLI 与 MCP 适配"
[B5]: https://github.com/glanderness/BeefTV/blob/c064b32902f7ad67395b67380062906b719c294d/docs/architecture/generation-task-state.md "#17 指定分支的生成任务重构方案"
[B6]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/internal/handler/agent_ops.go "BeefTV 操作端点与调用者识别"
[B7]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/internal/handler/business_catalog.go "BeefTV 业务接口工具和收费说明"
[B8]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/plugins/beeftv-skills/mcp.json "BeefTV MCP 启动配置"
[B9]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/backend/go.mod "BeefTV MCP Go SDK 版本"
[B10]: https://github.com/glanderness/BeefTV/blob/542124baadcdbe5ce34ba9029ef7f1d192b49d16/plugins/beeftv-skills/mcp.claude.json "BeefTV Claude MCP 启动配置"
