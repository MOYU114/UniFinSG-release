# UniFinSG 接口契约看板

版本：v2（2026-11-06，随 UniFinSG 2.0 同步；v1 的已下线部分见 §1.2.1）。依据：[2.0 设计稿](../v2/design-v2.md)、[已定决策](../questions/README.md)、[路线图](../roadmap/README.md)。

本文件是后端、Agent、前端并行开发**唯一的接口依据**。实现与本文不一致时，以本文为准；需要修改时先改本文。与 [2.0 设计稿](../v2/design-v2.md) 冲突时以设计稿为准。

2.0 的会话、Agent、BYOK、积分与上传接口编号使用 `API-AG2-*`（§4）；v1 的助手接口除 `GET /assistant/advice` 外全部下线。

---

## 1. 看板

### 1.1 状态图例与更新规则

| 状态 | 含义 |
| --- | --- |
| `未开始` | 尚未动工 |
| `开发中` | 在角色分支（如 `backend_dev`）上开发，尚未合并到 `feat_test` |
| `可联调` | 已合并到 `feat_test`，调用方可以从 mock 切换过来联调 |
| `已上线` | 已随 `feat_test` 合并到 `main` 并部署到云端验证 |
| `已删除` | 已于备注列写明的日期下线，路由不再注册；保留行是为了让旧调用方知道去哪里迁移 |
| `阻塞` | 等待依赖，在备注列写明原因 |

规则：

1. 提供方负责人修改本表的“状态”，并在“PR”列附合并到 `feat_test` 的提交或 PR 链接。
2. 契约经各方评审冻结之后，任何字段、路径或错误码的变更都必须和代码一起修改本文件，并在合并到 `feat_test` 前通知所有调用方。
3. 只新增可选字段、不删除或改名的变更视为兼容变更，仍需更新本文件，但不必等待调用方确认。

### 1.2 HTTP 接口

| 编号 | 方法 | 路径 | 提供方 | 调用方 | Roadmap | 阶段 | 状态 | PR |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| API-AUTH-01 | GET | `/api/auth/login` | 后端 auth | 前端 | BE-A2 | Phase 6 | 开发中 | |
| API-AUTH-02 | GET | `/api/auth/callback` | 后端 auth | Cognito（浏览器重定向） | BE-A2 | Phase 6 | 开发中 | |
| API-AUTH-03 | POST | `/api/auth/logout` | 后端 auth | 前端 | BE-A2 | Phase 1 local / Phase 6 cognito | 开发中 | |
| API-AUTH-04 | POST | `/api/users/register` | 后端 auth（仅 local） | 前端、压测 | BE-A1 | Phase 1 | 开发中 | |
| API-AUTH-05 | POST | `/api/users/login` | 后端 auth（仅 local） | 前端、压测 | BE-A1 | Phase 1 | 开发中 | |
| API-AUTH-06 | GET | `/api/me` | 后端 auth | 前端、压测 | BE-A3 | Phase 2 | 开发中 | |
| API-AUTH-07 | DELETE | `/api/users/:id` | 后端 auth | 前端 | BE-A4 | Phase 6 | 开发中 | |
| API-FIN-01 | GET | `/api/users/:id/profile` | 后端 finance | 前端 | BE-F1 | Phase 2 | 开发中 | |
| API-FIN-02 | PUT | `/api/users/:id/profile` | 后端 finance | 前端 | BE-F1 | Phase 2 | 开发中 | |
| API-FIN-03 | GET | `/api/users/:id/budgets/:month` | 后端 finance | 前端 | BE-F2 | Phase 2 | 开发中 | |
| API-FIN-04 | PUT | `/api/users/:id/budgets/:month` | 后端 finance | 前端 | BE-F2 | Phase 2 | 开发中 | |
| API-FIN-05 | GET | `/api/users/:id/transactions` | 后端 finance | 前端 | BE-F3 | Phase 3 | 开发中 | |
| API-FIN-06 | POST | `/api/users/:id/transactions` | 后端 finance | 前端、压测 | BE-F3 | Phase 3 | 开发中 | |
| API-FIN-07 | PATCH | `/api/users/:id/transactions/:tid` | 后端 finance | 前端 | BE-F3 | Phase 3 | 开发中 | |
| API-FIN-08 | DELETE | `/api/users/:id/transactions/:tid` | 后端 finance | 前端 | BE-F3 | Phase 3 | 开发中 | |
| API-FIN-09 | GET | `/api/users/:id/dashboard` | 后端 finance | 前端、压测 | BE-F5 | Phase 3 | 开发中 | |
| API-OFF-01 | GET | `/api/offers` | 后端 offers | — | — | Phase 4 | 已删除（2026-11-06） | Offers 下线，见 §1.2.1 |
| API-OFF-02 | GET | `/api/users/:id/offers/recommendations` | 后端 offers | — | — | Phase 4 | 已删除（2026-11-06） | Offers 下线，见 §1.2.1 |
| API-OFF-03 | GET | `/api/offers/:oid/go` | 后端 offers | — | — | Phase 4 | 已删除（2026-11-06） | Offers 下线，见 §1.2.1 |
| API-EVAL-01 | POST | `/api/eval/events` | 后端 eval | 前端（测试模式） | BE-E1 | Phase 5 | 开发中 | |
| API-EVAL-02 | GET | `/api/admin/eval/export` | 后端 eval | 产品组、运维 | BE-E1 | Phase 5 | 开发中 | |
| API-AG-01 | POST | `/api/users/:id/assistant/ask` | Agent | — | — | — | 已删除（2026-11-06） | 由 API-AG2-06 取代 |
| API-AG-02 | GET | `/api/users/:id/assistant/advice` | Agent | 前端 | AG-11 | 见 Agent 计划 | 可联调 | [`33d6e85`](https://github.com/MOYU114/UniFinSG/commit/33d6e85) |
| API-AG-03 | GET | `/api/users/:id/assistant/history` | Agent | — | — | — | 已删除（2026-11-06） | 由 API-AG2-05 取代 |
| API-AG-04 | GET | `/api/users/:id/assistant/quota` | Agent | — | — | — | 已删除（2026-11-06） | 由 API-AG2-10 取代 |
| API-AG2-01 | POST | `/api/users/:id/agent/sessions` | Agent | 前端 | 2.0 | P2 | 可联调 | [`ffd7750`](https://github.com/MOYU114/UniFinSG/commit/ffd7750) |
| API-AG2-02 | GET | `/api/users/:id/agent/sessions` | Agent | 前端 | 2.0 | P2 | 可联调 | [`ffd7750`](https://github.com/MOYU114/UniFinSG/commit/ffd7750) |
| API-AG2-03 | PATCH | `/api/users/:id/agent/sessions/:sid` | Agent | 前端 | 2.0 | P2 | 可联调 | [`ffd7750`](https://github.com/MOYU114/UniFinSG/commit/ffd7750) |
| API-AG2-04 | DELETE | `/api/users/:id/agent/sessions/:sid` | Agent | 前端 | 2.0 | P2 | 可联调 | [`ffd7750`](https://github.com/MOYU114/UniFinSG/commit/ffd7750) |
| API-AG2-05 | GET | `/api/users/:id/agent/sessions/:sid/messages` | Agent | 前端 | 2.0 | P2 | 可联调 | [`ffd7750`](https://github.com/MOYU114/UniFinSG/commit/ffd7750) |
| API-AG2-06 | POST | `/api/users/:id/agent/sessions/:sid/messages` | Agent | 前端、压测 | 2.0 | P2 | 可联调 | [`ffd7750`](https://github.com/MOYU114/UniFinSG/commit/ffd7750) |
| API-AG2-07 | POST | `/api/users/:id/agent/drafts/:draft_id/decision` | Agent | 前端 | 2.0 | P3 | 可联调 | [`1836e7a`](https://github.com/MOYU114/UniFinSG/commit/1836e7a) |
| API-AG2-08 | POST | `/api/users/:id/agent/uploads` | Agent | 前端 | 2.0 | P4 | 可联调 | [`11c5546`](https://github.com/MOYU114/UniFinSG/commit/11c5546) |
| API-AG2-09 | GET | `/api/users/:id/agent/uploads/:upload_id` | Agent | 前端 | 2.0 | P4 | 可联调 | [`11c5546`](https://github.com/MOYU114/UniFinSG/commit/11c5546) |
| API-AG2-10 | GET | `/api/users/:id/agent/credits` | Agent | 前端 | 2.0 | P1 | 可联调 | [`6d7ed89`](https://github.com/MOYU114/UniFinSG/commit/6d7ed89) |
| API-AG2-11 | GET | `/api/users/:id/llm-config` | Agent | 前端 | 2.0 | P1 | 可联调 | [`6d7ed89`](https://github.com/MOYU114/UniFinSG/commit/6d7ed89) |
| API-AG2-12 | PUT | `/api/users/:id/llm-config` | Agent | 前端 | 2.0 | P1 | 可联调 | [`6d7ed89`](https://github.com/MOYU114/UniFinSG/commit/6d7ed89) |
| API-AG2-13 | POST | `/api/users/:id/llm-config/test` | Agent | 前端 | 2.0 | P1 | 可联调 | [`6d7ed89`](https://github.com/MOYU114/UniFinSG/commit/6d7ed89) |
| API-AG2-14 | DELETE | `/api/users/:id/llm-config` | Agent | 前端 | 2.0 | P1 | 可联调 | [`6d7ed89`](https://github.com/MOYU114/UniFinSG/commit/6d7ed89) |
| API-OPS-01 | GET | `/healthz` | 运维 | ALB | OP-08 | 见运维计划 | 未开始 | |
| API-OPS-02 | GET | `/readyz` | 运维 | ALB、运维 | OP-08 | 见运维计划 | 未开始 | |

`API-AG2-*` 的 Roadmap 列写 `2.0`，阶段列对应 [设计稿 §12](../v2/design-v2.md) 的 P1–P5。

### 1.2.1 已删除接口（2026-11-06）

以下路由在 2.0 中**不再注册**，调用会得到 404。处理器代码可能仍留在树中供旧回归测试使用，但生产入口已关闭；数据库表保留不动，不写 down 迁移。

| 方法 | 路径 | 取代者 / 说明 |
| --- | --- | --- |
| POST | `/api/users/:id/assistant/ask` | API-AG2-06（会话内发消息） |
| GET | `/api/users/:id/assistant/history` | API-AG2-05（会话消息列表） |
| GET | `/api/users/:id/assistant/quota` | API-AG2-10（每日积分） |
| POST / GET / DELETE | `/api/users/:id/token` | 母本用户 token 管理；BYOK 改用 API-AG2-11–14，`apiKeys` 表不再使用 |
| POST | `/api/users/:id/conversation/*` | 母本旧聊天；改用 `/agent/sessions` 系列 |
| POST / GET | `/api/users/:id/uploads`（旧） | API-AG2-08 / 09（私有上传） |
| GET | `/api/offers`、`/api/users/:id/offers/recommendations`、`/api/offers/:oid/go` | Offers 模块整体下线，无替代 |

保留：`GET /api/users/:id/assistant/advice`（纯规则建议，不调模型、不扣积分，总览页继续使用，见 §4.9）。

### 1.3 内部契约与数据格式

| 编号 | 内容 | 提供方 | 使用方 | Roadmap | 阶段 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| GO-INSIGHT-01 | `insight.Snapshot` / `Provider`（§5） | 后端 finance | Agent | BE-00、BE-F4 | Phase 0 类型 / Phase 3 实现 | 已对接 |
| TOOL-01 | 工具注册表（§4.11）：7 个工具的输入 Schema 与 `readOnly` / `destructive` 标注 | Agent | 内部 Agent、MCP stdio | 2.0 | P2 / P3 / P5 | 已对接 |
| DATA-OFFER-01 | 优惠数据文件格式（原 §6） | — | — | — | — | 已删除（2026-11-06） |
| ENV-01 | 环境变量清单（§7） | 全体 | 运维 | BE-00、OP-08 | Phase 0 清单 / OP-08 环境接入 | 未开始 |
| MOCK-LLM-01 | OpenAI 兼容的 mock 服务（支持 `tool_calls` 与 `submit_extraction`） | Agent | 运维、压测 | AG-03 | P1 扩展 | 可联调 |

2026-10-28：`backend_dev` 已提交 [BE-00 类型与类别枚举](https://github.com/MOYU114/UniFinSG/commit/289446a)，供 Agent 编写假数据；尚未合并到 `feat_test`，待 Agent、前端评审后冻结，计算实现由 BE-F4 完成。

后端执行方式：按[后端 Phase 计划](../roadmap/backend.md)的依赖与验收条件推进，不按日期或周次排期。每轮实现及测试后，由独立 subagent 使用指定代码审查 skill 审查，修复并复审通过后按 phase 提交。接口定义与实际状态不变；Agent、前端和运维仍按各自计划推进，可在后端批次获准合并后接入。

---

## 2. 通用约定

### 2.1 基础

- 所有业务接口的前缀为 `/api`。线上经 CloudFront 与前端同源，本地开发由 Vite 代理到 `http://localhost:8090`。
- 请求与响应都是 `application/json; charset=utf-8`，除了 SSE（`text/event-stream`）、CSV 导出和 302 跳转。
- 字段名使用 `snake_case`。

### 2.2 鉴权（沿用母本机制）

| 项 | 值 |
| --- | --- |
| 会话 Cookie | `auth_token`，HttpOnly，SameSite=Lax，生产环境 Secure |
| CSRF Cookie | `csrf_token`，前端可读，SameSite=Strict |
| CSRF 请求头 | `X-CSRF-Token`，值等于 `csrf_token` Cookie（双重提交）；所有非 GET/HEAD/OPTIONS 请求必须携带 |
| Bearer | 母本中间件也接受 `Authorization: Bearer <auth_token>`，并免除 CSRF 校验；只供压测脚本使用，前端不要使用 |

- `/api/users/:id/...` 统一经过 `authMW → requirePathUser → CSRFMiddleware`：未登录返回 401；路径中的 `:id` 与登录身份不一致返回 403。请求体中的 `user_id` 一律忽略。
- 前端在拿到 `GET /api/me` 的 `user_id` 之后，用它拼接路径。
- `AUTH_MODE=cognito`（线上）：只开放 API-AUTH-01/02/03；API-AUTH-04/05 返回 404。
- `AUTH_MODE=local`（本地开发、压测、on-premise 基线）：开放 API-AUTH-04/05；API-AUTH-01/02 返回 404。登录后的所有接口在两种模式下完全相同。

### 2.3 错误格式

新接口统一使用：

```json
{
  "error": {
    "code": "validation_failed",
    "message": "amount_cents must be greater than 0",
    "fields": { "amount_cents": "must_be_positive" }
  }
}
```

`fields` 只在 `validation_failed` 时出现。母本旧接口仍返回 `{"error": "..."}` 字符串格式，前端需兼容两种格式；旧接口在拆分时再统一。

| HTTP | `code` | 含义 |
| --- | --- | --- |
| 400 | `bad_request` | 请求体无法解析、参数格式错误 |
| 400 | `validation_failed` | 字段校验失败，见 `fields` |
| 401 | `unauthenticated` | 未登录或会话过期 |
| 403 | `forbidden` | 访问他人数据，或管理 token 错误 |
| 403 | `csrf_invalid` | CSRF 校验失败（母本中间件当前返回 `{"error":"invalid csrf token"}`，BE-A1 时统一） |
| 404 | `not_found` | 资源不存在，或当前 `AUTH_MODE` 下未开放的接口 |
| 409 | `conflict` | 唯一约束冲突（如 local 注册用户名已存在） |
| 409 | `profile_required` | 需要先完成画像（如生成默认预算时没有画像） |
| 429 | `rate_limited` | 超过限流；响应头带 `Retry-After`（秒） |
| 429 | `quota_exceeded` | 仅 v1 遗留助手；2.0 已删除每日次数配额，改为 402 `credits_exhausted`（§4.1） |
| 500 | `internal` | 服务器错误，`message` 不包含内部细节 |
| 503 | `unavailable` | 依赖不可用（DB、Redis、Cognito） |

### 2.4 金额、日期与时区

- 金额一律为**整数分**，字段名以 `_cents` 结尾，货币固定为 SGD。前端负责显示为 `S$12.30`。
- 日期为 `YYYY-MM-DD`，月份为 `YYYY-MM`，时间戳为带时区的 RFC 3339（如 `2026-11-03T10:15:00+08:00`）。
- “今天”和“当月”一律按 `Asia/Singapore` 计算。
- 比例使用万分比整数 `*_bp`（`8000` = 80%），避免浮点误差。

### 2.5 类别枚举（记账、预算、工具与评估共用）

| 值 | 显示名 |
| --- | --- |
| `food` | Food |
| `transport` | Transport |
| `housing_tuition` | Housing / Tuition |
| `daily_shopping` | Daily Shopping |
| `study_digital` | Study / Digital |
| `entertainment_other` | Entertainment / Other |

### 2.6 地点枚举（已删除）

2.0 删除 Offers 后不再有地点枚举。`utown`、`kent_ridge_central` 等值只存在于历史迁移与旧数据中，任何接口都不再接受 `location` 参数。

### 2.7 其他枚举

| 字段 | 值 |
| --- | --- |
| `housing` | `on_campus`、`off_campus`、`with_family` |
| 类别状态 `status` | `ok`（< `warn_bp`）、`warn`（≥ `warn_bp` 且 ≤ 100%）、`over`（> 100%，优先判断）；`warn_bp` 默认 8000 |
| 评估任务 `task` | `onboarding`、`add_expense`、`understand_budget`、`get_advice`、`add_expense_by_chat` |
| 草稿 `kind` | `create_transactions`、`update_transaction`、`delete_transaction`、`update_budget` |
| 草稿 `status` | `pending`、`approved`、`rejected`、`expired`、`failed` |

历史评估数据中可能仍有 `find_offer` 行（Offers 下线前记录）。该值不再被接受为新的 `task`，只在 `view=events` 的原始导出中可读，详见 §3.4。

### 2.8 分页

列表接口使用 `page`（从 1 开始，默认 1）和 `page_size`（默认 20，最大 100），响应带：

```json
{ "items": [], "page": 1, "page_size": 20, "total": 0 }
```

---

## 3. 后端接口

### 3.1 auth

#### API-AUTH-01 `GET /api/auth/login`（仅 cognito）

- 查询参数：`redirect`（可选，前端站内路径，如 `/dashboard`；只接受以 `/` 开头的相对路径）。
- 行为：生成 `state` 与 PKCE `code_verifier`，写入 10 分钟有效的 HttpOnly Cookie，302 到 Cognito 托管登录页。
- flow Cookie 经应用客户端密钥派生的独立密钥加密，包含 `state`、PKCE verifier、nonce 与站内跳转路径；跨实例使用同一客户端配置即可校验。授权请求带 `openid email` scope、S256 challenge 和 nonce。拒绝外站、双斜杠、反斜杠和编码后变为外站的跳转。
- 响应：302，无响应体。

#### API-AUTH-02 `GET /api/auth/callback`（仅 cognito）

- 查询参数：`code`、`state`（由 Cognito 提供）。
- 行为：校验 `state`；换取 token；校验 ID token 的签名（JWKS）、`iss`、`aud`、`exp`、`token_use=id`；按 `sub` 查找或创建本地用户；设置 `auth_token` 与 `csrf_token` Cookie。
- 同时校验 nonce，仅接受 RS256；JWKS 缓存遇到未知 `kid` 时刷新。`sub` 按不透明、区分大小写的 ASCII 标识保存（1–255 字节），不强制 UUID 格式，不裁剪或归一化。邮箱和 Cognito username 仅从已验签 token 读取，不按邮箱合并账户；本地用户名使用 `cognito_` 加 subject 的 SHA-256 十六进制值，不开放密码登录。flow Cookie 在回调时清除，授权码与 state 不写入应用访问日志。
- 成功：302 到 `FRONTEND_URL + redirect`（默认 `/dashboard`）。
- 失败：302 到 `FRONTEND_URL + /signin?error=<code>`，`code` 取值 `state_mismatch`、`token_invalid`、`cognito_unavailable`。

#### API-AUTH-03 `POST /api/auth/logout`

- 需要 CSRF 头。两种模式都可用（local 模式替代母本的 `POST /api/users/:id/logout`，母本接口保留）。
- 响应 200：

```json
{ "logout_url": "https://unifinsg-prod.auth.ap-southeast-1.amazoncognito.com/logout?client_id=xxx&logout_uri=https%3A%2F%2Fd123.cloudfront.net%2Fsignin" }
```

local 模式下 `logout_url` 为 `null`，前端直接跳转到 `/signin`。

#### API-AUTH-04 `POST /api/users/register`（仅 local，沿用母本）

- 请求：`{"username": "test01", "password": "Passw0rd!"}`；用户名 3–32 位字母数字和 `_`，密码至少 8 位。
- 响应 201：`{"id": 12, "username": "test01", "created_at": "2026-10-28T10:00:00+08:00"}`
- 错误：`validation_failed`、`conflict`。

#### API-AUTH-05 `POST /api/users/login`（仅 local，沿用母本）

- 请求同上。成功时设置 Cookie。
- 响应 200（沿用母本字段，`auth_token` 供压测脚本使用 Bearer）：

```json
{ "id": 12, "username": "test01", "created_at": "2026-10-28T10:00:00+08:00", "auth_token": "…" }
```

- 错误：401 `unauthenticated`。

#### API-AUTH-06 `GET /api/me`

- 响应 200：

```json
{
  "user_id": 12,
  "email": "student@u.nus.edu",
  "auth_mode": "cognito",
  "onboarded": true,
  "current_month": "2026-11"
}
```

- `onboarded`：已保存画像且当月存在预算（包括自动复制生成的）时为 `true`。local 模式下 `email` 为用户名。
- 读取前按同一次请求捕获的 Asia/Singapore 当前月份检查预算：已有画像而当月预算缺失时，复用 API-FIN-03 的生成逻辑（复制最近一个更早的预算月，否则使用画像默认值），在用户行锁内再次检查并原子写入预算和全部类别。没有画像时不生成，`onboarded: false`；月份切换不要求用户重新引导。
- cognito 模式下 `email` 来自最近一次成功回调中已验签 ID token 的 email claim，`user_id` 始终为本地整数 ID；邮箱不参与授权和账户匹配。
- 错误：401。

#### API-AUTH-07 `DELETE /api/users/:id`

- 需要 CSRF 头。请求体：`{"confirm": "DELETE"}`。
- 行为：删除本地用户（级联删除画像、预算、记账、助手对话与审计）；cognito 模式下再调用 `AdminDeleteUser`；清除 Cookie。
- 本地删除事务成功后清理原有用户 worker 与会话缓存；SQL 会话记录被级联删除，即使 Redis 清理失败也不能继续鉴权。Cognito 删除只使用已保存的 provider username，不接受请求提供的身份。匿名评估事件与历史优惠表数据保留（优惠表自 2026-11-06 起不再读写）。
- 响应 204。若 Cognito 删除失败，本地数据仍删除，记录警告日志，响应仍为 204。
- 错误：400 `validation_failed`（未确认）。

### 3.2 finance

#### API-FIN-01 / 02 `GET|PUT /api/users/:id/profile`

- PUT 请求：

```json
{
  "monthly_disposable_cents": 120000,
  "housing": "on_campus",
  "default_budget_cents": 100000
}
```

- 校验：`monthly_disposable_cents > 0`；`housing` 在枚举中；`default_budget_cents ≥ 0`（缺省时取 `monthly_disposable_cents`）。
- 响应 200（GET 与 PUT 相同）：

```json
{
  "monthly_disposable_cents": 120000,
  "housing": "on_campus",
  "default_budget_cents": 100000,
  "updated_at": "2026-10-28T10:05:00+08:00"
}
```

- GET 在尚未设置时返回 404 `not_found`，前端据此进入引导页。

#### API-FIN-03 `GET /api/users/:id/budgets/:month`

- `:month` 为 `YYYY-MM`。
- 预算已存在时直接以一致的只读事务返回，无论月份是否在生成范围内，均不加 `FOR UPDATE`。
- 预算不存在时，仅允许为 Asia/Singapore 当月前后各 12 个月（含边界）自动生成。超出范围返回 404 `not_found`，不写入。范围内复制目标月份之前最近一个有预算的月份；没有则按画像默认值生成（6 类按固定比例拆分），并落库；`generated: true` 表示本次自动生成。没有画像时返回 409 `profile_required`。生成时沿用用户行锁，锁内重查预算，避免并发首次读取重复或混合版本。
- 响应 200：

```json
{
  "month": "2026-11",
  "total_cents": 100000,
  "warn_bp": 8000,
  "categories": [
    { "category": "food", "budget_cents": 40000 },
    { "category": "transport", "budget_cents": 10000 },
    { "category": "housing_tuition", "budget_cents": 0 },
    { "category": "daily_shopping", "budget_cents": 15000 },
    { "category": "study_digital", "budget_cents": 15000 },
    { "category": "entertainment_other", "budget_cents": 20000 }
  ],
  "category_sum_cents": 100000,
  "unallocated_cents": 0,
  "generated": false,
  "source": "copied_from_2026-10"
}
```

- `source` 取值：`saved`、`copied_from_YYYY-MM`、`profile_default`，以及由已确认的 Agent 草稿写入时的 `agent`。

#### API-FIN-04 `PUT /api/users/:id/budgets/:month`

- 请求：`total_cents`、`warn_bp`（可选，默认 8000，范围 5000–10000）、`categories`（必须包含全部 6 类）。
- 校验：金额 ≥ 0；类别之和可以不等于总预算，响应中通过 `unallocated_cents`（可为负数）提示差额。
- 只修改该月份，不影响其他月份。响应同 API-FIN-03（`generated: false`，`source: "saved"`）。

#### API-FIN-05 `GET /api/users/:id/transactions`

- 查询参数：`month`（默认当月）、`category`（可选）、`page`、`page_size`。
- 按 `spent_on` 倒序，同一天按 `created_at` 倒序。
- 响应 200：

```json
{
  "items": [
    {
      "id": 501,
      "amount_cents": 650,
      "spent_on": "2026-11-03",
      "category": "food",
      "merchant": "Fine Food",
      "note": "lunch",
      "source": "manual",
      "created_at": "2026-11-03T12:40:00+08:00",
      "updated_at": "2026-11-03T12:40:00+08:00"
    }
  ],
  "page": 1,
  "page_size": 20,
  "total": 1
}
```

- `source` 为 `manual` 或 `agent`；`agent` 表示由已确认的 Agent 草稿入账，前端在明细页显示 “AI” 标记。`agent` 记录另带 `draft_id`（非必返）。

#### API-FIN-06 `POST /api/users/:id/transactions`

- 请求：

```json
{ "amount_cents": 650, "spent_on": "2026-11-03", "category": "food", "merchant": "Fine Food", "note": "lunch" }
```

- 校验：`amount_cents` 为 1 – 100000000 的整数；`spent_on` 必填且不晚于新加坡时间的今天（可以是过去任意月份）；`category` 在枚举中；`merchant` ≤ 64 字符，`note` ≤ 200 字符，均可省略或为 `null`。
- 响应 201：单条记录（结构同列表项）。

#### API-FIN-07 `PATCH /api/users/:id/transactions/:tid`

- 请求：上述字段的任意子集，校验规则相同。响应 200：更新后的记录。
- 错误：404（不存在或不属于该用户，二者不区分）。

#### API-FIN-08 `DELETE /api/users/:id/transactions/:tid`

- 响应 204；错误 404。

#### API-FIN-09 `GET /api/users/:id/dashboard`

- 查询参数：`month`（默认当月）。
- 响应 200：即 `insight.Snapshot` 的 JSON（§5）。仪表盘和助手使用同一份数字。
- 查询新加坡当月时，已有画像但缺预算会先复用 API-FIN-03 的预算生成逻辑，再读取一致快照。显式查询过去或未来月份始终只读，不自动生成预算；没有画像时不生成，`missing` 按实际缺失状态包含 `profile` 和 `budget`。

```json
{
  "month": "2026-11",
  "as_of": "2026-11-12T09:00:00+08:00",
  "days_elapsed": 12,
  "days_in_month": 30,
  "total_budget_cents": 100000,
  "total_spent_cents": 46500,
  "remaining_cents": 53500,
  "usage_bp": 4650,
  "projected_spent_cents": 116250,
  "categories": [
    { "category": "food", "budget_cents": 40000, "spent_cents": 33000, "usage_bp": 8250, "status": "warn" },
    { "category": "transport", "budget_cents": 10000, "spent_cents": 4500, "usage_bp": 4500, "status": "ok" },
    { "category": "housing_tuition", "budget_cents": 0, "spent_cents": 0, "usage_bp": null, "status": "ok" },
    { "category": "daily_shopping", "budget_cents": 15000, "spent_cents": 3000, "usage_bp": 2000, "status": "ok" },
    { "category": "study_digital", "budget_cents": 15000, "spent_cents": 0, "usage_bp": 0, "status": "ok" },
    { "category": "entertainment_other", "budget_cents": 20000, "spent_cents": 6000, "usage_bp": 3000, "status": "ok" }
  ],
  "top_category": "food",
  "transaction_count": 18,
  "missing": [],
  "hash": "sha256:4f1c…"
}
```

- 预算为 0 时，`usage_bp` 一律返回 `null`（包括支出为 0 的情况）；零预算类别无支出时 `status` 为 `ok`，有支出时为 `over`。
- `projected_spent_cents` 在当月有记录的天数少于 3 天时为 `null`。
- 查询过去月份时，`days_elapsed = days_in_month`，`projected_spent_cents = total_spent_cents`。

### 3.3 offers（已删除）

2026-11-06 起，Offers（学生优惠）模块的前后端代码、路由、命令与数据文件全部删除：

- 不再注册 `GET /api/offers`、`GET /api/users/:id/offers/recommendations`、`GET /api/offers/:oid/go`；调用返回 404。
- `data/offers/` 与 `backend/cmd/import-offers` 已移除，不再有优惠导入流程（原 DATA-OFFER-01 作废，见 §6）。
- 数据库表 `offers`、`offer_clicks` 保留但不再读写，不写 down 迁移。
- 评估导出的 `clicks` 视图一并删除；历史点击数据不再通过接口暴露。

总览页原先的优惠卡片已移除；需要省钱建议时使用 `GET /assistant/advice` 的规则建议（§4.9）。

### 3.4 eval

#### API-EVAL-01 `POST /api/eval/events`

- 不需要登录；按 IP 限流（每分钟 60 次）。不需要 CSRF 头（不使用 Cookie 鉴权）。
- 限流窗口从该 IP 的首次请求开始计时 60 秒；请求校验失败也计入额度。第 61 次起返回 429 与 `Retry-After`，Redis 不可用时返回 503。IP 只以哈希形式进入短期 Redis key，不写入事件表；默认只信任连接地址，ALB 等代理部署须由运维与后端确认可信代理解析后接入，不能直接信任任意转发头。
- 请求：

```json
{ "participant_code": "P07", "task": "add_expense", "event": "end", "success": true, "error_code": null, "client_ts": "2026-11-12T14:03:22+08:00" }
```

- 校验：`participant_code` 匹配 `^P\d{2}$`；`task` 为 `onboarding`、`add_expense`、`understand_budget`、`get_advice`、`add_expense_by_chat` 之一；`event` 为 `start` 或 `end`；`end` 时 `success` 必填；`error_code` ≤ 32 字符。**不接受任何金额、备注或自由文本字段**，多余字段直接丢弃。已下线的 `find_offer` 不再被接受。
- `client_ts` 可空，非空时须为 RFC3339 时间；记录服务器 `received_at`，耗时只用服务器时间计算。
- 响应 202：`{"accepted": true}`。

#### API-EVAL-02 `GET /api/admin/eval/export`

- 请求头 `X-Admin-Token: <ADMIN_TOKEN>`；错误 token 返回 403。
- 查询参数：`format=csv|json`（默认 `csv`）、`view=summary|events`（默认 `summary`）。
- `summary`：每个任务的参与人数、成功率、耗时中位数和 P90、错误码分布。`events`：原始事件。
- `clicks` 视图已于 2026-11-06 随 Offers 下线删除；传入 `view=clicks` 返回 400 `bad_request`。
- 汇总按 `(received_at, id)` 排序后，以参与者与任务分组：每个 `start` 开启新尝试并替换此前未完成尝试；其后的第一个 `end` 完成该尝试。重复 `end`、没有 `start` 的 `end` 仍保留在原始导出中，但不增加汇总计数。每位参与者只按最新一次尝试计入汇总。
- 固定输出五个任务。`participants` 是有 `start` 的参与者数；`success_rate` 为最新尝试成功人数除以参与人数，未完成计为未成功，无参与者时为 `null`。耗时统计只包括最新尝试已完成的样本（含失败）；偶数个样本的中位数取中间两项均值，P90 取排序后第 `ceil(0.9*n)` 项，无样本时为 `null`。`top_errors` 统计最新失败尝试的非空错误码，每人最多一条。
- 历史数据中的 `find_offer` 行**不参与汇总**（汇总固定输出五个现行任务），只在 `view=events` 的原始导出中保持可读，`task` 原样返回 `find_offer`。
- JSON 两种视图都返回数组：`summary` 每项字段同 CSV，`top_errors` 为错误码到数量的对象；`events` 为 `id, participant_code, task, event, success, error_code, client_ts, received_at`（`success`、`error_code`、`client_ts` 可为 `null`）。
- CSV 事件表头使用上述字段顺序；`null` 输出为空单元格，错误码按原值字典序输出 `code:count` 并用分号分隔。错误码中的 `\`、`:`、`;` 分别转义为 `\\`、`\:`、`\;`，以便无歧义地还原分布；例如代码 `a:1;b` 出现两次表示为 `a\:1\;b:2`。可能被表格软件解释为公式的文本单元格，以及原本以单引号开头的单元格，都前置一个单引号；程序读取 CSV 后，遇到开头单引号时只移除一个，再按上述规则解析错误分布。导出禁用缓存；未配置 `ADMIN_TOKEN` 时返回 503，不允许空 token 访问。
- CSV 示例（summary）：

```
task,participants,success_rate,median_seconds,p90_seconds,top_errors
onboarding,18,0.94,71,118,validation_failed:2
```

---

## 4. Agent 接口（v2）

前缀为 `/api/users/:id/agent`；BYOK 配置例外，挂在 `/api/users/:id/llm-config`。所有路由仅在 MySQL 模式下注册；SQLite 启动时不注册并打印警告。

### 4.1 通用约定

- 鉴权与 CSRF 沿用 §2.2；除 `GET` 外的请求都必须带 `X-CSRF-Token`。
- 错误体沿用 §2.3 的 `{error:{code,message,fields?,details?}}`。SSE 一旦开始，错误只通过 `error` 事件传递，不再改变 HTTP 状态码。
- **模型永远不直接写账**：`propose_*` 工具只创建草稿；只有 API-AG2-07 的 `approve` 分支在数据库事务里写交易或预算。
- 限流：每用户每分钟 `ASSISTANT_RPM`（默认 5）次，发消息与草稿决策共用；超限返回 429 `rate_limited` 并带 `Retry-After`（秒）。
- 会话锁：同一会话同时只允许一个运行中的回合（Redis，TTL 120 秒）；冲突返回 409 `turn_in_progress`。
- 幂等：`client_request_id` 在 Redis 保留 10 分钟；同一 ID 重试返回 409 `duplicate_request`。
- 积分：回合开始按本回合最高可能档位预检（无图 2 分、有图 5 分），不足返回 402 `credits_exhausted`；结束按实际档位结算。BYOK 不预检、不扣分；草稿确认后的续写不扣分。
- 未产生任何输出的失败回合（上游错误、超时）不扣分。

v2 新增或沿用的错误码：

| HTTP | `code` | 含义 |
| --- | --- | --- |
| 400 | `bad_request` | 请求体不是单个 JSON 对象或字段类型错误 |
| 400 | `validation_failed` | 字段校验失败，见 `fields` |
| 402 | `credits_exhausted` | 当日积分不足；`details.resets_at` 为次日 00:00 SGT |
| 404 | `not_found` | 会话、草稿、图片不存在或不属于当前用户（二者不区分） |
| 409 | `turn_in_progress` | 该会话已有运行中的回合 |
| 409 | `duplicate_request` | `client_request_id` 已受理 |
| 409 | `draft_not_pending` | 草稿已处理、已过期或已失败 |
| 409 | `upload_used` | 图片已被另一条消息使用 |
| 410 | `upload_expired` | 图片已过期 |
| 413 | `upload_too_large` | 图片超过 10MB |
| 415 | `unsupported_media_type` | 文件头不是 PNG / JPEG / WebP |
| 422 | `draft_failed` | 草稿执行失败，已标记为 `failed` |
| 422 | `llm_test_failed` | BYOK 保存前连通测试失败；附 `upstream:{status,message}`，配置不保存 |
| 429 | `rate_limited` | 超过每分钟限流 |
| 503 | `llm_not_configured` | 平台模型未配置且该用户未启用 BYOK |
| 503 | `credits_unavailable` | 积分存储（Redis）不可用；BYOK 不受影响 |
| 503 | `unavailable` | 数据库、Redis 或存储暂不可用 |

### 4.2 会话

#### API-AG2-01 `POST /api/users/:id/agent/sessions`

- 请求：`{"title": "Lunch"}`，`title` 可省略；请求体必须是 JSON 对象，没有标题时传 `{}`。`title` 最长 120 字符，缺省为 `New chat`。
- 响应 201：

```json
{ "id": 42, "title": "New chat", "kind": "agent", "created_at": "2026-11-06T10:00:00+08:00", "updated_at": "2026-11-06T10:00:00+08:00" }
```

- 首条用户消息后，若标题仍为 `New chat`，后端截取消息内容（去首尾空格）前 20 个字符自动命名；纯图片消息不改名。

#### API-AG2-02 `GET /api/users/:id/agent/sessions`

- 查询参数：`cursor`（上一页返回的不透明游标）、`limit`（1–50，默认 20）。
- 按 `updated_at` 倒序。响应 200：

```json
{ "sessions": [ { "id": 42, "title": "Lunch", "kind": "agent", "created_at": "…", "updated_at": "…" } ], "next_cursor": "" }
```

- `next_cursor` 为空字符串表示没有更多；非空时原样作为下一次请求的 `cursor`。非法 `cursor` 返回 400 `validation_failed`。

#### API-AG2-03 `PATCH /api/users/:id/agent/sessions/:sid`

- 请求：`{"title": "November"}`，1–120 字符。
- 响应 200：更新后的会话对象；不存在或不属于该用户返回 404 `not_found`。

#### API-AG2-04 `DELETE /api/users/:id/agent/sessions/:sid`

- 级联删除该会话的消息、草稿与上传记录，并删除私有存储中的图片对象。
- 响应 204；不存在返回 404。

### 4.3 消息列表

#### API-AG2-05 `GET /api/users/:id/agent/sessions/:sid/messages`

- 查询参数：`before`（消息 ID，返回比它更早的消息）、`limit`（1–100，默认 20）。
- 读取前会惰性把超时草稿标记为 `expired`，并回填草稿的实时状态与图片的过期标记。
- 响应 200：

```json
{
  "messages": [
    {
      "id": 9000,
      "role": "user",
      "content": "record lunch 6.5",
      "parts": [{ "type": "text", "text": "record lunch 6.5" }],
      "turn_id": "8f2c…",
      "verified": null,
      "created_at": "2026-11-06T12:30:00+08:00"
    },
    {
      "id": 9001,
      "role": "assistant",
      "content": "Ready to record S$6.50 for lunch.",
      "parts": [
        {
          "type": "tool_call",
          "id": "call_1",
          "name": "propose_transactions",
          "args": { "items": [ { "amount_cents": 650, "spent_on": "2026-11-06", "category": "food", "note": "lunch" } ] }
        },
        {
          "type": "draft",
          "draft_id": "d_01J…",
          "kind": "create_transactions",
          "status": "pending",
          "payload": { "items": [ { "amount_cents": 650, "spent_on": "2026-11-06", "category": "food", "note": "lunch" } ] },
          "warnings": [],
          "expires_at": "2026-11-06T13:00:00+08:00"
        },
        { "type": "text", "text": "Ready to record S$6.50 for lunch." }
      ],
      "turn_id": "8f2c…",
      "verified": null,
      "created_at": "2026-11-06T12:30:02+08:00"
    }
  ],
  "has_more": false
}
```

- `messages` 按时间正序；`before` 用于向上翻页，`has_more` 表示还有更早的消息。
- `verified`：`true` / `false` 表示回答中的金额是否与本回合工具结果核对通过；没有工具调用时为 `null`。
- `parts` 元素：

| `type` | 字段 | 说明 |
| --- | --- | --- |
| `text` | `text` | 纯文本片段 |
| `image` | `upload_id`、`expired?` | `expired=true` 时前端显示“图片已过期” |
| `tool_call` | `id`、`name`、`args` | 模型发起的工具调用 |
| `tool_result` | `call_id`、`name`、`result`、`is_error` | 工具结果；`result` 为原始 JSON |
| `draft` | `draft_id`、`kind`、`status`、`payload`、`before?`、`warnings?`、`expires_at?`、`decided_at?`、`reject_reason?`、`result?` | 草稿卡片；`status` 取值见 §2.7 |

### 4.4 发消息（SSE）

#### API-AG2-06 `POST /api/users/:id/agent/sessions/:sid/messages`

- 请求：

```json
{ "content": "record lunch 6.5", "upload_ids": [], "client_request_id": "c0a8…" }
```

- 校验：`content` 1–2000 字符；`upload_ids` 存在时 `content` 可为空字符串；`upload_ids` 最多 5 个、不重复、属于同一会话、未过期且未被其他消息使用。
- 有图片时进入固定截图工作流（`status: extracting`），否则进入 ReAct 循环（每回合最多 `AGENT_MAX_STEPS` 次模型调用，默认 6，整回合 90 秒）。
- 成功时响应头 `Content-Type: text/event-stream`，事件如下（`event:` 名称 + JSON `data:`）：

| 事件 | data 字段 | 说明 |
| --- | --- | --- |
| `ack` | `turn_id`、`user_message_id`、`mode`、`credits_reserved` | 必定第一个发送；`mode` 为 `platform` 或 `byok`，`credits_reserved` 为本次预检占用的最高档积分（BYOK 为 0） |
| `status` | `phase`：`thinking` \| `calling_tool` \| `extracting` | 进度提示，可多次 |
| `tool_call` | `id`、`name`、`args` | 一次工具调用；`args` 为模型给出的参数 |
| `tool_result` | `id`、`name`、`ok`、`summary` | `summary` 为一句人类可读摘要，不发送完整结果 |
| `delta` | `text` | 回答文字增量，可多次 |
| `draft` | `draft_id`、`kind`、`payload`、`before?`、`warnings`、`expires_at` | 出确认卡片；`warnings` 取值如 `possible_duplicate`、`needs_review` |
| `done` | `status`、`assistant_message_id`、`verified`、`credits_charged`、`credits_remaining` | 必定最后一个发送（`error` 事件可能在其前） |
| `error` | `code`、`message`、`upstream?` | `upstream` 仅在 `code=llm_upstream_error` 时出现，含 `status` 与已截断的 `message` |

`draft` 示例（`create_transactions`）：

```json
{
  "draft_id": "d_01J…",
  "kind": "create_transactions",
  "payload": { "items": [ { "amount_cents": 650, "spent_on": "2026-11-06", "category": "food", "merchant": null, "note": "lunch" } ] },
  "warnings": [],
  "expires_at": "2026-11-06T13:00:00+08:00"
}
```

`update_transaction`、`delete_transaction`、`update_budget` 的 `draft` 事件额外带 `before`（修改前快照）。

`done` 示例：

```json
{ "status": "awaiting_approval", "assistant_message_id": 9001, "verified": null, "credits_charged": 2, "credits_remaining": 87 }
```

- `done.status` 取 `completed`、`awaiting_approval`、`failed`；有草稿待确认时为 `awaiting_approval`。
- `verified` 为 `null` 表示本回合没有可核对的工具结果。
- BYOK 模式下 `credits_charged` 为 0、`credits_remaining` 为 `null`。
- 流内 `error.code` 取值：`max_steps`（达到步数上限）、`turn_timeout`（整回合超时）、`llm_upstream_error`（上游错误，附 `upstream`）、`agent_failed`（其他失败）。
- 中途断流时，前端以 `done` 之后重新 `GET messages` 的结果为准。
- 截图工作流：抽取结果非空时直接创建 `create_transactions` 草稿并推送 `draft`，随后用一句 `delta` 说明识别结果；抽取为空时回退到 ReAct 循环。图片在本回合按 base64 发送，历史图片只以 `[图片]` 占位。

### 4.5 草稿决策（SSE）

#### API-AG2-07 `POST /api/users/:id/agent/drafts/:draft_id/decision`

- 请求：

```json
{ "action": "approve", "edited_payload": null, "reason": null }
```

- `action` 为 `approve` 或 `reject`；`edited_payload` 仅 `approve` 时可带（结构与草稿 `payload` 相同，省略或 `null` 表示按原参数执行）；`reason` 仅 `reject` 时可带，≤200 字符。
- 行为：先按 `status='pending' AND expires_at > NOW()` 原子消费草稿，再在数据库事务内执行；交易写入 `source='agent'` 并记录 `draft_id`。
- 响应为 SSE，事件格式与 API-AG2-06 相同，固定先发 `status: {phase:"thinking"}`，随后是续写的 `delta` / `tool_call` / `tool_result` / `draft` / `done`；续写不扣积分，`done.credits_charged` 恒为 0。
- 拒绝后模型收到的工具结果为 `{"status":"rejected","reason":"…","instruction":"…"}`，被要求不要重复提交同样内容。
- 错误：400 `bad_request`（`action` 不是 `approve`/`reject`）、400 `validation_failed`（修改后的参数非法）、404 `not_found`（不存在或他人草稿）、409 `draft_not_pending`（已处理或过期）、422 `draft_failed`（执行失败并已标记 `failed`）。

### 4.6 上传

#### API-AG2-08 `POST /api/users/:id/agent/uploads`

- `multipart/form-data`：`file`（必填）、`session_id`（必填）。
- 校验：会话归属；≤10MB；按文件头魔数只接受 `image/png`、`image/jpeg`、`image/webp`；不信任客户端 `Content-Type` 与扩展名。
- 响应 201：

```json
{ "upload_id": 77, "mime_type": "image/jpeg", "size": 184320, "expires_at": "2026-11-07T10:00:00+08:00" }
```

- 过期时间为 `now + UPLOAD_TTL_HOURS`（默认 24 小时）。
- 错误：400 `validation_failed`（缺 `session_id` / 空文件）、404 `not_found`（会话不存在）、413 `upload_too_large`、415 `unsupported_media_type`。

#### API-AG2-09 `GET /api/users/:id/agent/uploads/:upload_id`

- 私有读取：每次请求都校验登录态与归属，不使用预签名 URL，也不返回存储 key。
- 成功 200：响应体为图片字节，`Content-Type` 为实际 MIME，`Cache-Control: private, no-store`，`X-Content-Type-Options: nosniff`。
- 错误：404 `not_found`、410 `upload_expired`。

### 4.7 积分

#### API-AG2-10 `GET /api/users/:id/agent/credits`

- 只读，不消耗积分。平台模式：

```json
{
  "mode": "platform", "daily_grant": 100, "used": 13, "remaining": 87,
  "resets_at": "2026-11-07T00:00:00+08:00",
  "costs": { "chat": 1, "tool": 2, "vision": 5 }
}
```

- BYOK 模式：`{"mode":"byok","daily_grant":null,"used":null,"remaining":null,"resets_at":null,"costs":null}`。
- 每日额度按 Asia/Singapore 自然日重置，不累积；预检不足时 402：

```json
{ "error": { "code": "credits_exhausted", "message": "Your daily credits are used up. They reset at midnight (SGT).", "details": { "resets_at": "2026-11-07T00:00:00+08:00" } } }
```

- Redis 不可用时平台模式返回 503 `credits_unavailable`；BYOK 模式不受影响。

### 4.8 BYOK 模型设置

#### API-AG2-11 `GET /api/users/:id/llm-config`

- 无配置：`{"enabled": false, "configured": false}`。
- 有配置：

```json
{ "enabled": true, "configured": true, "kind": "openai_compatible", "base_url": "https://api.example.com/v1", "model": "gpt-4o-mini", "api_key_last4": "abcd", "last_test_at": "2026-11-06T10:00:00+08:00", "last_test_ok": true }
```

- **永不返回 Key 或其密文。**

#### API-AG2-12 `PUT /api/users/:id/llm-config`

- 请求：`{"kind":"openai_compatible","base_url":"https://…","model":"…","api_key":"sk-…","enabled":true}`。
- `kind` 只能是 `openai_compatible` 或 `anthropic`；`api_key` 省略表示沿用已存 Key；`enabled` 省略时新配置默认启用，已有配置保持原值。
- 保存前先做一次最小连通测试；失败返回 422 `llm_test_failed`（附 `upstream:{status,message}`）且不落库。
- 响应 200：同 API-AG2-11 的视图。
- 错误：400 `validation_failed`（`kind`、URL 或模型名非法）、422 `llm_test_failed`、503 `unavailable`。
- Base URL 只允许 `https`；`LLM_ALLOW_INSECURE_BYOK=true` 时（仅开发）允许 `http` 与 loopback。连接时检查目标 IP，拒绝私网、link-local（含云元数据地址）与重定向。

#### API-AG2-13 `POST /api/users/:id/llm-config/test`

- 请求体可省略；省略时使用已存配置，否则字段同 PUT（`enabled` 不参与）。
- 发一次最小请求（`max_tokens:1`，内容为 `ping`），计入每分钟限流。
- 响应 200：`{"ok": true, "latency_ms": 412}`；测试失败仍返回 200，`ok:false` 并附脱敏后的 `error`。
- 错误：400 `validation_failed`、429 `rate_limited`、503 `unavailable`。

#### API-AG2-14 `DELETE /api/users/:id/llm-config`

- 删除配置，回到平台模型。响应 204；无配置时同样 204。

### 4.9 保留的规则建议

#### API-AG-02 `GET /api/users/:id/assistant/advice`

- 查询参数：`month`（默认当月）。不调用 LLM、不扣积分、不写会话；总览页继续使用。
- 响应 200：

```json
{
  "month": "2026-11",
  "snapshot_hash": "sha256:4f1c…",
  "rule_advice": [ { "id": "food_warn", "text": "…", "numbers_cents": [40000] } ],
  "missing": [],
  "disclaimer": "This is educational budgeting guidance, not professional financial advice."
}
```

### 4.10 已删除的助手接口

`POST /assistant/ask`、`GET /assistant/history`、`GET /assistant/quota` 已于 2026-11-06 下线，分别由 API-AG2-06、API-AG2-05、API-AG2-10 取代；请求返回 404。详见 §1.2.1。

### 4.11 工具注册表（TOOL-01）

工具只在 `backend/internal/tools` 定义一次：内部 Agent 由注册表生成模型工具列表，MCP 适配器注册同一批条目，名称、Schema 与标注完全一致。工具通过显式传入的 `UserContext`（`user_id`、`session_id`、`turn_id`、`tool_call_id`）取身份，不读取全局状态。

| 工具 | `readOnly` | `destructive` | 输入 | 行为 |
| --- | --- | --- | --- | --- |
| `list_categories` | `true` | `false` | `{}` | 返回 6 个分类及中英文说明 |
| `get_budget_snapshot` | `true` | `false` | `{month?, affordability?:{amount_cents, category}}` | 返回预算快照与可选的可负担性判断；只读，不生成预算 |
| `list_transactions` | `true` | `false` | `{from?, to?, category?, query?, limit? 1..50}` | 交易列表，含 `id` 与 `source`；日期须同月，默认当月 |
| `propose_transactions` | `false` | `false` | `{items:[{amount_cents, spent_on?, category, merchant?, note?}] 1..20}` | 创建 `create_transactions` 草稿 |
| `propose_update_transaction` | `false` | `false` | `{transaction_id, changes:{amount_cents?, spent_on?, category?, merchant?, note?}}` | 校验归属后创建草稿，并存修改前快照 |
| `propose_delete_transaction` | `false` | `true` | `{transaction_id, reason?}` | 创建删除草稿，并存被删交易快照 |
| `propose_update_budget` | `false` | `false` | `{month, total_cents?, warn_bp?, categories?:[{category, budget_cents}]}` | 创建预算草稿，并存修改前预算 |

- 参数校验与 `finance` 的写入校验共用同一套规则（金额 1–100000000 分、`merchant` ≤64、`note` ≤200、6 类枚举、日期格式）。
- `propose_update_budget` 只修改该月已存在的预算：未提供的字段沿用原值，`categories` 里未列出的类别保持原值；该月没有预算时返回参数错误。
- `propose_*` 的返回值形如 `{draft_id, status:"pending_approval", …}`；模型每回合最多提交一个草稿，第二个 `propose_*` 会收到 `is_error` 结果。
- MCP 适配器（`internal/mcpserver` + `cmd/mcp-stdio`）本期只提供本地 stdio：

```bash
cd backend
go run ./cmd/mcp-stdio --user-id 1
```

- 该进程以单个用户身份连接本地 MySQL，供 MCP Inspector / Claude Code 调试。**不注册任何 HTTP 路由，也没有鉴权**。
- 通过 MCP 调用 `propose_*` 只会创建草稿；外部客户端无法确认草稿，确认只能在 UniFinSG 前端完成。这是本期的已知限制。

---

## 5. Go 内部契约（GO-INSIGHT-01）

由后端 finance 模块实现，Agent **只读取、不重算**任何金额或比例。仪表盘接口直接序列化 `Snapshot`。

```go
package insight

type Category string // 取值见 §2.5

type Status string

const (
    StatusOK   Status = "ok"
    StatusWarn Status = "warn"
    StatusOver Status = "over"
)

type CategoryStat struct {
    Category    Category `json:"category"`
    BudgetCents int64    `json:"budget_cents"`
    SpentCents  int64    `json:"spent_cents"`
    UsageBP     *int     `json:"usage_bp"` // 预算为 0 时为 nil
    Status      Status   `json:"status"`
}

type Snapshot struct {
    Month               string         `json:"month"`         // "2026-11"
    AsOf                time.Time      `json:"as_of"`         // Asia/Singapore
    DaysElapsed         int            `json:"days_elapsed"`
    DaysInMonth         int            `json:"days_in_month"`
    TotalBudgetCents    int64          `json:"total_budget_cents"`
    TotalSpentCents     int64          `json:"total_spent_cents"` // = 各类别 SpentCents 之和
    RemainingCents      int64          `json:"remaining_cents"`   // 可为负数
    UsageBP             *int           `json:"usage_bp"`
    ProjectedSpentCents *int64         `json:"projected_spent_cents"` // 数据不足时为 nil
    Categories          []CategoryStat `json:"categories"`            // 固定 6 项，按 §2.5 顺序
    TopCategory         *Category      `json:"top_category"`          // 无支出时为 nil
    TransactionCount    int            `json:"transaction_count"`
    Missing             []string       `json:"missing"` // "profile" / "budget" / "transactions"
    Hash                string         `json:"hash"`    // 以上内容的 sha256，用于审计
}

type Affordability struct {
    AmountCents           int64    `json:"amount_cents"`
    Category              Category `json:"category"`
    RemainingAfterCents   int64    `json:"remaining_after_cents"`
    CategoryUsageAfterBP  *int     `json:"category_usage_after_bp"`
    ExceedsTotal          bool     `json:"exceeds_total"`
    ExceedsCategory       bool     `json:"exceeds_category"`
}

type Provider interface {
    Snapshot(ctx context.Context, userID int64, month string) (*Snapshot, error)
    Affordability(s *Snapshot, amountCents int64, category Category) Affordability
}
```

- Phase 0 提交只含类型的 `backend/internal/insight/types.go`；真实实现随 Phase 3 记账与仪表盘闭环交付，Agent 可在该 phase 合并到 `feat_test` 后接入。假 `Snapshot` 保留供独立单测和实现交付前的开发使用。
- `Provider.Snapshot` 每次调用只捕获一次时间，用于判断新加坡当月和生成 `as_of`。读取当月且已有画像、预算缺失时，先通过 finance 的共享生成逻辑在用户行锁内补齐预算，再以只读 Repeatable Read 事务读取完整快照；过去／未来月份始终只读，没有画像时不生成预算。预算与类别使用同一一致性版本，`missing` 按最终读到的数据生成。
- `hash` 覆盖除自身之外的全部快照字段（包含 `as_of`），用于审计当次输出；它不是只随业务数据变化的数据版本号。相同 `as_of` 与相同数字得到相同 hash。
- 计算规则见 [改造方案 §3.5](../architecture/refactor-design-v1.md)：`warn` 为使用率 ≥ `warn_bp`，`over` 为 > 100%；当月有记录的天数 < 3 时不预测。
- 内部调用前置条件：`Affordability` 接受有效的六类 `Snapshot`、合法类别和正整数分，计算结果须能由上述整数类型表示。既定签名没有错误返回值；非法输入或无法表示的结果会触发固定错误消息的 panic，不能以截断、饱和值或 `null` 冒充精确结果。Agent 接入时须校验外部输入，并在调用边界处理该失败，不继续输出数字。此项须在合并 `feat_test` 前向 Agent 同步；HTTP 记账接口仍按自身校验规则返回结构化错误。

---

## 6. 优惠数据文件格式（DATA-OFFER-01，已删除）

2026-11-06 起该契约作废：`data/offers/`、`backend/cmd/import-offers` 与相关字段校验均已删除，仓库中不再有优惠数据模板或导入命令。历史迁移 `000006_offers` / `000007_offer_clicks` 保留，表内数据不再被读写。

如将来恢复类似功能，需要重新评审数据模型、接口与合规要求，不要沿用本节。

---

## 7. 环境变量清单（ENV-01）

“沿用”表示母本已有的变量名。注意：母本 `config.Load` 目前**不会展开** `config.json` 中的 `${MYSQL_HOST}` 等占位符，OP-08 需要实现环境变量覆盖。

| 变量 | 用途 | 使用方 | 敏感 | 备注 |
| --- | --- | --- | --- | --- |
| `UNICHATGO_CONFIG` | 配置文件路径 | 后端 | 否 | 沿用 |
| `UNICHATGO_DB` | 数据库驱动，固定 `mysql` | 后端 | 否 | 沿用 |
| `UNICHATGO_APIKEY_KEY` | AES-256-GCM 密钥（32 字节，标准 base64）；v2 起用于加密 BYOK Key，必填 | 后端（crypto） | 是 | 沿用变量名，用途变更 |
| `MYSQL_HOST` | MySQL 地址 | 后端 | 否 | 沿用 |
| `MYSQL_UNICHATGO_PASSWD` | MySQL 应用账号密码 | 后端 | 是 | 沿用 |
| `MYSQL_ROOT_PASSWD` | 本地 compose 的 root 密码 | 本地 compose | 是 | 沿用，线上不用 |
| `REDIS_HOST` / `REDIS_PASSWD` / `REDIS_DB` | Redis 连接 | 后端 | `REDIS_PASSWD` 是 | 沿用（compose 中为 `REDIS_PASSWD`） |
| `GOOGLE_API_KEY` / `GOOGLE_SEARCH_ENGINE_ID` | 旧功能的网页搜索 | 后端（旧聊天） | 是 | 沿用，可不设置 |
| `UNICHATGO_OPERATIONS_TOKEN` | 母本异步执行指标接口的 token | 后端 | 是 | 沿用，异步关闭时不需要 |
| `AUTH_MODE` | `cognito` 或 `local` | 后端 | 否 | 新增 |
| `COGNITO_REGION` | 如 `ap-southeast-1` | 后端 | 否 | 新增 |
| `COGNITO_USER_POOL_ID` | 用户池 ID | 后端 | 否 | 新增 |
| `COGNITO_CLIENT_ID` | 应用客户端 ID | 后端 | 否 | 新增 |
| `COGNITO_CLIENT_SECRET` | 后端保密应用客户端密钥 | 后端 | 是 | cognito 模式必填，跨实例一致 |
| `COGNITO_DOMAIN` | 托管登录页域名 | 后端 | 否 | 新增 |
| `COGNITO_REDIRECT_URI` | `…/api/auth/callback` | 后端 | 否 | 新增 |
| `FRONTEND_URL` | 登录完成后的跳转基址 | 后端 | 否 | 新增 |
| `ADMIN_TOKEN` | 评估导出接口的管理 token | 后端 eval | 是 | 新增 |
| `TRUSTED_PROXY_CIDRS` | eval IP 限流可信反向代理网段 | 后端 eval、运维 | 否 | 可选，逗号分隔 IPv4/IPv6 CIDR；直接读取环境变量，非法配置拒绝启动。仅 socket 直连 IP 属于可信网段时，从 `X-Forwarded-For` 右向左选第一个不可信 IP。未配置、无转发头、可信后缀格式错误或全链可信时使用直连 IP；不读取 `X-Real-IP`。部署时只配置实际代理网段 |
| `LLM_KIND` | 平台模型接口类型：`openai_compatible`（默认）或 `anthropic` | Agent | 否 | 新增 |
| `LLM_MODEL` | 平台模型名 | Agent | 否 | 沿用 |
| `LLM_API_KEY` | 平台模型 key | Agent | 是 | 沿用 |
| `LLM_BASE_URL` | 平台模型地址；压测时指向 mock-llm | Agent | 否 | 沿用 |
| `LLM_ALLOW_INSECURE_BYOK` | `true` 时允许 BYOK 使用 `http` 与 loopback（仅开发，如 Ollama） | Agent | 否 | 新增，默认 `false` |
| `ASSISTANT_RPM` | 每用户每分钟回合数（默认 5），平台与 BYOK 都生效 | Agent | 否 | 沿用 |
| `CREDITS_DAILY_GRANT` | 每日积分（默认 100，SGT 0 点重置、不累积） | Agent | 否 | 新增 |
| `CREDITS_COST_CHAT` / `CREDITS_COST_TOOL` / `CREDITS_COST_VISION` | 普通 / 带工具 / 带截图回合的扣分（默认 1 / 2 / 5） | Agent | 否 | 新增 |
| `AGENT_MAX_STEPS` | 每回合最多模型调用次数（1–20，默认 6） | Agent | 否 | 新增 |
| `AGENT_DRAFT_TTL_MINUTES` | 草稿有效期（1–1440，默认 30） | Agent | 否 | 新增 |
| `STORAGE_DRIVER` | `local`（默认）或 `s3` | Agent | 否 | 新增 |
| `UPLOAD_DIR` | `local` 驱动的图片目录（默认 `./data/uploads`） | Agent | 否 | 新增 |
| `UPLOAD_TTL_HOURS` | 上传记录过期时间（1–720，默认 24）；S3 部署须 ≤24，与生命周期规则一致 | Agent | 否 | 新增 |
| `S3_BUCKET` / `S3_REGION` | 私有上传桶与区域；`STORAGE_DRIVER=s3` 时必填，缺失则启动失败 | Agent、运维 | 否 | 新增 |
| `S3_PREFIX` | 对象 key 前缀（默认 `uploads/`） | Agent | 否 | 新增 |
| ~~`ASSISTANT_DAILY_LIMIT`~~ | v1 的每日提问次数上限 | — | — | **v2 删除**；改为每日积分（`CREDITS_*`），`ASSISTANT_RPM` 保留 |
| `LEGACY_UPLOADS_ENABLED` | 旧功能文件上传开关，云端默认 `false` | — | — | **不再读取**；v2 上传走私有存储 |
| `MOCK_LLM_TTFT_MS` / `MOCK_LLM_TOKENS_PER_SEC` / `MOCK_LLM_OUTPUT_WORDS` / `MOCK_LLM_ERROR_RATE` | mock-llm 行为 | mock-llm | 否 | 沿用 |
| `VITE_API_BASE_URL` | 前端 API 基址，默认 `/api` | 前端 | 否 | 沿用 |
| `VITE_AUTH_MODE` | 前端显示 Cognito 按钮或本地表单 | 前端 | 否 | 新增 |
| `VITE_USE_MOCK` | 前端使用本地 mock 数据（§8） | 前端 | 否 | 新增 |

`GIN_MODE=release` 时 Cookie 带 `Secure`，线上必须设置。

UniFinSG 要求 JSON 配置中的 `async.enabled=false`。设置为 `true` 时主程序在数据库、Redis 和迁移初始化之前立即以 fatal 错误退出，不支持母本 async 运行模式。

可信代理必须安全追加或覆盖 `X-Forwarded-For`，不能原样透传客户端可控头。多级代理须配置实际需要跨越的整条可信链；解析在第一个不可信地址停止，未被信任的上游代理会成为限流桶边界。真实部署的代理地址与转发模式仍需运维联调确认。

---

## 8. 前端 mock 说明

- §3、§4 中每个响应示例都可以直接作为 mock 数据，建议放在 `frontend/src/mocks/<接口编号>.json`，例如 `API-FIN-09.json`、`API-AG2-06.json`。
- 用 `VITE_USE_MOCK=true` 切换：axios 拦截器按“方法 + 路径模板”返回对应 JSON；SSE 的 mock 按 §4.4 的事件顺序发送 `ack → status → tool_call → tool_result → delta → draft → done`，每个事件间隔 50ms。
- 至少准备以下额外场景：仪表盘空数据（`transaction_count: 0`、`missing: ["transactions"]`）、类别超支（`status: "over"`）、回答 `verified: false`、402 `credits_exhausted`、429 `rate_limited`、草稿 `possible_duplicate` 警告、图片过期（`parts[].expired: true`）。
- 某个接口状态变为 `可联调` 后，前端负责人移除该接口的 mock，并在看板备注中注明。
