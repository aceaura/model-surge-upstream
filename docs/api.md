# ModelSurge Upstream API 参考

上游「配置中心 + 凭据下发」服务的 HTTP 接口规范。

- **文档版本**：1.3（2026-09-17）
- **对应实现**：`backend/httpapi`（`7150b91`；相对 1.2 无接口变更，本版把每个端点补齐为「使用场景 + 请求/响应字段表 + 错误表」的正式规格）
- **服务定位**：管理上游账号、模型与参数配置；对调用方下发请求目标（地址、认证头、参数策略）。**不转发数据面聊天流量，不承载调度运行时状态。**

每个端点按固定模板描述：**使用场景 → 请求（路径参数 / 查询参数 / 请求体字段表）→ 响应（字段表）→ 错误 → 示例**。字段表的类型与枚举取值以 §2.5、§2.8 的约定为准；跨端点复用的响应结构在 §3 定义为数据模型，端点章节直接引用。

---

## 1. 概述

服务暴露两组接口与一个健康检查：

| 分组 | 前缀 | 密钥 | 用途 |
|---|---|---|---|
| 健康检查 | `/healthz` | 无 | 依赖就绪状态 |
| 管理面 | `/admin/*` | `MSU_ADMIN_KEY` | 配置的增删改查（桌面客户端使用） |
| 下发面 | `/v1/*` | `MSU_DELIVERY_KEY` | 只读的目标解析（调用方使用） |

两把密钥相互独立：管理密钥访问下发面返回 `401`，反之亦然（密钥校验先于路由匹配）。

核心概念为三层：

- **provider**：内置提供商规格，编译期常量，运行期不可修改，无任何接口可变更；
- **account**：某提供商下的一个具体账号（凭据 + 可选端点覆盖），管理面可增删改；
- **model**：对外暴露的模型标识，归属某账号，声明协议与参数策略（`defaults`/`overrides`）。

---

## 2. 通用约定

### 2.1 基础地址

```
http://<host>:<port>
```

默认监听 `:8080`（环境变量 `MSU_LISTEN`，见附录 A）。

### 2.2 认证

除 `GET /healthz` 外，所有接口要求请求头：

```
Authorization: Bearer <密钥>
```

| 约定 | 说明 |
|---|---|
| 前缀 | `Bearer`，大小写不敏感；其后允许空白 |
| 校验 | 常数时间比较（`crypto/subtle`），防时序侧信道 |
| 失败 | 密钥缺失、格式不符或不正确一律返回 `401` `unauthorized`，不区分原因 |

### 2.3 请求体

- 带请求体的接口（POST/PUT）请求体必须为 JSON 对象，建议携带 `Content-Type: application/json`；
- 请求体**禁止未知字段**：出现未声明的字段名直接返回 `400` `invalid_json`，以此尽早暴露拼写错误；
- GET/DELETE 忽略请求体；
- 「字段缺省」（JSON 中不出现该键）与「字段为空值」（空串/`0`/`null`）语义不同，区别在各端点的字段表与省略语义说明中给出。

### 2.4 时间格式

所有时间字段为 RFC 3339 / UTC，例如 `2026-09-16T01:18:46.614536431Z`。

### 2.5 数据类型与字段表约定

字段表中使用的类型词汇：

| 类型 | 含义 |
|---|---|
| `string` | UTF-8 文本 |
| `bool` | `true` / `false` |
| `int` | JSON number，整数 |
| `number` | JSON number，浮点（IEEE 754 双精度） |
| `object` | JSON 对象 `{}` |
| `array of X` | JSON 数组，元素类型 X；**本服务数组字段恒为数组，空时不输出 `null`** |
| `map[string]string` | 键与值均为 string 的对象 |
| `time` | RFC 3339 UTC 字符串 |
| `枚举` | 有限取值集，全部枚举及其含义见 2.8 |

### 2.6 错误响应

业务错误（鉴权失败、校验失败、存储故障等）为统一信封：

```json
{
  "error": {
    "code": "not_found",
    "message": "account \"foo\" not found",
    "status": 404
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `error.code` | string | 机器可读错误码，完整清单见 2.7 |
| `error.message` | string | 人类可读描述；非领域错误只回 `internal error`，不泄露底层细节（如连接串） |
| `error.status` | int | 与 HTTP 状态码一致 |

不属于错误信封的三类响应：

| 响应 | 形态 |
|---|---|
| 路由层 404（未知路径） | `text/plain` 空信封，标准库行为 |
| 路由层 405（方法不匹配） | `text/plain`，带 `Allow` 头（如 `Allow: GET, HEAD`） |
| `GET /healthz` 的 503 | 健康状态体（见 §4），不是错误信封 |

### 2.7 错误码

| code | HTTP | 触发场景 |
|---|---|---|
| `unauthorized` | 401 | 密钥缺失或错误（含跨面使用密钥） |
| `not_found` | 404 | 账号/模型不存在；模型引用的账号不存在 |
| `already_exists` | 409 | 账号或模型重名 |
| `account_disabled` | 409 | resolve 时账号被禁用 |
| `model_disabled` | 409 | resolve 时模型被禁用 |
| `invalid_provider` | 400 | `provider_id` 未知，或账号引用的 provider 不在规格表内 |
| `invalid_credential` | 400 | 凭据非法：kind 缺失/不支持、与 provider 声明不符、api_key 为空 |
| `invalid_protocol` | 400 | 协议不被账号所属 provider 支持 |
| `invalid_json` | 400 | 请求体不是合法 JSON、含未知字段、defaults/overrides 不是 JSON 对象 |
| `invalid_request` | 400 | 必填字段缺失、`base_url` 非法、`context_window` 为负等 |
| `quota_unavailable` | 502 | 上游额度接口请求失败或返回非 2xx |
| `upstream_unavailable` | 502 | 上游自身接口（如模型列举）请求失败或返回非 2xx |
| `storage_error` | 500 | PostgreSQL 故障；其他非领域错误的兜底 |

### 2.8 枚举值

**`protocol`（协议标识）**

| 值 | 含义 |
|---|---|
| `anthropic` | Anthropic Messages 协议（`POST /v1/messages`） |
| `chat_completions` | OpenAI Chat Completions 协议（`POST /v1/chat/completions`） |
| `responses` | OpenAI Responses 协议（`POST /v1/responses`） |
| `gemini` | Google Gemini 协议（`generateContent`） |

**`auth`（认证头形态，provider 声明）**

| 值 | 生成的请求头 |
|---|---|
| `bearer` | `Authorization: Bearer <api_key>` |
| `anthropic_key` | `x-api-key: <api_key>` 与 `anthropic-version: 2023-06-01` |

**`credential.kind`（凭据形态）**

| 值 | 含义 |
|---|---|
| `api_key` | 静态 API 密钥（本期唯一；kind 字段为将来刷新型凭据预留判别式） |

**`MeterKind`（计量项形态）**

| 值 | 含义 |
|---|---|
| `balance` | 预付费余额，充值才涨，无周期重置 |
| `usage` | 后付费已用量，可能带月度上限，没有"余量"概念 |
| `rate_limit` | 滚动速率窗口（如 RPM/TPM），requests 与 tokens 是并存独立维度 |

**`MeterUnit`（计量单位）**

| 值 | 含义 |
|---|---|
| `currency` | 货币金额，币种见同条目 `currency` 字段 |
| `requests` | 请求数 |
| `tokens` | token 数 |
| `credits` | 平台积分/点数 |

**`ResetRule`（重置规律）**

| 值 | 含义 |
|---|---|
| `none` | 不重置 |
| `rolling` | 滚动窗口（如每分钟滑动） |
| `daily` | 每日重置 |
| `monthly` | 每月重置 |
| `prepaid` | 预付费：无周期重置，充值才变化 |

---

## 3. 数据模型

跨端点复用的响应结构在此定义，端点章节以「`X`（见 3.n）」引用。

### 3.1 Provider（内置规格）

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `id` | string | 恒有 | 稳定标识，创建账号时 `provider_id` 只能取这些值 |
| `display_name` | string | 恒有 | 显示名 |
| `website` | string | 恒有 | 官网地址 |
| `base_url` | string | 恒有 | 默认根地址；账号未覆盖 `base_url` 时使用 |
| `protocols` | array of string | 恒有 | 支持的协议，取值见 2.8 `protocol`；创建模型时 `protocol` 只能取账号 provider 的此集合 |
| `auth` | 枚举 | 恒有 | 认证头形态，见 2.8 `auth` |
| `credential` | 枚举 | 恒有 | 凭据形态，本期恒为 `api_key` |
| `quota` | object | 该 provider 声明了额度端点时 | 额度接口声明，子字段见下 |
| `quota.path` | string | 同上 | 额度端点路径，拼接在生效 `base_url` 之后 |
| `quota.method` | string | 同上 | HTTP 方法，当前恒为 `GET` |
| `quota.kind` | 枚举 | 同上 | 主计量项形态，见 2.8 `MeterKind`；作为解析结果的兜底语义 |
| `quota.unit` | 枚举 | 同上 | 主计量项单位，见 2.8 `MeterUnit` |
| `quota.reset` | 枚举 | 同上 | 重置规律，见 2.8 `ResetRule` |
| `models` | object | 该 provider 声明了列举端点时 | 模型列举端点声明，子字段见下 |
| `models.path` | string | 同上 | 列举端点路径 |
| `models.method` | string | 同上 | HTTP 方法，当前恒为 `GET` |

当前注册序（固定顺序，共 6 家）：

| id | base_url | protocols | auth | quota | models |
|---|---|---|---|---|---|
| `anthropic` | `https://api.anthropic.com` | `anthropic` | `anthropic_key` | — | `/v1/models` |
| `openai` | `https://api.openai.com` | `chat_completions`, `responses` | `bearer` | — | `/v1/models` |
| `gemini` | `https://generativelanguage.googleapis.com` | `gemini`, `chat_completions` | `bearer` | — | `/v1beta/models` |
| `kimi` | `https://api.moonshot.cn/coding` | `anthropic`, `chat_completions` | `anthropic_key` | — | `/v1/models` |
| `ark` | `https://ark.cn-beijing.volces.com/api/v3` | `anthropic`, `chat_completions` | `bearer` | — | — |
| `deepseek` | `https://api.deepseek.com` | `anthropic`, `chat_completions` | `bearer` | `{path: "/user/balance", method: "GET", kind: "balance", unit: "currency", reset: "prepaid"}` | `/models` |

> 除 `deepseek` 外五家均未声明 `quota`：它们没有可用的额度端点。注意速率窗口维度不依赖 `quota` 声明的形态字段，只要额度端点通了就会从响应头一并读出（见 3.6）。

> `ark` 不声明 `models`：其列举端点实测各路径恒返回 `401`，声明了也只会稳定失败。同理其 `responses` 协议实测不可用，故未列入 `protocols`。

### 3.2 Account View（账号读取形态）

管理面一切读取路径返回该形态，**凭据一律脱敏**：

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `name` | string | 恒有 | 账号唯一标识 |
| `provider_id` | string | 恒有 | 所属提供商 id，见 3.1 |
| `credential.kind` | 枚举 | 恒有 | `api_key` |
| `credential.api_key` | string | 恒有 | **脱敏值**，规则：空串回空串；长度 ≤ 8 全掩为 `***`；否则前 4 位 + `***` + 后 4 位 |
| `base_url` | string | 恒有 | 端点覆盖；空串表示沿用 provider 默认 |
| `headers` | map[string]string | 恒有 | 附加到上游请求的自定义头（原值，不脱敏）；空对象表示无 |
| `enabled` | bool | 恒有 | `false` 时其下所有模型在 resolve 中返回 `409 account_disabled` |
| `created_at` | time | 恒有 | 创建时刻 |
| `updated_at` | time | 恒有 | 最后更新时刻 |

### 3.3 Model（模型）

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `id` | string | 恒有 | 模型唯一标识，**可含 `/`**（如 `ds-1/v4`） |
| `account` | string | 恒有 | 归属账号名；账号删除时模型级联删除 |
| `native_model` | string | 恒有 | 上游真实模型名，构造上游请求体时使用 |
| `protocol` | 枚举 | 恒有 | 见 2.8 `protocol`；创建/更新时须被账号 provider 支持 |
| `context_window` | int | 恒有（管理面形态） | 上下文窗口 token 数声明；`0` 表示未声明 |
| `defaults` | object | 恒有 | 缺省填充参数：调用方缺什么补什么；空对象表示无 |
| `overrides` | object | 恒有 | 强制覆盖参数：无论调用方给什么都压盖；空对象表示无 |
| `enabled` | bool | 恒有 | 模型开关；`false` 时 resolve 返回 `409 model_disabled` |
| `created_at` | time | 恒有 | |
| `updated_at` | time | 恒有 | |

### 3.4 Listing（下发面模型清单元素）

不含任何凭据：

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `id` | string | 恒有 | 模型标识 |
| `account` | string | 恒有 | 归属账号 |
| `provider_id` | string | 恒有 | 由账号推导 |
| `protocol` | 枚举 | 恒有 | 见 2.8 |
| `native_model` | string | 恒有 | 上游真实模型名 |
| `context_window` | int | 非 0 时 | `0`（未声明）时字段缺省 |
| `enabled` | bool | 恒有 | `model.enabled && account.enabled`——账号停用会使其下全部模型不可用 |

### 3.5 ResolvedTarget（解析结果）

`POST /v1/resolve` 的响应体：

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `model_id` | string | 恒有 | 回显请求的模型标识 |
| `account` | string | 恒有 | 归属账号名 |
| `provider_id` | string | 恒有 | 提供商 id |
| `protocol` | 枚举 | 恒有 | 见 2.8；决定调用方构造哪种协议的请求体 |
| `base_url` | string | 恒有 | 生效根地址 = 账号 `base_url` 非空则用之，否则 provider 默认；无尾部 `/` |
| `native_model` | string | 恒有 | 写入上游请求体的模型名 |
| `context_window` | int | 非 0 时 | `0`（未声明）时字段缺省 |
| `headers` | map[string]string | 恒有 | **含真实凭据**的认证头 + 账号自定义头，构成规则见下 |
| `defaults` | object | 恒有 | 原样下发，不与服务端任何参数预合并 |
| `overrides` | object | 恒有 | 同上 |

`headers` 的构成规则：

1. 按 provider 的 `auth` 生成认证头（取值见 2.8 `auth` 表）；
2. 账号 `headers` **最后叠加**，同名键覆盖上述任何头（用于自建网关改写认证、追加 `anthropic-version` 等场景）。

**安全边界**：`headers` 含真实凭据，这是下发面存在的意义；服务端日志对该结构脱敏（`Authorization`、`x-api-key` 两个键名遮蔽），但响应体本身带原值，调用方须按密钥同级保管。

**参数合并职责在调用方**：调用方应以 `defaults ← 自身请求参数 ← overrides` 的顺序叠加（对象递归合并，数组与标量整体替换）。服务端不预合并，因为 defaults 与 overrides 语义不同，合并后无法区分。

### 3.6 QuotaReport（额度报告）

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `account` | string | 恒有 | 账号名 |
| `queryable` | bool | 恒有 | `false` 表示该 provider 未声明额度接口，**属正常答案而非错误**，此时 `meters` 为空数组 |
| `meters` | array of Meter | 恒有（空时为 `[]`） | 计量项列表；端点通了但响应无可识别字段时也为空数组 |
| `at` | time | 恒有 | 本次查询时刻 |

**为什么是列表而非单值**：上游额度有四类形态——预付费余额（充值才涨）、后付费已用量（没有"余量"可言）、订阅周期配额（关键信息是下次重置时刻）、滚动速率窗口（`requests` 与 `tokens` 是并存的独立计数，各有各的余量与重置）。单值结构只能表达第一类，因此报告承载一组计量项，单值形态退化成只有一项。

**Meter（计量项）**

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `kind` | 枚举 | 恒有 | 见 2.8 `MeterKind` |
| `unit` | 枚举 | 恒有 | 见 2.8 `MeterUnit`；数值本身说不出自己是钱还是请求数，故必填 |
| `label` | string | 有值时 | 给人看的维度名，如币种 `CNY`、速率维度 `tokens` |
| `currency` | string | 有值时 | 币种缩写（如 `CNY`/`USD`），仅 `unit` 为 `currency` 时有意义 |
| `remaining` | number | 有值时 | 余量；解析不出则缺省 |
| `total` | number | 有值时 | 总量或上限 |
| `used` | number | 有值时 | 已用量。后付费形态往往只有此项 |
| `reset` | 枚举 | 有值时 | 见 2.8 `ResetRule` |
| `reset_at` | time | 有值时 | 下次重置的绝对时刻，上游给了才有。周期配额与速率窗口下这比静态的 `reset` 规律更有用 |

**数值来源**：响应体内的计量项，其 `kind`/`unit`/`reset` 以 provider 规格中的额度声明（见 3.1）兜底，上游响应自带更精确信息时以响应为准；速率窗口维度来自响应头 `x-ratelimit-{remaining,limit,reset}-{requests,tokens}`，重置时刻同时接受 RFC3339 与 Unix 秒两种写法。认不出的字段一律不猜——只报 `queryable: true` 而不产出计量项。

额度报告只在进程内存缓存（带 TTL，默认 60s，`MSU_QUOTA_TTL`），不落库；账号更新或删除时缓存立即失效。查询上游使用与 resolve 相同的认证头（含账号自定义头）。

### 3.7 UpstreamModelsReport（上游模型清单）

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `account` | string | 恒有 | 账号名 |
| `queryable` | bool | 恒有 | `false` 表示该 provider 未声明列举接口，**属正常答案而非错误**，此时 `models` 为空数组 |
| `models` | array of Entry | 恒有（空时为 `[]`） | 上游返回的模型条目，按 `id` 升序排列 |
| `at` | time | 恒有 | 本次查询时刻 |

Entry：

| 字段 | 类型 | 出现条件 | 取值与含义 |
|---|---|---|---|
| `id` | string | 恒有 | 上游模型标识 |
| `display_name` | string | 有值时 | 显示名，上游未提供时缺省 |

解析行为：兼容 `data`（OpenAI/Anthropic/DeepSeek）与 `models`（Gemini）两种外层键，条目标识依次尝试 `id`、`name`、`model`；重复标识折叠，无标识的条目跳过；外层结构无法识别时返回空数组而不编造条目。

与额度一致：只在进程内存缓存（TTL 复用 `MSU_QUOTA_TTL`，默认 60s），不落 PostgreSQL 也不进 Redis；账号更新或删除时缓存立即失效；请求上游使用与 resolve 相同的认证头。

> 该接口与 `GET /v1/models` 的区别：本接口回答「上游账号实际能用哪些模型」（上游事实），`/v1/models` 回答「本服务已配置哪些模型」（本地配置）。两者互补，可用于核对配置是否与上游现状一致。

---

## 4. 健康检查

### 4.1 查询就绪状态

**使用场景**：容器编排的 healthcheck、调用方启动自检。判断 PostgreSQL（权威存储）与 Redis（可选缓存）是否就绪。

```
GET /healthz
```

**请求**：无路径参数、无查询参数、无请求体；**免鉴权**。

**响应** `200`（就绪）或 `503`（PostgreSQL 不可达），字段相同：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `ready` | bool | 服务可用性，恒等于 `database` |
| `database` | bool | PostgreSQL 可达性；`false` 时整个服务判定不可用 |
| `cache` | bool | Redis 可达性；**`false` 不影响 `ready`**——缓存仅是加速，故障时降级直读 PG。未配置 Redis（`MSU_REDIS_ADDR` 缺省）时恒为 `false` |

> `503` 响应体是上述健康状态体，**不是** 2.6 的错误信封。

**示例**：

```bash
curl http://localhost:8080/healthz
```

```json
{ "ready": true, "database": true, "cache": true }
```

---

## 5. 管理面 API

以下接口均要求 `Authorization: Bearer <MSU_ADMIN_KEY>`（见 2.2）。成功与错误响应遵循 2.5 / 2.6 的约定；各端点错误表只列领域错误，`401 unauthorized`（密钥缺失/错误/跨面使用）与 `500 storage_error`（PG 故障）对所有端点通用，不再逐条重复。

### 5.1 列举 provider

**使用场景**：管理客户端启动时拉取内置规格表——渲染「新建账号」表单的 provider 下拉框、展示各家默认 `base_url` 与支持的协议、判断某账号能否查询额度（3.1 的 `quota`）与上游模型清单（3.1 的 `models`）。

```
GET /admin/providers
```

**请求**：无路径参数、无查询参数、无请求体。

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `providers` | array of Provider | 元素结构见 3.1；按固定注册序排列，当前恒为 6 个元素 |

**错误**：无领域错误。

**示例**：

```bash
curl http://localhost:8080/admin/providers \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

```json
{
  "providers": [
    {
      "id": "anthropic",
      "display_name": "Anthropic",
      "website": "https://www.anthropic.com",
      "base_url": "https://api.anthropic.com",
      "protocols": ["anthropic"],
      "auth": "anthropic_key",
      "credential": "api_key",
      "models": {"path": "/v1/models", "method": "GET"}
    }
  ]
}
```

> 上例仅展示首个元素；`quota` / `models` 未声明的 provider 相应字段缺省。

### 5.2 列举账号

**使用场景**：管理客户端主界面的账号列表。

```
GET /admin/accounts
```

**请求**：无路径参数、无查询参数、无请求体。

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `accounts` | array of Account View | 元素结构见 3.2；按 `name` 升序；**凭据一律脱敏** |

**错误**：无领域错误。

**示例**：

```bash
curl http://localhost:8080/admin/accounts \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

### 5.3 创建账号

**使用场景**：接入一个新的上游账号（新申请的 API key、或自建网关的一个入口）。

```
POST /admin/accounts
```

**请求体字段**：

| 字段 | 类型 | 必填 | 约束 / 允许值 | 含义 |
|---|---|---|---|---|
| `name` | string | 是 | 首尾空白剥除后非空；全局唯一，重名 `409 already_exists`；**不应含 `/`**（见下注） | 账号唯一标识 |
| `provider_id` | string | 是 | 须精确等于 3.1 表中某个 `id`（**不做空白剥除、大小写敏感**），未知返回 `400 invalid_provider` | 所属提供商 |
| `api_key` | string | 与 `credential` 二选一 | 非空 | 凭据简写，等价于 `credential: {"kind":"api_key","api_key":…}` |
| `credential` | object | 与 `api_key` 二选一 | 须为 `{"kind":"api_key","api_key":"…"}`；`kind` 须与 provider 声明的 `credential` 一致 | 完整凭据结构；**与 `api_key` 同时给出时以本字段为准** |
| `base_url` | string | 否 | 缺省或空串 = 沿用 provider 默认；非空时首尾空白剥除、尾部 `/` 剥除后须以 `http://` 或 `https://` 开头 | 上游根地址覆盖 |
| `headers` | map[string]string | 否 | 缺省落库为 `{}`；键值不做内容校验 | 附加到上游请求的自定义头 |
| `enabled` | bool | 否 | 缺省 `true` | 账号开关 |

> **`name` 含 `/` 的后果**：创建本身不会被拒绝，但单段路由 `{name}` 无法匹配含 `/` 的名字——该账号创建后**无法再被 GET/PUT/DELETE 寻址**，只能整体重建数据清理。请避免。

**响应** `201`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `account` | Account View | 结构见 3.2；凭据脱敏 |

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `invalid_json` | 请求体非法、含未知字段 |
| 400 | `invalid_provider` | `provider_id` 未知 |
| 400 | `invalid_credential` | 凭据缺失、`kind` 不符或 `api_key` 为空 |
| 400 | `invalid_request` | `name` 空白；`base_url` 前缀非法 |
| 409 | `already_exists` | 同名账号已存在 |

**示例**：

```bash
curl -X POST http://localhost:8080/admin/accounts \
  -H "Authorization: Bearer $MSU_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"ds-1","provider_id":"deepseek","api_key":"sk-…"}'
```

### 5.4 查看账号

**使用场景**：查看账号详情；`model_count` 用于删除确认时向操作者提示级联删除的规模。

```
GET /admin/accounts/{name}
```

**路径参数**：

| 参数 | 类型 | 约束 | 含义 |
|---|---|---|---|
| `name` | string | 单段路由，匹配不含 `/` 的完整剩余路径段 | 账号名 |

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `account` | Account View | 结构见 3.2 |
| `model_count` | int | 该账号名下模型数，`0` 表示无；**查询模型数时若该账号已被并发删除，`model_count` 回 `0` 且 `account` 为最后一个读取形态的缓存快照**（低概率竞态，仅作提示用途） |

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 账号不存在 |

**示例**：

```bash
curl http://localhost:8080/admin/accounts/ds-1 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

```json
{
  "account": {
    "name": "ds-1",
    "provider_id": "deepseek",
    "credential": {"kind": "api_key", "api_key": "sk-1***last"},
    "base_url": "",
    "headers": {},
    "enabled": true,
    "created_at": "2026-09-16T01:18:46.614536431Z",
    "updated_at": "2026-09-16T01:18:46.614536431Z"
  },
  "model_count": 2
}
```

### 5.5 更新账号

**使用场景**：修改账号配置或**轮换 API key**——凭据只写不读，PUT 时省略凭据字段即保留原值，因此本接口是换钥的唯一途径。

```
PUT /admin/accounts/{name}
```

**路径参数**：同 5.4 的 `name`。

**请求体字段**：与 5.3 完全同名同类型（请求体中的 `name` 被忽略，以路径为准）。省略语义与创建不同：

| 字段 | 省略或为空时 |
|---|---|
| `provider_id` | **保留原值** |
| `credential` / `api_key`（两者都不给） | **保留原凭据** |
| `base_url` | 置空串（回落 provider 默认） |
| `headers` | 置 `{}` |
| `enabled` | 置 `true` |

> **`base_url`、`headers`、`enabled` 是替换语义**：只想改 `enabled` 时，须同时带上现有 `base_url` 与 `headers`，否则会被清空。

> **变更 `provider_id` 不回溯校验其名下模型**：模型的 `protocol` 只在创建/更新模型时校验。账号改挂到不支持该协议的 provider 后，resolve 仍会成功，请求将在上游被拒。

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `account` | Account View | 结构见 3.2 |

**副作用**：该账号的额度缓存与上游模型缓存立即失效，下次查询强制回源。

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 账号不存在 |
| 400 | `invalid_provider` / `invalid_credential` / `invalid_request` / `invalid_json` | 同 5.3 |

**示例**（关闭账号开关；因替换语义，须把当前有效的 `base_url` 与 `headers` 一并带回，否则会被清空）：

```bash
curl -X PUT http://localhost:8080/admin/accounts/ds-1 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"provider_id":"deepseek","base_url":"https://api.deepseek.com","headers":{},"enabled":false}'
```

**示例**（仅轮换 API key，其余字段按当前值原样带回）：

```bash
curl -X PUT http://localhost:8080/admin/accounts/ds-1 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"provider_id":"deepseek","api_key":"sk-new-…","base_url":"https://api.deepseek.com","headers":{},"enabled":true}'
```

### 5.6 删除账号

**使用场景**：下线账号。**单事务**内先删除该账号名下全部模型，再删除账号；名下有模型时须确认级联删除。

```
DELETE /admin/accounts/{name}
```

**路径参数**：同 5.4 的 `name`。

**请求**：无请求体。

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `deleted_models` | array of string | 被级联删除的模型 id；无模型时为 `[]` |

**副作用**：账号与其名下模型的缓存、额度缓存、上游模型缓存一并失效。

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 账号不存在 |

**示例**：

```bash
curl -X DELETE http://localhost:8080/admin/accounts/ds-1 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

```json
{ "deleted_models": ["ds-1/v4", "ds-1/v4-thinking"] }
```

### 5.7 查询账号额度

**使用场景**：向该账号所属上游查询余额 / 已用量 / 速率窗口，供管理客户端展示。上游的额度形态因家而异（预付费余额、后付费用量、订阅周期配额、滚动速率窗口），报告以一组计量项承载，见 3.6。管理面提供此接口是因为桌面客户端只持管理密钥。

```
GET /admin/accounts/{name}/quota
```

**路径参数**：同 5.4 的 `name`。

**请求**：无查询参数、无请求体。

**响应** `200`：QuotaReport，结构见 3.6。

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 账号不存在 |
| 400 | `invalid_provider` | 账号引用的 provider 已不在内置规格表（升级后规格变更） |
| 502 | `quota_unavailable` | 上游额度接口请求失败或返回非 2xx |

**缓存**：结果只在进程内存缓存，TTL 由 `MSU_QUOTA_TTL`（默认 60s）控制；账号更新或删除时立即失效。查询上游使用与 resolve 相同的认证头（3.5 规则）。

**示例**（DeepSeek 风格：余额 + 响应头速率窗口）：

```bash
curl http://localhost:8080/admin/accounts/ds-1/quota \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

```json
{
  "account": "ds-1",
  "queryable": true,
  "meters": [
    {
      "kind": "balance",
      "unit": "currency",
      "label": "CNY",
      "currency": "CNY",
      "remaining": 42.5,
      "reset": "prepaid"
    },
    {
      "kind": "rate_limit",
      "unit": "tokens",
      "label": "tokens",
      "remaining": 9000,
      "total": 10000,
      "reset": "rolling",
      "reset_at": "2026-09-17T02:01:00Z"
    }
  ],
  "at": "2026-09-17T02:00:00Z"
}
```

provider 未声明额度接口时（如除 deepseek 外的五家）：

```json
{ "account": "kimi-1", "queryable": false, "meters": [], "at": "2026-09-17T02:00:00Z" }
```

### 5.8 查询上游可用模型

**使用场景**：向该账号所属上游查询其实际可用的模型清单，与本地配置（5.9）比对——发现上游新增了模型、或本地配置引用了已下线的模型。provider 未声明列举端点（如 `ark`，实测各路径恒 `401`）时返回 `queryable: false`。

```
GET /admin/accounts/{name}/upstream-models
```

**路径参数**：同 5.4 的 `name`。

**请求**：无查询参数、无请求体。

**响应** `200`：UpstreamModelsReport，结构见 3.7。

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 账号不存在 |
| 400 | `invalid_provider` | 账号引用的 provider 已不在内置规格表 |
| 502 | `upstream_unavailable` | 上游列举接口请求失败或返回非 2xx |

**缓存**：与额度相同（`MSU_QUOTA_TTL`、更新/删除即失效、认证头同 resolve）。

**示例**：

```bash
curl http://localhost:8080/admin/accounts/ds-1/upstream-models \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

```json
{
  "account": "ds-1",
  "queryable": true,
  "models": [
    {"id": "deepseek-chat"},
    {"id": "deepseek-reasoner"}
  ],
  "at": "2026-09-17T01:19:48.567Z"
}
```

### 5.9 列举模型

**使用场景**：管理客户端的模型列表；可按账号过滤查看某账号名下配置。

```
GET /admin/models?account={name}
```

**查询参数**：

| 参数 | 类型 | 必填 | 约束 / 允许值 | 含义 |
|---|---|---|---|---|
| `account` | string | 否 | 精确匹配账号名 | 按账号过滤；缺省或空串 = 列出全部 |

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `models` | array of Model | 元素结构见 3.3；按 `id` 升序 |

> `account` 指向不存在的账号**不报错**，返回空数组 `[]`（过滤条件而非资源引用）。

**错误**：无领域错误。

**示例**：

```bash
curl "http://localhost:8080/admin/models?account=ds-1" \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

### 5.10 创建模型

**使用场景**：把一个上游模型暴露给调用方——指定它归属哪个账号、以什么协议出站、以及参数策略。

```
POST /admin/models
```

**请求体字段**：

| 字段 | 类型 | 必填 | 约束 / 允许值 | 含义 |
|---|---|---|---|---|
| `id` | string | 是 | 首尾空白剥除后非空；全局唯一，重名 `409 already_exists`；**可含 `/`**（如 `ds-1/v4`，见 5.11 路由说明） | 对外暴露的模型标识 |
| `account` | string | 是 | 首尾空白剥除后非空；须为已存在账号 | 归属账号 |
| `native_model` | string | 是 | 首尾空白剥除后非空 | 上游真实模型名 |
| `protocol` | 枚举 | 是 | 2.8 的 `protocol` 取值之一，且须被账号所属 provider 支持 | 出站协议 |
| `context_window` | int | 否 | ≥ 0；缺省或 `0` = 未声明 | 上下文窗口 token 数 |
| `defaults` | object | 否 | 须为 JSON 对象（数组/标量返回 `400 invalid_json`）；缺省或 `null` 落库为 `{}` | 缺省填充参数 |
| `overrides` | object | 否 | 同 `defaults` | 强制覆盖参数 |
| `enabled` | bool | 否 | 缺省 `true` | 模型开关 |

> `defaults` 与 `overrides` 原样存储、原样下发（3.5），**本服务不做参数合并**；调用方按 `defaults ← 自身请求参数 ← overrides` 叠加（对象递归合并、数组与标量整体替换）。互斥参数组（如 anthropic 的 thinking 相关字段）应在 overrides 中整组写全，避免深合并拼出上游拒收的组合。

**响应** `201`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `model` | Model | 结构见 3.3 |

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `invalid_json` | 请求体非法、含未知字段、`defaults`/`overrides` 不是 JSON 对象 |
| 400 | `invalid_request` | `id` / `account` / `native_model` 空白；`context_window` 为负 |
| 404 | `not_found` | 引用的账号不存在（预检失败，或写入时被并发删除触发外键约束） |
| 400 | `invalid_protocol` | `protocol` 不被账号所属 provider 支持，message 列出可选集 |
| 409 | `already_exists` | 同名模型已存在 |

**示例**：

```bash
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer $MSU_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "ds-1/v4",
    "account": "ds-1",
    "native_model": "deepseek-v4.1-flash",
    "protocol": "chat_completions",
    "context_window": 1000000,
    "defaults": {"temperature": 0.6, "top_p": 0.9},
    "overrides": {"max_tokens": 8192}
  }'
```

### 5.11 查看模型

**使用场景**：查看单个模型的完整配置。

```
GET /admin/models/{id...}
```

**路径参数**：

| 参数 | 类型 | 约束 | 含义 |
|---|---|---|---|
| `id` | string | **通配路由**：匹配含尾部全部剩余路径（可含多个 `/`） | 模型标识 |

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `model` | Model | 结构见 3.3 |

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 模型不存在 |

**示例**（模型 id 含 `/`，通配路由整段匹配）：

```bash
curl http://localhost:8080/admin/models/ds-1/v4 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

### 5.12 更新模型

**使用场景**：修改模型配置——换 `native_model`、调参数策略、迁移到别的账号。

```
PUT /admin/models/{id...}
```

**路径参数**：同 5.11 的 `id`（通配路由）。

**请求体字段**：与 5.10 完全同名同类型（请求体中的 `id` 被忽略，以路径为准）。省略语义：

| 字段 | 省略或为空时 |
|---|---|
| `account` / `native_model` / `protocol` | **保留原值** |
| `context_window` | 置 `0`（清除声明） |
| `defaults` / `overrides` | 置 `{}`（清除） |
| `enabled` | 置 `true`（重新启用） |

> **`context_window`、`defaults`、`overrides`、`enabled` 是替换语义**：只改其中一项时须把其余项一并带上。

> 迁移 `account` 时按**新账号**的 provider 校验 `protocol`；新账号不存在返回 `404 not_found`。

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `model` | Model | 结构见 3.3 |

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 模型不存在；或目标 `account` 不存在 |
| 400 | `invalid_protocol` | `protocol` 不被（新）账号所属 provider 支持 |
| 400 | `invalid_request` / `invalid_json` | 同 5.10 |

**示例**（只把 `max_tokens` 从 8192 调到 16384；因替换语义，`context_window`、`defaults`、`enabled` 等其余替换字段须一并带回当前值）：

```bash
curl -X PUT http://localhost:8080/admin/models/ds-1/v4 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "account": "ds-1",
    "native_model": "deepseek-v4.1-flash",
    "protocol": "chat_completions",
    "context_window": 1000000,
    "defaults": {"temperature": 0.6, "top_p": 0.9},
    "overrides": {"max_tokens": 16384},
    "enabled": true
  }'
```

### 5.13 删除模型

**使用场景**：下线一个对外暴露的模型标识。调用方此后 resolve 该 id 将得到 `404 not_found`。

```
DELETE /admin/models/{id...}
```

**路径参数**：同 5.11 的 `id`（通配路由）。

**请求**：无请求体。

**响应** `204 No Content`：无响应体。

**错误**：

| HTTP | code | 触发条件 |
|---|---|---|
| 404 | `not_found` | 模型不存在 |

**示例**：

```bash
curl -X DELETE http://localhost:8080/admin/models/ds-1/v4 \
  -H "Authorization: Bearer $MSU_ADMIN_KEY"
```

---

## 6. 下发面 API

以下接口均要求 `Authorization: Bearer <MSU_DELIVERY_KEY>`（见 2.2），全部**只读**。`401` / `500` 通用错误不再逐条重复。

### 6.1 列举可用模型

**使用场景**：调用方（数据面服务）启动或定期同步时发现可下发的模型清单——拿到 id、协议与上下文窗口声明，据此构建自己的路由表。只含启用中的模型信息，**不含任何凭据**。

```
GET /v1/models
```

**请求**：无路径参数、无查询参数、无请求体。

**响应** `200`：

| 字段 | 类型 | 取值与含义 |
|---|---|---|
| `models` | array of Listing | 元素结构见 3.4；按 `id` 升序；**含禁用模型**（`enabled: false`），是否跳过由调用方决定 |

**错误**：无领域错误。

**示例**：

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer $MSU_DELIVERY_KEY"
```

```json
{
  "models": [
    {
      "id": "ds-1/v4",
      "account": "ds-1",
      "provider_id": "deepseek",
      "protocol": "chat_completions",
      "native_model": "deepseek-v4.1-flash",
      "context_window": 1000000,
      "enabled": true
    }
  ]
}
```

### 6.2 解析目标

**使用场景**：调用方每次向上游发起请求前，把模型 id 解析为完整的请求目标——上游地址、**含真实凭据**的认证头、native 模型名与参数策略。这是数据面转发的核心依赖。

```
POST /v1/resolve
```

**请求体字段**：

| 字段 | 类型 | 必填 | 约束 / 允许值 | 含义 |
|---|---|---|---|---|
| `model_id` | string | 是 | 非空；未知模型返回 `404 not_found` | 待解析的模型标识，须与 6.1 中的 `id` 一致 |

**响应** `200`：ResolvedTarget，结构见 3.5。

**错误**（按判定顺序，命中即返回）：

| 顺序 | 条件 | HTTP | code |
|---|---|---|---|
| 1 | 模型不存在 | 404 | `not_found` |
| 2 | 模型被禁用 | 409 | `model_disabled` |
| 3 | 账号不存在 | 404 | `not_found` |
| 4 | 账号被禁用 | 409 | `account_disabled` |
| 5 | 账号引用的 provider 不在规格表 | 400 | `invalid_provider` |

**示例**：

```bash
curl -X POST http://localhost:8080/v1/resolve \
  -H "Authorization: Bearer $MSU_DELIVERY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model_id": "ds-1/v4"}'
```

```json
{
  "model_id": "ds-1/v4",
  "account": "ds-1",
  "provider_id": "deepseek",
  "protocol": "chat_completions",
  "base_url": "https://api.deepseek.com",
  "native_model": "deepseek-v4.1-flash",
  "context_window": 1000000,
  "headers": {
    "Authorization": "Bearer sk-…"
  },
  "defaults": {"temperature": 0.6, "top_p": 0.9},
  "overrides": {"max_tokens": 8192}
}
```

调用方拿到 ResolvedTarget 后，自行以 `base_url + headers + native_model` 构造对上游的请求，并按 `defaults ← 自身请求参数 ← overrides` 叠加请求参数。

### 6.3 查询账号额度

**使用场景**：数据面运行时读取账号余额 / 速率窗口，用于调度决策（如额度耗尽时切换账号）。

```
GET /v1/accounts/{name}/quota
```

**路径参数**：

| 参数 | 类型 | 约束 | 含义 |
|---|---|---|---|
| `name` | string | 单段路由 | 账号名 |

请求、响应、错误、缓存行为与 5.7 **完全一致**（同一实现）。下发面提供此接口供数据面运行时使用。

**示例**：

```bash
curl http://localhost:8080/v1/accounts/ds-1/quota \
  -H "Authorization: Bearer $MSU_DELIVERY_KEY"
```

### 6.4 查询上游可用模型

**使用场景**：调用方在运行时核对某账号在上游的真实可用模型（如上游下线了模型时提前感知，避免反复撞 404）。

```
GET /v1/accounts/{name}/upstream-models
```

**路径参数**：同 6.3 的 `name`。

请求、响应、错误、缓存行为与 5.8 **完全一致**（同一实现）。

**示例**：

```bash
curl http://localhost:8080/v1/accounts/ds-1/upstream-models \
  -H "Authorization: Bearer $MSU_DELIVERY_KEY"
```

---

## 7. 快速上手

```bash
# 0. 起栈（密钥必须显式传入，无默认值）
MSU_ADMIN_KEY=admin-key MSU_DELIVERY_KEY=delivery-key docker compose up -d

# 1. 看有哪些内置 provider
curl http://localhost:8080/admin/providers \
  -H "Authorization: Bearer admin-key"

# 2. 建账号
curl -X POST http://localhost:8080/admin/accounts \
  -H "Authorization: Bearer admin-key" -H "Content-Type: application/json" \
  -d '{"name":"ds-1","provider_id":"deepseek","api_key":"sk-…"}'

# 3. 建模型
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer admin-key" -H "Content-Type: application/json" \
  -d '{"id":"ds-1/v4","account":"ds-1","native_model":"deepseek-v4.1-flash","protocol":"chat_completions","context_window":1000000,"defaults":{"temperature":0.6},"overrides":{"max_tokens":8192}}'

# 4. 调用方解析目标（数据面每次转发前调用）
curl -X POST http://localhost:8080/v1/resolve \
  -H "Authorization: Bearer delivery-key" -H "Content-Type: application/json" \
  -d '{"model_id": "ds-1/v4"}'
```

调用方拿到 ResolvedTarget 后，自行以 `base_url + headers + native_model` 构造对上游的请求，并按 `defaults ← 自身请求参数 ← overrides` 叠加请求参数。

---

## 附录 A. 环境变量

| 变量 | 必填 | 默认值 | 含义 |
|---|---|---|---|
| `MSU_PG_DSN` | 是 | — | PostgreSQL 连接串，如 `postgres://msu:msu@localhost:5432/msu` |
| `MSU_ADMIN_KEY` | 是 | — | 管理面密钥；**无默认值**，缺失时进程拒绝启动 |
| `MSU_DELIVERY_KEY` | 是 | — | 下发面密钥；**无默认值**，缺失时进程拒绝启动 |
| `MSU_LISTEN` | 否 | `:8080` | HTTP 监听地址 |
| `MSU_REDIS_ADDR` | 否 | — | Redis 地址；缺省 = 不启用缓存（`/healthz` 的 `cache` 恒 `false`，读写直连 PG） |
| `MSU_CACHE_TTL` | 否 | `5m` | 账号/模型缓存 TTL |
| `MSU_QUOTA_TTL` | 否 | `60s` | 额度与上游模型清单缓存 TTL |
