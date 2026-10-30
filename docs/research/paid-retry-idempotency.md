# 付费接口怎么防重复扣费、怎么做用户确认：业界做法（带出处）与 #17 方案对照

范围：只查「付费、防重复扣费、用户确认」这一块。生成任务的状态与取回、外部 MCP 调用方的确认，另有两个调研在做。
依据：#17 方案（分支 `tds/conv-01a177a2-…` 的 `docs/architecture/generation-task-state.md`），以及 main `542124b` 上的现有代码（`backend/internal/app/service.go`、`image_submission.go`、`provider_submission.go`、`generation/http.go`、`repository/local_runtime.go`、`task/admission.go`、`task/identity.go`、`database/local_schema_ensure.go`）。
写法：每条结论都分开写「出处原话」和「据此推断」。没有跑代码、没有改代码。

## 一、结论

1. **业界没有「确认令牌」这个通用做法。** 防重复扣费靠三样东西叠起来：①请求自带身份（幂等键）②把「记下这个身份」和「产生副作用」放在同一个事务 ③钱那一层的不变量（一次授权只请款一次）。Stripe、AWS、Google、PayPal、Adyen 都是这三样的不同组合。
2. **#17 的方向是对的，最强的两条本来就和业界一致，不能删**：确认与接纳在同一个事务里核对并作废（AWS 原话要求这么干）、后台算好结论只给前端读（等于 Stripe 的 PaymentIntent / Google 的 Operation 对象）。
3. **该改 4 处**：`/retry` 不接受客户端提交标识（重放返回 409，业界要求返回上一次的结果）；上游支持幂等键的渠道不必把「提交未确认」升级成「让用户确认」（同一 key 重发就是安全的）；用 `updatedAt` 当状态版本会误作废确认；3.5 节「2 分钟查不到就判定未接纳」依赖本地超时，业界不做这种推断。
4. **想复杂了 1 处**：独立发凭据的接口（`POST /tasks/:id/regenerate-confirmations`）加三重校验加桌面专用鉴权，可以用「扣费入口检查一次调用方身份 + 确认记录一条唯一约束」替代，少一个接口、少一类 403 测试。

## 二、业界的五条主线

### 2.1 幂等键：请求自带身份，而不是另发凭据

**出处原话（Stripe，`https://docs.stripe.com/api/idempotent_requests`）**

> The API supports idempotency for safely retrying requests without accidentally performing the same operation twice. When creating or updating an object, use an idempotency key. Then, if a connection error occurs, you can safely repeat the request without risk of creating a second object or performing the update twice.
>
> For API v1, Stripe saves the status code and body of the first request made with a given idempotency key after endpoint execution begins. Retries with the same key return the saved response, including 500 errors.
>
> A client generates an idempotency key… we suggest using V4 UUIDs… Idempotency keys are up to 255 characters long.
>
> For API v1, you can remove keys from the system automatically after they're at least 24 hours old. We generate a new request if a key is reused after the original is pruned. The idempotency layer compares incoming parameters to those of the original request and errors if they're not the same to prevent accidental misuse.
>
> For API v1, we save results only after the execution of an endpoint begins. If incoming parameters fail validation, or the request conflicts with another request that's executing concurrently, we don't save the idempotent result because no API endpoint initiates the execution. You can retry these requests.

同键并发冲突的错误码（`https://docs.stripe.com/error-codes/idempotency-key`）原话：

> `idempotency_key_in_use` — The idempotency key provided is currently being used in another request. This occurs if your integration is making duplicate requests simultaneously.

**AWS EC2 `ClientToken`（`https://docs.aws.amazon.com/AWSEC2/latest/APIReference/Run_Instance_Idempotency.html`）**

> A client token is a unique, case-sensitive string of up to 64 ASCII characters… You should not reuse the same client token for other API requests. If you retry a request that completed successfully using the same client token and the same parameters, the retry succeeds without performing any further actions. If you retry a successful request using the same client token, but one or more of the parameters are different… the retry fails with an `IdempotentParameterMismatch` error.

**Google AIP-155（`https://google.aip.dev/155`）**

> The most important purpose for request IDs is to provide idempotency guarantees: allowing the same request to be issued more than once without subsequent calls having any effect.
>
> Providing a request ID **must** guarantee idempotency. If a duplicate request is detected, the server **should** return the response for the previously successful request, because the client most likely did not receive the previous response.
>
> APIs **may** choose any reasonable timeframe for honoring request IDs.

AIP-155 还专门写了「旧的成功响应已经过期怎么办」：

> In this situation, the method **may** return the current state of the resource instead. In other words, it is permissible to substitute the historical success response with a similar response that reflects more current data.

**PayPal（`https://developer.paypal.com/api/rest/reference/idempotency/`）**

> For example, when you include a previously specified `PayPal-Request-Id` header in a request, PayPal returns the latest status of the previous request that used that same header. Conversely, when you omit the `PayPal-Request-Id` header from a request, PayPal duplicates the request.
>
> When you send two simultaneous API requests with same `PayPal-Request-Id` header, PayPal processes the first request and might fail the second request.

**Adyen（`https://docs.adyen.com/development-resources/api-idempotency/`）**

> If the Adyen payments platform already processed the request, the response to the first attempt will be returned without duplication.
>
> Keys are stored at a company account level… Idempotency keys are valid for a period of 7 to 14 days after first submission.

**据此推断**：BeefTV 已经有这一套，而且语义一模一样——

- `metadata.clientOperationId` + `CREATE UNIQUE INDEX idx_tasks_user_client_op ON tasks(user_id, client_operation_id)`（`backend/internal/database/local_schema_ensure.go`），就是这个「调用方生成的请求 ID」；
- `admitExisting`（`backend/internal/task/identity.go`）在同一个键、不同请求体时返回冲突，就是 AWS 的 `IdempotentParameterMismatch` / Stripe 的「参数不一致就报错」；
- 同键同体重放返回原来那个任务，就是 AIP-155 的「返回上一次成功的结果」。

两家都拿它当「safely retrying」的唯一手段，而不是另发凭据。**但 `/retry` 这条路没有客户端提交标识**：它靠 `UPDATE … WHERE status IN (failed, cancelled)`（`repository/local_runtime.go` 的 `retryTask`）做条件更新，第二次点击匹配 0 行、返回 `ErrTaskNotRetryable`。条件更新保证不会建出两份，这点没错；但返回的是错误而不是「上一次的结果」，用户会看到一次失败提示。

### 2.2 记账和副作用必须在同一个事务里

**出处原话（AWS Builders' Library, `https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/`）**

> At Amazon, our preferred approach is to incorporate a unique caller-provided client request identifier into our API contract… A concrete example of this can be seen in the Amazon EC2 `DescribeInstances` response… (In the Amazon EC2 API the unique client request identifier is called the `ClientToken`).
>
> An important consideration is that the process that combines recording the idempotent token and all mutating operations related to servicing the request must meet the properties for an atomic, consistent, isolated, and durable (ACID) operation… This ensures that we avoid situations where we could potentially record the idempotent token and fail to create some resources or, alternatively, create the resources and fail to record the idempotent token.

同一篇还讲了两个容易忽略的点：

> One possible response to this request is to return a `ResourceAlreadyExists` return code. This meets the basic tenets for idempotency because there is no side effect for retrying the call. However, this leads to uncertainty from the perspective of the caller… although there is no side effect from the service perspective, returning `ResourceAlreadyExists` has a side effect from the client's perspective.

> It's impractical to retain that knowledge indefinitely… for EC2 instances, it works to limit the time period to the lifetime of the resource, plus an interval after which it is reasonable to assume that any late arriving requests would either have arrived or would no longer be valid.

> We find that it's safest to assume that the customer intended a different outcome… we return a validation error indicating a parameter mismatch between idempotent requests. To support this deep validation, we also store the parameters used to make the initial request along with the client request identifier.

**出处原话（Brandur Leach, `https://brandur.org/idempotency-keys`）**

> For endpoints that get away with only mutating local state in an ACID database, it's possible to get a robust and simple idempotency implementation by mapping requests to transactions… This approach is far easier and less complicated than what's described here, and I'd suggest that anyone who can get away with it take that path.
>
> Implementations that need to make synchronous changes in foreign state (i.e. outside of a local ACID store) are somewhat more difficult to design… To guarantee idempotency on this type of endpoint we'll need to introduce idempotency keys.
>
> Atomic phases should be safely committed **before** initiating any foreign state mutation. If the call fails, our local state will still have a record of it happening that we can use to retry the operation.
>
> A recovery point is a name of a check point that we get to after having successfully executed any atomic phase or foreign state mutation. Its purpose is to allow a request that's being retried to jump back to the point in the lifecycle just before the last attempt failed.

**据此推断**：BeefTV 对上游调用的正是 Brandur 说的「foreign state mutation」——本地事务提交了也没用，上游已经扣的钱回不来。所以：

- #17 第 3.1 节「确认记录的核对与消耗，与新任务的接纳放在同一个事务」**就是 AWS 那句 ACID 原话的直译，方向完全正确，这是整份方案里最不能省的一条**；
- 反过来，#17 没有把「先记账、再发上游」写成不变量。BeefTV 有 `RouteAttempt.DispatchState`（`not_sent / accepted / submission_unknown / rejected_no_job`），已经是恢复点，但方案里没有要求「发起上游请求之前，带幂等键的 attempt 必须已经提交」。这一条是 2.3 节所有「不确定」判断的地基，漏了它，`recovery_outcome` 的结论就还是会毒。
- 顺带印证 #17 的一个细节：上游幂等键不是新任务才给，而是**绑在 attempt 上**（`provider_submission.go`：`uuid.NewSHA1(taskID + ":" + attemptID)`，注释写明「跨 worker/进程恢复稳定，用户显式重试或被安全拒绝的路由才换新 attempt」）。这正是「一次操作一个身份」的正确画法：同一 attempt 重发是安全的，用户重试换 attempt 就是新的一次花钱。

### 2.3 不确定时的重试规则：可查、可重放，否则不重试

**出处原话（RFC 9110 §9.2.2, `https://www.rfc-editor.org/rfc/rfc9110.html`）** —— 这一句就是「结果不确定时绝不自动重提」的行业表述：

> A client **SHOULD NOT** automatically retry a request with a non-idempotent method unless it has some means to know that the request semantics are actually idempotent, regardless of the method, or some means to detect that the original request was never applied.
>
> A proxy **MUST NOT** automatically retry non-idempotent requests. A client **SHOULD NOT** automatically retry a failed automatic retry.

**出处原话（AWS, `https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/`）**

> The other problem with retries is that some remote calls have side effects. A timeout or failure doesn't necessarily mean that side effects haven't happened.
>
> In general, our view is that APIs with side effects aren't safe to retry unless they provide idempotency… Read-only APIs are typically idempotent, while resource creation APIs may not be.
>
> Some APIs, like the Amazon Elastic Compute Cloud (Amazon EC2) `RunInstances` API, provide explicit token-based mechanisms to provide idempotency and make them safe to retry.

**出处原话（AWS EC2 重试建议表，同 2.1 链接）**：200 → 不重试；400 段（客户端错误）→ 不重试（但补一句「如果请求涉及正在改变状态的资源，重试有可能成功」）；500 段 → 重试并退避。

**据此推断**：RFC 给了两个放行条件，正好对应 #17 的两条设计：

| RFC 的放行条件 | BeefTV 对应事实 |
| --- | --- |
| 「请求语义本身幂等」 | 上游支持幂等键的渠道 + 同一 attempt 同一 key；或本地任务已带 `clientOperationId` |
| 「有办法判断原请求根本没被应用」 | `DispatchState = not_sent` / `rejected_no_job`，或按提交标识查不到任务 |

所以 #17「结果不确定绝不自动重提」不只是保守，是标准允许的**唯一安全侧**；而 #17 没写出来的是：**这两个条件成立时，自动重提是允许并且被推荐的**（Stripe 和 AWS 都这么做）。这一条直接把「可能扣过费」变成「可以免费确认」，并把最贵的一类 `confirm` 消掉——见第四节第 2 条。

### 2.4 确认与扣款分开，但只有一次扣款

**出处原话（Stripe PaymentIntent 生命周期，`https://docs.stripe.com/payments/paymentintents/lifecycle`）**

> After your customer provides payment information, the PaymentIntent enters the `requires_confirmation` status and is ready to confirm. Most integrations skip this state because they submit payment method information when the payment is confirmed.
>
> If the payment requires additional actions, such as authenticating with 3D Secure, the PaymentIntent has a status of `requires_action`.
>
> Processing — Occurs after required actions are handled and the payment uses an asynchronous payment method… If you're separately authorizing and capturing funds, your PaymentIntent can instead move to `requires_capture`.
>
> Canceled — You can cancel a PaymentIntent before it reaches a `processing` or `succeeded` state. Cancellation invalidates the PaymentIntent for future payment attempts, releases any held funds, and can't be undone… PaymentIntents might also automatically transition to `canceled` if they're confirmed too many times.

**出处原话（Stripe 确认接口，`https://docs.stripe.com/api/payment_intents/confirm`）**

> If the selected payment method requires additional authentication steps, the PaymentIntent will transition to the `requires_action` status and suggest additional actions via `next_action`. If payment fails, the PaymentIntent transitions to the `requires_payment_method` status or the `canceled` status if the confirmation limit is reached. If payment succeeds, the PaymentIntent will transition to the `succeeded` status (or `requires_capture`, if `capture_method` is set to `manual`).
>
> After `next_action`s are handled by the client, no additional confirmation is required to complete the payment.
>
> There is a variable upper limit on how many times a PaymentIntent can be confirmed. After this limit is reached, any further calls to this endpoint will transition the PaymentIntent to the `canceled` state.

**出处原话（Stripe 授权与请款分开，`https://docs.stripe.com/payments/place-a-hold-on-a-payment-method`）**

> When you create a payment, you can place a hold on an eligible payment method to reserve funds that you can capture later… This is sometimes referred to as manual capture.
>
> You need to capture the funds before the authorization expires. If the authorization expires before you capture the funds, the funds are released and the payment status changes to `canceled`.
>
> Card payments: The amount is typically on hold for 7 days for online payments…

**出处原话（Stripe 请款，`https://docs.stripe.com/payments/capture-later`）**

> Although some card payments are eligible for multicapture, you can only perform one capture on an authorized payment for most payments. If you partially capture a payment, you can't perform another capture for the difference.

**据此推断**：Stripe 的「确认」和「扣款」分开，不是靠一张凭据表，是靠三个结构性事实：

1. **对象自己持状态**：`requires_confirmation → requires_action → processing → succeeded`。已经 `succeeded` 的对象不能再被 confirm，想再收钱必须新建一个对象——「新的一次花钱 = 新对象」。
2. **确认次数有上限**，超了直接 `canceled`，而不是继续尝试。
3. **一次授权只对应一次捕获**，钱那一层直接不允许重复请款。

对 #17 来说：`Outcome`（后台算下一步）≈ `PaymentIntent`（服务端持有的意图对象），`用户确认` ≈ confirm 动作——这个拆分与业界同构。但**「一次确认只换一次扣费」在 Stripe 里是靠「对象状态机 + 次数上限 + 一次捕获」实现的，不是靠「发放一个一次性令牌」**。这决定了 #17 里那套凭据机制可以更简单（见第四节第 4 条 / 第三节 Q2）。

### 2.5 一次性凭据的标准做法（如果确实要用凭据）

**出处原话（RFC 6749 §4.1.2, `https://www.rfc-editor.org/rfc/rfc6749.html`）**

> The authorization code MUST expire shortly after it is issued to mitigate the risk of leaks. A maximum authorization code lifetime of 10 minutes is RECOMMENDED. The client MUST NOT use the authorization code more than once. If an authorization code is used more than once, the authorization server MUST deny the request and SHOULD revoke (when possible) all tokens previously issued based on that authorization code.

**据此推断**：#17 定的「确认有效期 10 分钟」和标准里一次性凭据的建议寿命一致，不是拍脑袋；「换一个 `clientOperationId` 用同一个确认 ID 会被拒绝」对应 RFC 的「重放必须拒绝」。#17 比 RFC 还多绑了「原任务的 routeRun/updatedAt」，比标准更严——方向没问题，问题在 `updatedAt`（第四节第 3 条）。

### 2.6 钱那一层的不变量，比调用方自觉更可靠

**出处原话（Adyen，同 2.1 链接）**

> The accounting rules in the Adyen payments platform take care of most potential double-processing issues that can impact payment modifications. For example, the following default rules apply:
> Captures: when partial captures are allowed, it is not possible to capture a higher amount than the authorized one.
> Refunds: when multiple refunds are allowed, by default the total refunded value cannot exceed the captured amount.

**据此推断**：这是最省事的一层——不靠调用方少发请求，靠账目本身拒绝不可能的状态。BeefTV 没有本地积分账本（钱直接扣在用户自己配置的上游渠道上，见 `runninghub_management.go` 里「积分 API Key」的用法），所以**做不到「账上不可能扣两次」**，只能做到「不可能提交两次」。这个差别必须写进用户文案和方案的限制说明：#17 第 5.3 节已经承认「只能保证一次确认最多一次付费」，建议把措辞固定成「最多提交一次」而不是「不会重复扣费」。

## 三、直接回答三个待定问题

**Q1 Stripe 怎么防重复扣款？怎么让「确认」和「扣款」分开？**

- 防重复扣款：客户端生成的 `Idempotency-Key`，同键同参返回上一次的响应（含 500），同键异参报错，同键并发返回 `idempotency_key_in_use`，键在 24 小时后可被清理。原话见 2.1。
- 确认与扣款分开：`requires_confirmation`（待确认）→ `requires_action`（等用户完成 3DS，做完不需要再确认）→ `processing` / `requires_capture`（等请款）→ `succeeded`；`capture_method=manual` 时先把钱冻住、7 天内请款，过期自动释放成 `canceled`。原话见 2.4。
- 其他值得抄的：AWS `ClientToken`（≤64 字符、同键异参 `IdempotentParameterMismatch`、SDK 自动生成并在重试时复用）；Adyen（键 7–14 天、按公司账号去重、会计规则兜底）；PayPal（**不带 `PayPal-Request-Id` 就会重复**，等于告诉我们这类头是可选项时默认不安全）；Google AIP-155（`request_id` 必须保证幂等、重复请求返回上次响应、时限自定）。

**Q2 「一次确认只换一次扣费」业界怎么实现？有没有更简单的通行做法？**

业界三种，按复杂度从低到高：

| 做法 | 代表 | 机制 | 适合 BeefTV 吗 |
| --- | --- | --- | --- |
| ① 幂等键（无独立凭据） | Stripe / AWS / Google | 一次用户动作 = 一个 ID；同 ID 重放不产生第二次副作用 | 适合，BeefTV 已有（`clientOperationId`）。但它只防「同一次动作被发两遍」，不防「用户点两次确认」 |
| ② 一次性凭据 | RFC 6749 授权码 | 服务端发码、一次性、重放拒绝并撤销 | 适合「点两次确认」这个场景，但发码接口是额外一套东西 |
| ③ 两段式钱 | Stripe 授权/捕获、Adyen capture | 先冻住额度，再请款一次；未请款自动释放 | BeefTV 没有本地账本，**用不了**（用户的钱在上游渠道账上） |

**「在事务里核对并作废」有没有更简单的通行做法**：有，而且业界更常用的形态是**让「一次确认」变成一条唯一约束**，而不是一张需要发码/消费/过期的表。等价写法：

```sql
-- 一张表就够：#17 的确认记录表，去掉「发 ID」这一步
CREATE TABLE regeneration_authorizations (
  user_id TEXT NOT NULL,
  target_node_id TEXT NOT NULL,
  original_task_id TEXT NOT NULL,
  original_route_run INTEGER NOT NULL,
  authorized_at DATETIME NOT NULL,
  consumed_task_id TEXT,           -- 消费时就填，唯一约束保证只填一次
  UNIQUE (user_id, target_node_id, original_task_id, original_route_run)
);
```

- 用户在确认框点「仍然重新生成」→ 前端在下一次付费提交里带 `confirm: { originalTaskId, originalRouteRun }`；
- 后台在接纳事务里 `INSERT … ON CONFLICT DO NOTHING`；插入成功 = 拿到这一次额度，插入被拒 = 已经用过，返回当前 `outcome`；
- 「同一次确认不会买到两份」由数据库保证，不需要 10 分钟 TTL、不需要把 `clientOperationId` 绑进来、不需要单独一个发凭据接口。

如果要保留 TTL，它只是这张表上的一个 `authorized_at` 判断，**不是**另一套机制。这是我根据上面五条推断出的实现，不是任何一家的原话——但它的每一块（同事务记账、一次性、约束兜底）都在 2.2 / 2.5 / 2.6 里有出处。

**Q3 「结果不确定时，绝不自动重提会扣钱的请求」业界怎么表述、怎么落地？**

- 表述：RFC 9110 §9.2.2 那句 `SHOULD NOT automatically retry… unless it has some means to know that the request semantics are actually idempotent… or some means to detect that the original request was never applied`；AWS 的「at most once commitment」；AWS 的「A timeout or failure doesn't necessarily mean that side effects haven't happened」。
- 落地三步，按顺序：**先查**（Stripe `retrieve` / AWS `Describe*` / BeefTV 的回执重建、查上游）；**能重放就用同一个身份重放**（幂等键，只在确有幂等契约的地方）；**都不行才交给人**，并且明确告诉用户「上一次可能已经扣过费，这次会再扣一次」。
- 反面清单（业界明确说不要做的）：不要靠「HTTP 方法是不是 POST」判断安全性（RFC 那句 `regardless of the method` 就是针对这个）；不要在没有幂等契约时自动重试有副作用的调用；不要自动重试一次已经自动重试过的请求。

## 四、对照 #17：一致 / 该改 / 想复杂了

### 4.1 与业界一致（保留，别因为「想简化」删掉）

| #17 的设计 | 对应业界做法 | 出处 |
| --- | --- | --- |
| 后台算 `outcome`，前端只照着显示和调用（3.1） | PaymentIntent / Operation 这类「服务端持有意图对象，客户端读状态」 | Stripe 生命周期页、AIP-155「返回当前状态」 |
| 免费取回与扣费提交拆成两个接口（3.2 / 3.3） | Stripe `retrieve` 与 `confirm` 分开；AIP-155 只要求写请求带 `request_id` | 同上 |
| 确认的核对与消耗和新任务接纳在同一个事务（3.1 第 2 点、3.3 第 4 点） | 「记录幂等 token 与所有变更必须是 ACID」；「先提交本地记录，再做外部副作用」 | AWS Builders' Library；Brandur |
| 确认绑定原任务的版本（3.1 第 2 点） | AIP-155 的旧响应替换规则；Brandur 的 recovery point | AIP-155；Brandur |
| 确认有效期 10 分钟 | 一次性凭据的建议寿命就是 10 分钟 | RFC 6749 §4.1.2 |
| 客户端提交标识去重 + 同键异参冲突（已有实现） | 幂等键三要素（客户端生成、同键同参重放、异参报错） | Stripe / AWS / AIP-155 三家的原话都能对上 |
| 上游幂等键按 attempt 派生、跨重启稳定（已有实现） | 「一次操作一个身份」，SDK 自动生成并在重试时复用 | AWS Builders' Library |

**已经在做、但方案里没当成资产的一条**：`backend/internal/app/image_submission.go`、`generation/http.go`、`generation/video.go`（BeefAPI 视频）、`generation/protocol.go`（声明式插件可把它映射到 header）都会给上游带 `Idempotency-Key`，键由 `taskID:attemptID` 派生，注释明确「同一 attempt 跨进程恢复稳定，用户显式重试才换新 attempt」。这是全套系统里**最有效的一层防重复扣费**，比任何确认令牌都直接。方案应该把它写成显式契约，而不是留在实现细节里。

### 4.2 该改（4 条，按优先级）

**① `/retry` 不接受客户端提交标识，重放返回 409（高）**

现在点两次「重新生成」：第一次把任务改回 `queued`，第二次 `WHERE status IN (failed, cancelled)` 匹配 0 行 → `ErrTaskNotRetryable` → 前端弹错误。不会多扣钱（这点是对的），但用户看到的是失败。

业界要求的是「返回上一次的结果」（AIP-155 原话：`the server should return the response for the previously successful request, because the client most likely did not receive the previous response`；AWS 更是专门说过只回 `ResourceAlreadyExists` 会让调用方困惑）。

改法：`/retry` 也接受 `clientOperationId`，重放时按「同键同体」返回同一个任务（含它当前的 `outcome`）。这条不改也能上线，但改动很小、收益明确。

**② 有幂等键的渠道，`submission_unknown` 不该升级成「让用户确认」（高）**

这是收益最大的一条。RFC 9110 明确放行「请求语义本身幂等」的自动重试，AWS 把这叫「at most once 承诺」，Stripe 的做法是「`execution begins` 之后同键返回保存的响应」。而 #17 现在的口径是：提交结论未确认 → `failed + confirm`，让用户决定要不要再花一次钱。

对**上游支持幂等键**的渠道（图片、BeefAPI 视频、声明式插件），用同一个 attempt 的同一个 key 重发，上游只会执行一次——**这是免费确认，不需要用户确认**。真正需要 `confirm` 的只剩两类：渠道不支持幂等键（键没有意义）、或回执确实丢了且查不到。

改法：给渠道能力加两个字段（`supports_idempotent_submit` / `supports_query_by_request_id`），`Outcome` 按能力给值：支持幂等键且回执在 → `retrievable`（后台自动重发同一 key，不是新建任务）；不支持 → 才落到 `failed + confirm`。这直接把 #17 里最贵、用户体验最差的一类状态缩到少数渠道上。

**③ 用 `updatedAt` 当状态版本，会误作废用户的确认（中）**

3.1 节把确认记录绑到「原任务的 routeRun 与 `updatedAt`」。但 `updatedAt` 会被**任何**写入改掉：进度推进、`stage` 文案变化、日志、以及方案自己在 3.2 加的取回循环（代码已确认：`backend/internal/repository/repository.go:380`、`:387` 每次写 stage/progress 都显式设 `updated_at: time.Now()`）。用户点开确认框的几秒里，一次无关的进度写入就能让确认失效，用户看到「提交被拒」，而结果其实没取回。

原本要防的是「确认期间结果被取回了」，那是**结论变了**，不是**记录变了**。改法：用显式版本。`routeRun` 已经在重试时 +1，够用；要更精确就加一个 `outcome_version`，只在 `phase` / `regenerate` 真正变化时 +1。这也是 AIP-155 用「当前状态」而不是「时间戳」判断新旧的思路。

**④ 3.5 节「2 分钟查不到即判定未接纳」依赖本地超时（中）**

依据是「2 分钟要大于后端接纳请求的超时」。业界不做这种推断：超时不可信（RFC 9110 的原话就是「超时或失败不代表副作用没发生」），而且 2 分钟是拿一个会随部署变化的值当业务判断，一旦本机后端卡顿超过它，就会把已接纳的任务判成未接纳。

而 3.5 节第 2 点已经给了正确解法：**重新生成时复用同一个 `clientOperationId`，后台 `admitExisting` 去重**。既然有这个，用户永远不需要看到「暂勿重新生成」——再点一次就是：要么返回原任务，要么建新任务。所以阈值可以整段删掉，判定保持不变：查不到就允许再点，靠去重兜底。

### 4.3 想复杂了（可以删/合并）

**① 独立发凭据的接口，可以退回「一次身份检查 + 一条唯一约束」（主要一条）**

3.1 节为了「一次确认只换一次付费提交」，引入了一整套：新接口 `POST /tasks/:id/regenerate-confirmations`、请求体绑 `clientOperationId`、10 分钟 TTL、`routeRun` + `updatedAt` 三重校验、拒绝 `requireTrustedDesktopWritePrincipal` 改装 `trustedDesktopUI`、在 `businessRouteAllowed` 里显式排除、再加一组 403 回归测试。

问题在于**这个接口想证明的东西，业界没有任何 API 试图证明**：它实际上在证明「桌面 UI 真的弹过确认框」。服务端无法验证这件事（可信客户端可以伪造），能验证的只有两件事：**谁有权花这笔钱**（调用方身份），**一个用户动作最多产生一次付费提交**（幂等 + 一次额度）。

所以可以缩成：

1. 扣费入口（`/retry`、带 `retryOf` 的 `POST /tasks`、对着已有节点的 `POST /tasks`）**在一个地方**检查调用方身份，比现在「在每个路径上判断一次」少写很多；
2. 确认不需要发凭据，用 3.2 那张唯一约束表（Q2）在接纳事务里 `INSERT … ON CONFLICT DO NOTHING`；
3. 「外部调用方不能确认 `confirm` 任务」这条政策改为：**外部调用方提交时带 `confirm` 字段一律 409**，一行判断，留在同一个入口里。

净效果：少一个接口、少一张需要过期和清理的表语义、少一类 403 测试，安全性不变（因为安全来自「谁能花钱」和「额度只有一次」，不是来自凭据的形态）。这是我根据 2.1–2.6 推出来的，不是任何一家的原话；如果团队更看重「确认这件事只能发生在一个专门的接口上」这个可读性，保留现设计也能work，只是要接受多出来的测试面。

**② `regenerate = blocked` 里的「审核未通过只拦原样重跑」需要算内容指纹，可以简化**

3.1 节为了这一条要新写一个「只覆盖提示词、参考素材、模型和参数」的内容指纹，还要和现有的 `ClientOperationHash` 分开（后者会把 `clientOperationId` 一起哈希）。业界没有这么细的做法——审核未通过这类终态，上游根本没扣钱，直接 `allowed`、让上游再判一次就够，不需要「原样 / 改过」的区分。省一个指纹函数和一组测试。这条是我的判断，没有出处，列出供评审取舍。

**③ 3.5 节的 `taskClientOperationId` + `generationSubmissionState: submitting | unknown` 两个字段**

如果采纳 4.2 第 ④ 条（永远允许再点、靠去重兜底），前端就不再需要区分 `submitting` 和 `unknown` 给用户看——两种情况下用户能做的事一样（再点一次）。这两个字段可以只留一个（`taskClientOperationId`），`unknown` 这个展示状态可以取消。

## 五、如果只能改一处

选 4.2 第 ② 条：**给渠道标注是否支持幂等提交，支持的就用同一个 key 重发，不要求用户确认**。

理由：它是唯一一条把「可能已扣费 → 让用户再付一次」变成「可能已扣费 → 免费确认」的改动，而这类状态是今天四轮检查里最常出问题、用户体验最差的一类；同时它有最硬的出处（RFC 9110 的两个放行条件、AWS 的 at most once 承诺、Stripe 的「执行开始后同键返回保存的响应」），并且 BeefTV 已经在给三条路径带上游幂等键，只是没把这层能力用起来。

## 六、没查到 / 不确定

1. 没找到可以直接照搬的开源项目。Infinite Canvas 没有任务中心、异步任务和恢复层（#15 根因说明里的对照），付款与确认这一段也没有可搬的实现；能搬的是上面这些**模式**（幂等键、同事务记账、授权/捕获、一次性凭据、idempotent receiver）。
2. Temporal 的 `WorkflowId` 去重（同一个 workflow id 只允许一个运行实例）我认为是这个问题的另一个好参照，但文档页抓取失败，没有拿到原文，**所以不列为出处**，只作为线索留给评审。
3. 「上游渠道是否真的会按 `Idempotency-Key` 去重」没有逐个验证。代码里确认了三处会带这个头（图片提交、BeefAPI 视频、声明式插件可映射），但渠道侧行为需要逐家核实——4.2 第 ② 条依赖这个核实结果，不能直接假定。
4. 「Stripe 的键 24 小时后清理」是文档原话的字面表述（`you can remove keys from the system automatically after they're at least 24 hours old`），严格说它读起来像「可以选择在 24 小时后清理」；不同来源对此的表述不一致，方案里要引这一条时建议只引「时限由服务自己定、BeefTV 取 10 分钟」，避免抄一个可能有争议的数字。
5. 我没有跑任何代码、没有改业务实现。所有对照基于读 main `542124b` 的代码和 #17 分支的方案文档。「`updatedAt` 会被无关写入改掉」这一条在代码里证实了：`backend/internal/repository/repository.go:380` 与 `:387` 在每次写 stage/progress 时都显式设 `updated_at: time.Now()`，所以确认框打开期间的任何进度推进都会改掉这个值。

## 七、出处清单

| 主题 | 链接 | 用途 |
| --- | --- | --- |
| Stripe 幂等键 | https://docs.stripe.com/api/idempotent_requests | 幂等键三要素、24 小时、参数不一致报错、执行未开始可重试 |
| Stripe 错误码 | https://docs.stripe.com/error-codes/idempotency-key | `idempotency_key_in_use`（同键并发） |
| Stripe PaymentIntent 生命周期 | https://docs.stripe.com/payments/paymentintents/lifecycle | 状态机、`requires_capture`、确认次数过多自动取消 |
| Stripe 确认接口 | https://docs.stripe.com/api/payment_intents/confirm | 3DS 后无需再确认、确认次数上限、状态转移 |
| Stripe 授权/请款分开 | https://docs.stripe.com/payments/place-a-hold-on-a-payment-method | 先冻钱后请款、7 天过期释放 |
| Stripe 请款规则 | https://docs.stripe.com/payments/capture-later | 一次授权只允许一次捕获 |
| AWS EC2 ClientToken | https://docs.aws.amazon.com/AWSEC2/latest/APIReference/Run_Instance_Idempotency.html | 客户端令牌、`IdempotentParameterMismatch`、重试建议表 |
| AWS 幂等 API 设计 | https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/ | 「记录 token 与变更必须 ACID」、at most once、token 保留时长 |
| AWS 超时与重试 | https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/ | 「超时不代表副作用没发生」、有副作用且无幂等就不该重试 |
| RFC 9110 §9.2.2 | https://www.rfc-editor.org/rfc/rfc9110.html | 「结果不确定不得自动重试」的标准表述与两个放行条件 |
| RFC 6749 §4.1.2 | https://www.rfc-editor.org/rfc/rfc6749.html | 一次性凭据：10 分钟寿命、不得重复使用、重放要拒绝并撤销 |
| Google AIP-155 | https://google.aip.dev/155 | `request_id` 必须保证幂等、重复请求返回上次响应、时限自定、旧响应可用当前状态替代 |
| PayPal 幂等 | https://developer.paypal.com/api/rest/reference/idempotency/ | 不带请求 ID 就会重复；同键并发第二个可能失败 |
| Adyen 幂等 | https://docs.adyen.com/development-resources/api-idempotency/ | 键 7–14 天、公司级去重、会计规则兜底 |
| Brandur：幂等键实现 | https://brandur.org/idempotency-keys | 先提交本地记录再做外部副作用、recovery point、能只用本地状态就别用键 |
| Idempotent Receiver（EIP） | https://www.enterpriseintegrationpatterns.com/patterns/messaging/IdempotentReceiver.html | 「接收方能安全收到同一条消息多次」的模式定义 |
| Saga（微服务补偿） | https://microservices.io/patterns/data/saga.html | 跨服务业务要么全部完成、要么用补偿事务撤销 |
| Transactional outbox | https://microservices.io/patterns/data/transactional-outbox.html | 「消息当且仅当数据库事务提交时才发出」 |

报告是给改方案用的：第二节给依据，第三节回答问题，第四节给可直接改的条目，第五节给只改一处的取舍。