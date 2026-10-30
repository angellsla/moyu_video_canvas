# 生成任务的状态与结果取回：别人怎么做，对照 #17 重构方案

调研日期：2026-10-26。只看「任务状态怎么表达、结果怎么取回、前端怎么恢复」；付费防重、外部 MCP 调用方确认另有调研，这里只在相关处点到。

对照对象：#17 方案 `docs/architecture/generation-task-state.md`（分支 `tds/conv-01a177a2-1794-7bdc-87f1-a231c6a35b23`，下称「方案」）。

标注约定：
- 【原文】出处文档原话或源码里写的（英文原话保留，必要时附中文意思）。
- 【推断】我据此得出的判断，不是出处说的。

源码版本（本地浅克隆后阅读）：

| 项目 | commit | 提交日期 |
| --- | --- | --- |
| [ComfyUI](https://github.com/comfyanonymous/ComfyUI) | `cdf272f3514353aab04dc2d9fceef57a260dc156` | 2026-10-26 |
| [InvokeAI](https://github.com/invoke-ai/InvokeAI) | `2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf` | 2026-10-25 |
| [stable-diffusion-webui (A1111)](https://github.com/AUTOMATIC1111/stable-diffusion-webui) | `82a973c04367123ae98bd9abdf80d9eda9b910e2` | 2024-07-27 |
| [midjourney-proxy](https://github.com/novicezk/midjourney-proxy) | `0be95a9187da5c9c59442c116b2998376dec3163` | 2025-08-03 |

下文源码链接都指向这些 commit。

## 一、结论

1. **方案的大方向和业界一致，不用推翻。** 所有看过的商用生成 API 都把「创建任务」（POST，花钱）和「按任务号查询 / 取结果」（GET，不花钱、可以无限重复）做成两个接口，没有一家提供「一个 retry 接口内部可能查、也可能重建」的设计。方案把 `/recover` 和 `/retry` 拆开，是在补齐这条通行做法。
2. **「服务端算好下一步，客户端照着做」有成熟先例。** 最贴近的是 Stripe PaymentIntent 的 `status` + `next_action`；ComfyUI 新的 `/api/jobs/{id}` 也是每次读取时从队列和历史里现算状态，不存一个单独的状态字段。方案的 `task.outcome` 是同一种思路。
3. **别人没有「提交未确认」这种状态，是因为用幂等键把它消灭了。** Stripe、ComfyUI 的付费节点、InvokeAI 都是：客户端生成一个键，响应丢了就带同一个键再发一次，服务端认出来就返回原结果。方案第 3.5 节的「超过 2 分钟查不到就判定没接纳」可以换成「带同一个 `clientOperationId` 自动重发」，少一个计时器和一个猜测。
4. **结果能不能取回，取决于上游保留多久，各家差很多**：Replicate 1 小时、OpenAI 后台任务约 10 分钟、火山方舟视频链接 24 小时（任务记录 7 天）、可灵 30 天、OpenAI Batch 输出文件 30 天。方案里视频「24 小时内 1/5/15/60 分钟退避」应改成按渠道的保留期来定，过了保留期直接写「拿不回」。
5. **开源工具在「生成完但前端没收到」这件事上普遍做得比 BeefTV 现状还弱**（ComfyUI 历史只在内存、A1111 只留最近 2 个结果且要手动点「恢复」），只有 InvokeAI 最近一个月做了一套完整的提交回执 + 交付认领。可借鉴的是 InvokeAI 的「提交回执」和「交付认领（claim / confirm / release，2 分钟过期）」，不可借鉴的是 A1111 的手动恢复按钮——正是用户说的「干嘛还要用户点击」。
6. **Temporal 太重，事务外发箱（outbox）BeefTV 已经以另一种形式有了**（发出付费请求前先记 `submission_unknown`）。都不建议引入新组件。

## 二、商用生成 API

### 2.1 状态集合

| 服务 | 状态 | 出处 |
| --- | --- | --- |
| Replicate | `starting` / `processing` / `succeeded` / `failed` / `canceled` / `aborted` | [Prediction lifecycle](https://replicate.com/docs/topics/predictions/lifecycle) |
| fal.ai 队列 | `IN_QUEUE` / `IN_PROGRESS` / `COMPLETED`（成败在结果里，webhook 用 `OK` / `ERROR`） | [Queue](https://fal.ai/docs/model-apis/model-endpoints/queue) |
| OpenAI 后台任务（Responses `background`） | 文中写明非终态为 `queued`、`in_progress`，离开即终态；示例里出现 `completed`、`failed`、`incomplete` | [Background mode](https://developers.openai.com/api/docs/guides/background) |
| OpenAI Batch | `validating` / `failed` / `in_progress` / `finalizing` / `completed` / `expired` / `cancelling` / `cancelled` | [Batch](https://developers.openai.com/api/docs/guides/batch) |
| Runway | `PENDING` / `THROTTLED` / `RUNNING` / `SUCCEEDED` / `FAILED` / `CANCELED` | [API Reference](https://docs.dev.runwayml.com/api/)、[Tiers](https://docs.dev.runwayml.com/usage/tiers/) |
| 可灵 | 旧版 `submitted` / `processing` / `succeed` / `failed`；新版 `succeeded` | [可灵文本生视频（旧版）](https://kling.ai/document-api/api/video/3-0-omni/text-to-video/legacy)、[新版](https://kling.ai/document-api/apiReference/model/textToVideo) |
| 火山方舟视频 | `queued` / `running` / `succeeded` / `failed` / `expired` / `cancelled` | [查询视频生成任务](https://docs.volcengine.com/docs/ark/get-video-generation-task-api?lang=zh) |
| midjourney-proxy（开源代理） | `NOT_START` / `SUBMITTED` / `IN_PROGRESS` / `FAILURE` / `SUCCESS` | [TaskStatus.java](https://github.com/novicezk/midjourney-proxy/blob/0be95a9187da5c9c59442c116b2998376dec3163/src/main/java/com/github/novicezk/midjourney/enums/TaskStatus.java) |

几个值得注意的细节：

- 【原文】Replicate：`aborted` 是 "the prediction exceeded its deadline before it could start running"，并且 "Aborted predictions aren't billed. Canceled predictions are billed for the time they ran."（[lifecycle](https://replicate.com/docs/topics/predictions/lifecycle)）
- 【原文】OpenAI Batch 过期时："unfinished requests within that batch are cancelled"，已完成部分的结果 "made available via the batch's output file"，并按已完成部分计费。（[Batch](https://developers.openai.com/api/docs/guides/batch)）
- 【原文】Runway：THROTTLED 可以按 PENDING 对待（[Go-Live Checklist](https://docs.dev.runwayml.com/guides/go-live/)）。
- 【推断】状态都只有 3–8 个，而且**把「扣没扣钱」放进了终态的区分里**（Replicate 的 aborted / canceled，Batch 的 expired 带部分结果）。方案把「拿不回」拆成 `no_result`（没扣钱）和 `unrecoverable`（可能扣了钱），与此一致。
- 【推断】没有一家对外暴露「提交未确认」状态。这个状态只存在于调用方一侧，各家靠幂等键解决（见 2.3）。

### 2.2 查询和重新创建是不是分开的

【原文】fal.ai 的四个接口（[Queue](https://fal.ai/docs/model-apis/model-endpoints/queue)）：

- 提交：`POST https://queue.fal.run/{app}`
- 状态：`GET .../requests/{request_id}/status`
- 结果：`GET .../requests/{request_id}`
- 取消：`PUT .../requests/{request_id}/cancel`

并写明 request_id 要存下来，"to check status or fetch results later, including from a different process"。

【原文】Replicate：`GET /v1/predictions/{prediction_id}` "Get the current state of a prediction."；同步等待（`Prefer: wait`）超时后返回 `starting` 状态，再用查询接口取（[HTTP API](https://replicate.com/docs/reference/http)）。

【原文】Runway：轮询 `GET /v1/tasks/{id}`，"no more than once every 5 seconds"，加抖动，非 200 时指数退避（[API Reference](https://docs.dev.runwayml.com/api/)）。

【原文】OpenAI 后台任务：用 GET 按 response id 轮询；取消 "Cancelling twice is idempotent"（[Background mode](https://developers.openai.com/api/docs/guides/background)）。

【原文】ComfyUI 内置的付费节点（调可灵、Runway、OpenAI 等）统一走一个轮询函数 `poll_op_raw`：查询请求显式传 `idempotent=False`（不带幂等键，因为本来就是只读），按 `completed_statuses` / `failed_statuses` / `queued_statuses` 三组字符串判断，排队时不消耗轮询次数（[client.py#L341-L560](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_api_nodes/util/client.py#L341-L560)，默认状态表在 [L132-L134](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_api_nodes/util/client.py#L132-L134)）。

【推断】**没有一家提供「retry」接口。** 「重新生成」在所有商用 API 里就是再调一次创建接口，拿到一个新任务号。「取回」就是按旧任务号 GET。两件事在协议层面就分开，不存在「后台自己决定走哪条」。BeefTV 的 `POST /tasks/:id/retry` 一口三用，是自己发明出来的，方案拆开是对的。

### 2.3 响应丢了怎么办：幂等键

【原文】Stripe（[Idempotent requests](https://docs.stripe.com/api/idempotent_requests)）：

- "if a connection error occurs, you can safely repeat the request without risk of creating a second object"
- 同一个键的重试返回第一次保存的响应，"including `500` errors"
- "The idempotency layer compares incoming parameters to those of the original request and errors if they're not the same"
- 键至少保留 24 小时；"the request conflicts with another request that's executing concurrently, we don't save the idempotent result ... You can retry these requests."

【原文】ComfyUI 付费节点：非 GET 请求默认 `idempotency_key=uuid.uuid4().hex`（[client.py#L335](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_api_nodes/util/client.py#L335)）；服务端 409 带 `idempotency_in_flight` 时等待重试（最长 30 秒），带 `idempotency_consumed` / `idempotency_mismatch` 时停止，前者给用户的话是 "The server could not return the result of the previous attempt of this request. Run the node again."（[L105-L110](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_api_nodes/util/client.py#L105-L110)、[L789-L797](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_api_nodes/util/client.py#L789-L797)、[L979-L1030](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_api_nodes/util/client.py#L979-L1030)）。

【原文】fal.ai：runner 返回 503/504/连接错误时，fal 自己重新排队最多 10 次；webhook 要按 `request_id` 去重（[Queue](https://fal.ai/docs/model-apis/model-endpoints/queue)）。

【对照 BeefTV 现状，已核对代码】BeefTV 对 BeefAPI 图片接口已有同一套：`generation/image_recovery.go` 的 `ImageReplayProtocol = "idempotency-key-v1"`，`app/image_submission.go` 给图片 POST 设 `Idempotency-Key`，并读 `Idempotency-Replayed` 响应头和 `image_result_unknown` / `image_result_expired` / `idempotency_conflict` 错误码。任务表 `ClientOperationID` 有 `(user, client_op)` 唯一索引（`model/models_task.go`）。

【推断】ComfyUI 遇到 `idempotency_consumed`（上游记得这次请求、但交不出结果）时，处理就是一句话让用户重新运行，**不做额外确认**。这对应方案里的 `failed + confirm`。BeefTV 要不要比 ComfyUI 更严（加确认），属于付费防重调研的范围，这里不下结论，只记录：看过的项目里没有一家在服务端做「确认后才允许再买一次」。

### 2.4 结果链接过期怎么办

| 服务 | 保留多久 | 出处 |
| --- | --- | --- |
| Replicate（API 创建） | 【原文】"all input parameters, output values, output files, and logs are automatically removed after an hour, by default"；之后 `output` 变 `null` | [Data retention](https://replicate.com/docs/topics/predictions/data-retention)、[HTTP API](https://replicate.com/docs/reference/http) |
| OpenAI 后台任务 | 【原文】"stored to disk for roughly 10 minutes"；只有 `store=true` 才保留更久 | [Background mode](https://developers.openai.com/api/docs/guides/background) |
| OpenAI Batch | 【原文】"The output file will automatically be deleted 30 days after the batch is complete." | [Batch](https://developers.openai.com/api/docs/guides/batch) |
| fal.ai | 【原文】媒体链接 "subject to your media expiration settings"，可按请求头 `X-Fal-Object-Lifecycle-Preference` 设置；"Expired files are permanently deleted and cannot be recovered." | [Queue](https://fal.ai/docs/model-apis/model-endpoints/queue)、[Media expiration](https://fal.ai/docs/model-apis/media-expiration) |
| Runway | 【原文】输出 URL 是临时的，要立即下载到自己的存储，不要直接展示 | [Outputs](https://docs.dev.runwayml.com/assets/outputs/) |
| 火山方舟视频 | 【原文，经搜索摘要转述】`video_url` 有效 24 小时；任务保存 7 天；`execution_expires_after` 默认 48 小时，超时标 `expired` | [查询任务](https://docs.volcengine.com/docs/ark/get-video-generation-task-api?lang=zh)、[创建任务](https://docs.volcengine.com/docs/ark/create-video-generation-task-api?lang=zh) |
| 可灵 | 【原文，经搜索摘要转述】生成的视频 30 天后删除 | [可灵文生视频](https://kling.ai/document-api/apiReference/model/textToVideo) |
| Google AIP-151 | 【原文】"APIs may allow their operation resources to expire ... A good rule of thumb for operation expiry is 30 days." | [AIP-151](https://google.aip.dev/151) |

【推断】
- 各家的共同口径是：**结果链接会过期，调用方要第一时间转存；过期后拿不回，只能重新生成。** 没有一家提供「链接过期后重新签发」的接口（火山方舟任务记录 7 天内可能能重新查到新链接，文档没写，未验证）。
- 所以「拿得回」有一个硬上限，就是渠道的保留期。方案第 3.2 节对视频/超分写的是固定的「24 小时内 1、5、15、60 分钟退避」。对 Replicate 类（1 小时）太长、对可灵（30 天）又太短。应当在每个渠道的协议声明里写保留期，退避按它截止，过期即写 `recovery_outcome = unrecoverable`（上游可能已扣费）。
- 火山方舟 `video_url` 24 小时过期但任务 7 天可查：若重新查询会给新链接，「链接过期」不等于拿不回。这一点要按渠道实测，不能一刀切。

### 2.5 推送（webhook）和轮询

【原文】Replicate：只对终态 webhook 重试，"final retry is sent about 1 minute after the prediction completed"；"Identical webhooks can be sent more than once"；要忽略终态之后到达的 webhook（[Receive webhooks](https://replicate.com/docs/topics/webhooks/receive-webhook)）。Replicate 也写明可以改用轮询或 SSE（[Webhooks](https://replicate.com/docs/topics/webhooks)）。

【原文】midjourney-proxy：webhook 推送失败只记日志、不重试（[NotifyServiceImpl.java#L37-L74](https://github.com/novicezk/midjourney-proxy/blob/0be95a9187da5c9c59442c116b2998376dec3163/src/main/java/com/github/novicezk/midjourney/service/NotifyServiceImpl.java#L44-L74)）。

【推断】webhook 只是加速，最终靠轮询兜底，这是各家文档的共同前提。BeefTV 桌面版没有公网地址，收不到上游 webhook，后台定时轮询是唯一可行的做法，方案沿用 5 秒取回循环是对的。

## 三、开源工具怎么处理「生成完但前端没收到」「刷新后恢复」

### 3.1 ComfyUI

- 【原文】队列和历史都在内存：`PromptQueue` 里 `self.queue`、`self.currently_running`、`self.history = {}`，历史上限 `MAXIMUM_HISTORY_SIZE = 10000`，任务完成时 `task_done` 把结果写进 `history[prompt_id]`（[execution.py#L1304-L1362](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/execution.py#L1304-L1362)）。资产文档写明 "In-memory history is transient."（[app/assets/docs/README.md#L260](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/app/assets/docs/README.md#L260)）
- 【原文】客户端可以自带 `prompt_id`（必须是规范 UUID），服务端原样使用（[server.py#L1164-L1240](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/server.py#L1164-L1240)、[jobs.py `validate_job_id`](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_execution/jobs.py#L34-L51)）。
- 【原文】`GET /api/jobs/{job_id}` 的状态是**读取时现算**的：先看历史，再看正在跑，再看排队，都没有就 404（[jobs.py `get_job`](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_execution/jobs.py#L394-L418)）；历史项的 `completed / failed / cancelled` 由保存的 `status_str` 和事件消息推出（[jobs.py#L225-L245](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_execution/jobs.py#L225-L245)）。
- 【原文】取消对终态和未知 id 是空操作，并且「是否正在跑」和「打断」在同一把锁里完成，避免打断到下一个任务（[jobs.py `cancel_job`](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/comfy_execution/jobs.py#L537-L560)、[execution.py `interrupt_if_running`](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/execution.py#L1378-L1395)）。
- 【原文】WebSocket 重连只补发队列状态和当前执行节点（[server.py#L336-L360](https://github.com/comfyanonymous/ComfyUI/blob/cdf272f3514353aab04dc2d9fceef57a260dc156/server.py#L336-L360)）。

【推断】ComfyUI 的恢复办法是「前端重连后按 prompt_id 去读历史」，服务端进程重启后历史就没了。对 BeefTV 有用的是两点：状态在读取时从事实现算（与方案 `Outcome` 纯函数同一路子）；取消/查询对终态幂等。它不处理「上游付费任务交了钱但进程重启」，这块 BeefTV 已经比它强。

### 3.2 InvokeAI（最有参考价值）

InvokeAI 在 2026-10 合入了一整套「提交回执 + 交付认领」，解决的问题和 BeefTV 几乎一样，只是它的计算在本地、不涉及付费。

- 【原文】队列项状态 `pending / in_progress / waiting / completed / failed / canceled`（[session_queue_common.py#L216](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/session_queue/session_queue_common.py#L216)）。
- 【原文】**提交回执**：批量提交带 `idempotency_key`；同一个键换了内容就报 `EnqueueIdempotencyConflictError`（"Idempotency key ... is already used by another submission"，[session_queue_default.py#L179-L183](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/session_queue/session_queue_default.py#L179-L183)）；另有 `GET enqueue_batch/receipt?idempotency_key=` 按键查回执、`POST enqueue_batch/acknowledge` 确认已收到（[routers/session_queue.py#L252-L326](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/api/routers/session_queue.py#L252-L326)）；已确认的回执保留 7 天（`ACKNOWLEDGED_ENQUEUE_RECEIPT_RETENTION = timedelta(days=7)`，[L60-L63](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/session_queue/session_queue_default.py#L60-L63)）。
- 【原文】前端恢复：刷新后对每个本地记着的提交，先按幂等键查回执拿到后端队列项 id，再逐个读队列项，读到 404 当作已删除（[coordinator.ts#L1043-L1090](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/frontend/webv2/src/features/queue/runtime/coordinator.ts#L1043-L1090)）。文档原话："Journal writes precede submission; backend receipts make retries safe after lost responses. Accepted IDs must be durable before their receipt can expire."（[workbench/projects/README.md#L47](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/frontend/webv2/src/workbench/projects/README.md#L47)）
- 【原文】**交付认领**：结果写回画布由哪个浏览器标签来做，服务端按队列项记 `delivery_claimant / delivery_claimed_at / delivered_at`；动作是 claim → 写画布 → confirm，或 release；"a claim neither confirmed nor released lapses after two minutes"（`QUEUE_ITEM_DELIVERY_CLAIM_TTL = timedelta(seconds=120)`）（[迁移文件](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/shared/sqlite_migrator/migrations/migration_2026_10_13_add_session_queue_delivery.py)、[session_queue_default.py#L73-L101、L759-L787](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/session_queue/session_queue_default.py#L759-L787)、[queue/README.md#L9](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/frontend/webv2/src/features/queue/README.md#L9)）。
- 【原文】**重试**：只对 `failed` / `canceled` 的根项生效，克隆成新 id，记 `retried_from_item_id` 指回原项（[session_queue_default.py `retry_items_by_id`#L1140-L1200](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/session_queue/session_queue_default.py#L1140-L1200)）。
- 【原文】**启动清理**：进程启动时把所有 `in_progress` / `waiting` 改成 `canceled`，理由是 "the invoker may have been killed while processing a queue item"（[L134-L140](https://github.com/invoke-ai/InvokeAI/blob/2c1c5ae833d7ffe5e60284b0ce55379fb86aabcf/invokeai/app/services/session_queue/session_queue_default.py#L134-L140)）。

【推断】
- InvokeAI 的「按幂等键查回执」就是 BeefTV 的 `GET /tasks/by-operation/:operationId`。它的回执和队列项在同一个本地 SQLite 事务里写，所以「查不到回执」是确定的答案：后端没接纳。BeefTV 桌面版同样是本机进程 + 事务接纳，**同样可以把「查不到」当确定答案**，前提是请求已经不在路上。InvokeAI 处理「还在路上」的办法是重发同一个键（服务端去重），而不是等一个固定时长。
- InvokeAI 需要「交付认领」，是因为它由**浏览器**把结果写进画布，多个标签会抢。BeefTV 方案让后台 `recoverPendingCanvasBindings` 写画布，前端只订阅，从根上避开了这个问题，比 InvokeAI 简单。认领机制不需要搬过来；但它的「2 分钟租约 + 确认 / 释放」和 BeefTV 已有的 `ClaimFailedTaskProviderRecovery` 租约是同一个思路，可以作为租约时长的参照。
- InvokeAI 启动时把执行中的任务一律判成取消，是因为它的计算在本进程里，进程死了任务就真没了。BeefTV 的上游任务在别人的服务器上，进程重启后任务可能还在跑、甚至已经出结果。**不能照搬**，BeefTV 应当继续让取回循环去查上游（方案已是如此）。

### 3.3 Automatic1111 / Forge

- 【原文】服务端进度和结果都在内存：`recorded_results_limit = 2`，只留最近 2 个结果（[progress.py#L16-L20](https://github.com/AUTOMATIC1111/stable-diffusion-webui/blob/82a973c04367123ae98bd9abdf80d9eda9b910e2/modules/progress.py#L16-L20)）；`restore_progress` 等任务结束后从这 2 个里找，找不到就返回 "Couldn't restore progress for {id_task}: results either have been discarded or never were obtained"（[L144-L152](https://github.com/AUTOMATIC1111/stable-diffusion-webui/blob/82a973c04367123ae98bd9abdf80d9eda9b910e2/modules/progress.py#L144-L152)）。
- 【原文】前端提交时把任务号存进 `localStorage`（`localSet("txt2img_task_id", id)`），刷新后显示一个「恢复进度」按钮，用户点了才去取（[javascript/ui.js#L145-L225](https://github.com/AUTOMATIC1111/stable-diffusion-webui/blob/82a973c04367123ae98bd9abdf80d9eda9b910e2/javascript/ui.js#L145-L225)）。

【推断】这是反面例子：前端自己记任务号、靠用户手动点恢复、服务端只留极少结果。BeefTV 现在的「继续取回」按钮和前端恢复扫描与它是同一类做法，方案改成后台自动取回是往对的方向走。Forge 是 A1111 的分支，这部分没有单独看源码。

### 3.4 midjourney-proxy

- 【原文】每 30 秒扫一次运行中的任务，超过账号配置的超时分钟数就 `task.fail("任务超时")` 并移出队列（[TaskTimeoutSchedule.java](https://github.com/novicezk/midjourney-proxy/blob/0be95a9187da5c9c59442c116b2998376dec3163/src/main/java/com/github/novicezk/midjourney/support/TaskTimeoutSchedule.java)）。

【推断】超时直接判失败，之后 Discord 那边就算出了图也不会再关联回来。BeefTV 早期「自动重发窗口 23 小时、过后永久卡住」的问题与此同类。教训是：**超时只能决定「不再等」，不能决定「结果不存在」**，后者要有上游的事实（明确失败、或过了上游保留期）。方案区分「暂时失败不写结论」和「持久化事实才判 failed」，与此一致。

## 四、「服务端驱动状态」与长任务编排

### 4.1 服务端给出下一步

- 【原文】Stripe PaymentIntent：`status` 只有 7 个值（`requires_payment_method / requires_confirmation / requires_action / processing / requires_capture / canceled / succeeded`）；`next_action` "If present, this property tells you what actions you need to take in order for your customer to fulfill a payment"（[PaymentIntent object](https://docs.stripe.com/api/payment_intents/object)、[Lifecycle](https://docs.stripe.com/payments/paymentintents/lifecycle)）。支付失败时状态回到 `requires_payment_method` "so that the payment can be retried"——「能不能再试」直接写在状态里，客户端不用猜。
- 【原文】Kubernetes 控制器："Level driven, not edge driven"，你 "can't count on having seen it turn from `false` to `true`, only that you now observe it being `true`"；控制器把结果写回主资源的 `Status`（[controllers.md](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-api-machinery/controllers.md)）。
- 【原文】Google AIP-151：长任务统一成 `Operation`，带 `done`、`error`、`response`、`metadata`；"Individual APIs must not define their own interfaces for long-running operations."（[AIP-151](https://google.aip.dev/151)）

【推断】
- 方案的 `Outcome{phase, retrieve, regenerate, reason}` 对应 Stripe 的 `status + next_action`：`phase` 是「现在怎样」，`regenerate` / `retrieve` 是「下一步能做什么」。这个形状有成熟先例。
- K8s 的「按当前水位、不按边沿」正好是 BeefTV 取回循环该遵守的规则：每轮循环只看任务当前的持久化事实，不依赖「上一次请求的响应」「前端有没有收到某个事件」。方案第 5.1 节第 6 条（响应丢失不影响判定）就是这条。
- AIP-151 的「各接口不要各自定义长任务接口」对应方案第 3.6 节「图片、视频、超分、深度共用一套」。

### 4.2 Temporal 和事务外发箱

- 【原文】Temporal 的持久执行靠 Event History 记录每一步，故障后 "resume from the last recorded event"；部署需要 "the Temporal Server, written in Go, and a database"，外加自己运维的 Worker 进程（[Temporal](https://docs.temporal.io/temporal)）。
- 【原文】事务外发箱解决的是 "How to atomically update the database and send messages to a message broker?"：消息和业务数据在同一个事务里写进 outbox 表，另由中继发送；缺点是中继可能重复发送，"a message consumer must be idempotent"（[microservices.io](https://microservices.io/patterns/data/transactional-outbox.html)）。

【推断】
- **Temporal 不值得。** BeefTV 主力是桌面单机 + SQLite，引入 Temporal 等于多打包一个服务端和数据库，与「本体轻量」的取舍相反。它的核心价值（每一步落库、崩溃后从最后一步接着跑、外部调用要幂等）BeefTV 已经用「任务表 + 路由尝试记录 + 回执 + 5 秒循环」实现了大半。
- **外发箱 BeefTV 已有对应物。** 发出付费请求前，`modelcatalog/route_attempt.go` 先把派发状态写成 `submission_unknown`（已核对代码 L123），这就是「先记下要做的事，再去做」。外发箱本身解决不了「HTTP 请求发出去、响应丢了」，这一段只能靠上游幂等键或按回执查询，BeefTV 已经两样都有。不需要新建 outbox 表。
- 可借鉴的是外发箱的那句约束：**中继会重复，消费端必须幂等**。对应到方案：后台取回循环和用户手动 `/recover` 可能同时触发，写回画布、写 `recovery_outcome` 都要能重复执行而结果不变（方案用租约，已覆盖）。

## 五、逐条对照 #17 方案

### 5.1 和别人一致，保留

| 方案条目 | 对应先例 |
| --- | --- |
| 第 3.1 节：后台算 `outcome`，前端只照着显示 | Stripe `status + next_action`；ComfyUI `/api/jobs/{id}` 读取时现算；K8s `Status` |
| `Outcome` 是对持久化事实的纯函数，不另存状态机 | ComfyUI `get_job` / `normalize_history_item` 都是纯函数；K8s level-driven |
| 第 3.2 / 3.3 节：免费取回 `/recover` 与扣费 `/retry` 分成两个接口，`/recover` 结构上不能创建上游任务 | 所有商用 API 的 GET 查询 vs POST 创建；ComfyUI 轮询显式 `idempotent=False` |
| 免费取回放到后台循环自动做 | Replicate / Runway / ComfyUI 付费节点都靠轮询兜底；InvokeAI 前端定时 reconcile；与 A1111 手动按钮相反 |
| 暂时失败不写结论，只有持久化事实才判 `failed` | midjourney-proxy 的反面教训；K8s 只看观察到的事实 |
| 「拿不回」拆成 `no_result`（没扣钱）和 `unrecoverable`（可能扣了钱） | Replicate `aborted`（不计费）vs `canceled`（计费）；OpenAI Batch `expired` 按已完成部分计费 |
| 每次尝试用租约 `ClaimFailedTaskProviderRecovery` | InvokeAI 交付认领 120 秒租约 |
| 重新生成复用同一个 `clientOperationId`，后台 `admitExisting` 去重 | Stripe 幂等键；InvokeAI `idempotency_key` + 内容不同报冲突 |
| 后台写画布，前端只订阅 | 比 InvokeAI（浏览器写画布、需要跨标签认领）更简单，保留 |
| 四类任务共用一套 `Outcome`，只是「去哪找结果」不同 | AIP-151 统一 Operation；ComfyUI 付费节点统一 `poll_op` |
| 重试生成新任务号、指回原任务（`retryOf`） | InvokeAI `retried_from_item_id`；商用 API 都是新任务号 |

### 5.2 建议改

1. **视频/超分自动查上游的截止时间按渠道保留期定，不用固定 24 小时。**（第 3.2 节、第七节第 3 条）
   依据：第 2.4 节表格，保留期从 10 分钟到 30 天不等。做法【推断】：在每个渠道的协议声明（`internal/protocol/docs/*`）里加「结果保留期」，退避在保留期内一直进行，过期写 `unrecoverable`。火山方舟这类「链接 24 小时、任务 7 天」的，要先实测过期后重新查询能否拿到新链接，再定按哪个算。

2. **「提交未确认」不用 2 分钟计时判定，改成带同一个 `clientOperationId` 自动重发。**（第 3.5 节）
   依据：Stripe 允许带同一个键安全重发、并发时返回可重试错误；ComfyUI 遇 `idempotency_in_flight` 等待重试；InvokeAI 用回执 + 同键重发。BeefTV 任务表已有 `(user, client_op)` 唯一索引和 `admitExisting`。
   做法【推断】：前端处于 `submitting / unknown` 时，自动用**原请求体和原 `clientOperationId`** 重发 `POST /tasks`（不是新建一次生成）。后台已接纳就返回原任务；还在接纳中就返回「处理中、稍后再试」；没接纳就这次接纳——上一次没接纳也就没扣钱，这次接纳是用户原本那一次点击，不是重复购买。这样 `generationSubmissionState: unknown` 不再需要人看，方案第 5.3 节列的「2 分钟误判」风险也没了。前提是请求体能完整重放（Stripe 也要求参数一致），需要确认前端本地保存了原请求；保存不了时再退回方案的做法。

3. **`retrieve: auto | manual` 可以去掉，「继续取回」按钮只在极少数情况出现。**（第 3.1 节表格）
   依据：看过的商用 API 和开源工具都没有「需要人工继续取回」这个状态，结果要么还能 GET 到，要么过期没了。BeefTV 现在会有 `manual`，是因为方案里退避有固定上限。按第 1 条改成「保留期内一直自动取」之后，`retrievable` 就只剩自动这一种【推断】。用户 2026-10-26 说过「不能自动检查状态吗 干嘛还要用户点击呢」。
   保留 `POST /tasks/:id/recover` 作为「立即再查一次」的入口（例如本地存储满了、用户清理后想马上取）即可，不需要单独的 `manual` 状态。

4. **启动时不要像 InvokeAI 那样把执行中任务判成取消。** 方案没写启动处理，建议明确写一句：进程重启后，`running` 的上游任务由取回循环按任务号继续查，不改状态【推断，依据 3.2 节】。BeefTV 现在是否已经这么做，本次没有核对代码。

### 5.3 想复杂了，可以删或推迟

1. **`Outcome` 的 `retrieve` 字段**：见 5.2 第 3 条，删掉后 `Outcome` 剩 `phase / regenerate / reason` 三个字段，形状和 Stripe `status + next_action` 一样小。
2. **第 3.5 节的 2 分钟判定和它在第 5.3 节的风险说明**：被 5.2 第 2 条替代。
3. **不引入 Temporal、不新建 outbox 表**：方案本来就没提，这里只是确认不需要（第四节）。
4. **一次性确认 ID（`regenerate-confirmations` 接口、确认记录表、与接纳同事务消耗）**：这部分属于付费防重，归另一个调研。本次能提供的事实只有一条：看过的 ComfyUI、InvokeAI、商用 API 都没有服务端「确认后才允许再买一次」的机制，ComfyUI 遇到「上游交不出结果」就是让用户重新运行。是否需要这么重，建议等付费防重调研的结论再定，不要在状态这一块先做。
5. **第四节迁移第 4 步「只记日志、不拒绝」的观察期**：同样属于付费守门，不属于状态与取回。如果第 4 条推迟，第 4、5 步可以一起推迟，先发第 1、2、3、6 步（状态投影、后台自动取回、`/recover`、前端改读 `taskOutcome`），这几步不依赖确认 ID【推断】。

## 六、没核实的地方

- 火山方舟、可灵的保留期来自搜索结果对官方文档的摘要，没有逐字打开原页面核对；可灵旧版文档页面抓取时只拿到标题。
- Runway 的 `GET /v1/tasks/{id}` 文档正文没抓到，状态集合和轮询建议来自搜索摘要和 Tiers / Go-Live 页面。
- OpenAI 后台任务的完整终态列表文档正文没写全，只有示例代码里出现。
- 5.2 第 2 条「自动同键重发」能不能落地，取决于前端是否在本地留着原请求体，本次没有查 BeefTV 前端代码。
- 5.2 第 4 条 BeefTV 重启后对 `running` 任务的现有处理，本次没有核对。
- 本次没有跑任何代码或测试，所有 BeefTV 侧的说法都来自阅读 main `542124b` 上的源码和 #17 方案文本。
