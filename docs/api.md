# ModelSurge Upstream API 参考

上游「配置中心 + 凭据下发」服务的 HTTP 接口规范。

- **文档版本**：1.2（2026-09-17）
- **对应实现**：`backend/httpapi`（`0877137` 下发面拆分 defaults 与 overrides，之上新增上游可用模型查询；额度报告改为多形态计量项列表——相对 1.1 为破坏性变更）
- **服务定位**：管理上游账号、模型与参数配置；对调用方下发请求目标（地址、认证头、参数策略）。**不转发数据面聊天流量，不承载调度运行时状态。**

---

## 1. 概述

服务暴露两组接口与一个健康检查：

| 分组 | 前缀 | 密钥 | 用途 |
|---|---|---|---|
| 健康检查 | `/healthz` | 无 | 依赖就绪状态 |
| 管理面 | `/admin/*` | `MSU_ADMIN_KEY` | 配置的增删改查（桌面客户端使用） |
| 下发面 | `/v1/*` | `MSU_DELIVERY_KEY` | 只读的目标解析（调用方使用） |

两把密钥相互独立：管理密钥不能访问下发面，反之亦然。

核心概念为三层：

- **provider**：内置提供商规格，编译期常量，运行期不可修改；
- **account**：某提供商下的一个具体账号（凭据 + 可选端点覆盖）；
- **model**：对外暴露的模型标识，归属某账号，声明协议与参数策略（`defaults`/`overrides`）。

---

## 2. 通用约定

### 2.1 基础地址

```
http://<host>:<port>
```

默认监听 `:8080`（环境变量 `MSU_LISTEN`）。

### 2.2 认证

除 `GET /healthz` 外，所有接口要求请求头：

```
Authorization: Bearer <密钥>
```

- 前缀 `Bearer` 大小写不敏感；
- 校验为常数时间比较；
- 密钥缺失或不正确返回 `401` `unauthorized`。

### 2.3 请求体

- 写接口（POST/PUT）请求体必须为 JSON 对象；
- 请求体**禁止未知字段**：出现未声明的字段名直接返回 `400` `invalid_json`，以此尽早暴露拼写错误；
- 空白字符串与字段缺省的区别在各接口的语义表中说明。

### 2.4 时间格式

所有时间字段为 RFC 3339 / UTC，例如 `2026-09-16T01:18:46.614536431Z`。

### 2.5 错误响应

所有非 2xx 响应为统一信封：

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
| `error.code` | string | 机器可读错误码，见 2.6 |
| `error.message` | string | 人类可读描述；非领域错误只回 `internal error`，不泄露底层细节（如连接串） |
| `error.status` | int | 与 HTTP 状态码一致 |

### 2.6 错误码

| code | HTTP | 触发场景 |
|---|---|---|
| `unauthorized` | 401 | 密钥缺失或错误 |
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

---

## 3. 数据模型

### 3.1 Provider（内置规格）

`GET /admin/providers` 返回的元素：

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | 稳定标识 |
| `display_name` | string | 显示名 |
| `website` | string | 官网地址 |
| `base_url` | string | 默认根地址（账号未覆盖时使用） |
| `protocols` | []string | 支持的协议，取值见 3.3 的 `protocol` |
| `auth` | string | 认证头形态：`bearer` 或 `anthropic_key` |
| `credential` | string | 凭据形态，本期恒为 `api_key` |
| `quota` | object \| 缺省 | 额度接口声明 `{path, method, kind, unit, reset}`，未声明则字段缺省。声明了 `quota` 时 `kind` 与 `unit` 必填（注册期缺失直接 panic），二者为额度解析结果的兜底语义，取值见 3.6 |
| `models` | object \| 缺省 | 上游模型列举接口声明 `{path, method}`，未声明则字段缺省（该上游无可用列举端点） |

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

| 字段 | 类型 | 说明 |
|---|---|---|
| `name` | string | 账号唯一标识 |
| `provider_id` | string | 所属提供商 |
| `credential.kind` | string | 凭据形态，`api_key` |
| `credential.api_key` | string | **脱敏值**：空串回空串；长度 ≤ 8 全掩为 `***`；否则前 4 位 + `***` + 后 4 位 |
| `base_url` | string | 端点覆盖；空串表示沿用 provider 默认 |
| `headers` | map[string]string | 附加到上游请求的自定义头 |
| `enabled` | bool | 禁用后其下模型在 resolve 中不可用 |
| `created_at` | time | |
| `updated_at` | time | |

### 3.3 Model（模型）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | 模型唯一标识，**可含 `/`**（如 `ds-1/v4`） |
| `account` | string | 归属账号；账号删除时级联删除模型 |
| `native_model` | string | 上游真实模型名 |
| `protocol` | string | `anthropic` / `chat_completions` / `responses` / `gemini`，须被账号 provider 支持 |
| `context_window` | int | 上下文窗口声明；`0` 表示未声明 |
| `defaults` | object | 缺省填充参数：调用方缺什么补什么 |
| `overrides` | object | 强制覆盖参数：无论调用方给什么都压盖 |
| `enabled` | bool | |
| `created_at` | time | |
| `updated_at` | time | |

### 3.4 Listing（下发面模型清单元素）

不含任何凭据：

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | |
| `account` | string | |
| `provider_id` | string | 由账号推导 |
| `protocol` | string | |
| `native_model` | string | |
| `context_window` | int \| 缺省 | `0` 时字段缺省 |
| `enabled` | bool | `model.enabled && account.enabled`，账号停用会使其下全部模型不可用 |

### 3.5 ResolvedTarget（解析结果）

`POST /v1/resolve` 的响应体：

| 字段 | 类型 | 说明 |
|---|---|---|
| `model_id` | string | 回显请求的模型标识 |
| `account` | string | |
| `provider_id` | string | |
| `protocol` | string | |
| `base_url` | string | 账号 `base_url` 优先，空则 provider 默认 |
| `native_model` | string | |
| `context_window` | int \| 缺省 | `0` 时字段缺省 |
| `headers` | map[string]string | **含真实凭据**的认证头 + 账号自定义头 |
| `defaults` | object | 原样下发，不与服务端任何参数预合并 |
| `overrides` | object | 同上 |

`headers` 的构成规则：

1. 按 provider 的 `auth` 生成认证头：
   - `bearer`：`Authorization: Bearer <api_key>`
   - `anthropic_key`：`x-api-key: <api_key>` 与 `anthropic-version: 2023-06-01`
2. 账号 `headers` **最后叠加**，可覆盖上述任何头（用于自建网关等场景）。

**参数合并职责在调用方**：调用方应以 `defaults ← 自身请求参数 ← overrides` 的顺序叠加（对象递归合并，数组与标量整体替换）。服务端不预合并，因为 defaults 与 overrides 语义不同，合并后无法区分。

### 3.6 QuotaReport（额度报告）

| 字段 | 类型 | 说明 |
|---|---|---|
| `account` | string | |
| `queryable` | bool | `false` 表示该 provider 未声明额度接口，**属正常答案而非错误**，此时 `meters` 为空数组 |
| `meters` | array of Meter | 计量项列表，可能为空数组（端点通了但响应中无可识别字段） |
| `at` | time | 本次查询时刻 |

**为什么是列表而非单值**：上游额度有四类形态——预付费余额（充值才涨）、后付费已用量（没有"余量"可言）、订阅周期配额（关键信息是下次重置时刻）、滚动速率窗口（`requests` 与 `tokens` 是并存的独立计数，各有各的余量与重置）。单值结构只能表达第一类，因此报告承载一组计量项，单值形态退化成只有一项。

**Meter（计量项）**

| 字段 | 类型 | 说明 |
|---|---|---|
| `kind` | string | 形态：`balance`（预付费余额）/ `usage`（后付费已用量）/ `rate_limit`（滚动速率窗口） |
| `unit` | string | 单位：`currency` / `requests` / `tokens` / `credits`。数值本身说不出自己是钱还是请求数，故必填 |
| `label` | string \| 缺省 | 给人看的维度名，如币种 `CNY`、速率维度 `tokens` |
| `currency` | string \| 缺省 | 币种，仅 `unit` 为 `currency` 时有意义 |
| `remaining` | number \| 缺省 | 余量；解析不出则缺省 |
| `total` | number \| 缺省 | 总量或上限 |
| `used` | number \| 缺省 | 已用量。后付费形态往往只有此项 |
| `reset` | string \| 缺省 | 重置规律：`none` / `rolling` / `daily` / `monthly` / `prepaid` |
| `reset_at` | time \| 缺省 | 下次重置的绝对时刻，上游给了才有。周期配额与速率窗口下这比静态的 `reset` 规律更有用 |

**数值来源**：响应体内的计量项，其 `kind`/`unit`/`reset` 以 provider 规格中的额度声明（见 3.1）兜底，上游响应自带更精确信息时以响应为准；速率窗口维度来自响应头 `x-ratelimit-{remaining,limit,reset}-{requests,tokens}`，重置时刻同时接受 RFC3339 与 Unix 秒两种写法。认不出的字段一律不猜——只报 `queryable: true` 而不产出计量项。

额度报告只在进程内存缓存（带 TTL，默认 60s，`MSU_QUOTA_TTL`），不落库；账号更新或删除时缓存立即失效。查询上游使用与 resolve 相同的认证头。

### 3.7 UpstreamModelsReport（上游模型清单）

| 字段 | 类型 | 说明 |
|---|---|---|
| `account` | string | |
| `queryable` | bool | `false` 表示该 provider 未声明列举接口，**属正常答案而非错误**，此时 `models` 为空数组 |
| `models` | []Entry | 上游返回的模型条目，按 `id` 升序排列；始终为数组，不会是 `null` |
| `at` | time | 本次查询时刻 |

Entry：

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | 上游模型标识 |
| `display_name` | string \| 缺省 | 显示名，上游未提供时缺省 |

解析行为：兼容 `data`（OpenAI/Anthropic/DeepSeek）与 `models`（Gemini）两种外层键，条目标识依次尝试 `id`、`name`、`model`；重复标识折叠，无标识的条目跳过；外层结构无法识别时返回空数组而不编造条目。

与额度一致：只在进程内存缓存（TTL 复用 `MSU_QUOTA_TTL`，默认 60s），不落 PostgreSQL 也不进 Redis；账号更新或删除时缓存立即失效；请求上游使用与 resolve 相同的认证头。

> 该接口与 `GET /v1/models` 的区别：本接口回答「上游账号实际能用哪些模型」（上游事实），`/v1/models` 回答「本服务已配置哪些模型」（本地配置）。两者互补，可用于核对配置是否与上游现状一致。

---

## 4. 健康检查

### GET /healthz

免鉴权。

**响应** `200`：

```json
{ "ready": true, "database": true, "cache": true }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `ready` | bool | 服务可用性，等于 `database` |
| `database` | bool | PostgreSQL 可达性 |
| `cache` | bool | Redis 可达性；**`false` 不影响 `ready`**——缓存仅是加速，故障时降级读 PG |

PostgreSQL 不可达时返回 `503`，字段同上。

---

## 5. 管理面 API

以下接口均要求 `Authorization: Bearer <MSU_ADMIN_KEY>`。

### 5.1 列举 provider

```
GET /admin/providers
```

**响应** `200`：

```json
{ "providers": [ Provider ] }
```

元素结构见 3.1，按固定注册序排列。

### 5.2 列举账号

```
GET /admin/accounts
```

**响应** `200`：

```json
{ "accounts": [ Account View ] }
```

### 5.3 创建账号

```
POST /admin/accounts
```

**请求体**：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `name` | string | 是 | 唯一标识；首尾空白在落库前剥除 |
| `provider_id` | string | 是 | 须为 3.1 表中的 id |
| `api_key` | string | 与 `credential` 二选一 | 凭据简写，等价于 `credential: {"kind":"api_key","api_key":…}` |
| `credential` | object | 与 `api_key` 二选一 | `{"kind":"api_key","api_key":"…"}`；`kind` 须与 provider 声明一致 |
| `base_url` | string | 否 | 覆盖 provider 默认根地址；须以 `http://` 或 `https://` 开头，尾部 `/` 落库前剥除 |
| `headers` | map[string]string | 否 | 缺省落库为 `{}` |
| `enabled` | bool | 否 | 缺省 `true` |

**响应** `201`：`{"account": Account View}`（凭据脱敏）。

**错误**：`invalid_provider`、`invalid_credential`、`invalid_request`、`already_exists`。

**示例**：

```bash
curl -X POST http://localhost:8080/admin/accounts \
  -H "Authorization: Bearer $MSU_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"ds-1","provider_id":"deepseek","api_key":"sk-…"}'
```

### 5.4 查看账号

```
GET /admin/accounts/{name}
```

**响应** `200`：

```json
{ "account": Account View, "model_count": 3 }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `model_count` | int | 该账号名下模型数，删除确认时提示级联规模 |

### 5.5 更新账号

```
PUT /admin/accounts/{name}
```

**请求体**与创建相同（`name` 以路径为准，请求体中的 `name` 被忽略）。字段省略语义：

| 字段 | 省略或为空时 |
|---|---|
| `provider_id` | 保留原值 |
| `credential` / `api_key`（两者都不给） | **保留原凭据**——凭据只写不读，这是换钥的唯一途径 |
| `base_url` | 置空（回落 provider 默认） |
| `headers` | 置空 `{}` |
| `enabled` | 置 `true` |

> 注意：`base_url`、`headers`、`enabled` 是**替换语义**。只想改 `enabled` 时，须同时带上现有 `base_url` 与 `headers`。

**响应** `200`：`{"account": Account View}`。副作用：该账号的额度缓存立即失效。

**错误**：`not_found`（账号不存在）及创建同款校验错误。

### 5.6 删除账号

```
DELETE /admin/accounts/{name}
```

单个事务内先删除该账号名下全部模型，再删除账号。

**响应** `200`：

```json
{ "deleted_models": ["ds-1/v4", "ds-1/v4-thinking"] }
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `deleted_models` | []string | 被级联删除的模型 id；无模型时为 `[]` |

### 5.7 查询账号额度

```
GET /admin/accounts/{name}/quota
```

透传上游额度接口并缓存（见 3.6）。管理面提供此接口是因为桌面客户端只持管理密钥。

**响应** `200`：QuotaReport。

**错误**：`not_found`（账号不存在）、`invalid_provider`（账号引用的 provider 已不在规格表）、`quota_unavailable`（上游请求失败或非 2xx，502）。

**示例**：

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

### 5.8 查询上游可用模型

```
GET /admin/accounts/{name}/upstream-models
```

向该账号所属上游查询其实际可用的模型清单（见 3.7）。用于核对本地配置是否与上游现状一致，例如上游新增或下线了模型。

**响应** `200`：UpstreamModelsReport。

**错误**：`not_found`（账号不存在）、`invalid_provider`（账号引用的 provider 已不在规格表）、`upstream_unavailable`（上游请求失败或返回非 2xx，502）。

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

provider 未声明列举接口时（如 `ark`）：

```json
{ "account": "ark-1", "queryable": false, "models": [], "at": "2026-09-17T01:19:46.339Z" }
```

### 5.9 列举模型

```
GET /admin/models?account={name}
```

| 查询参数 | 必填 | 说明 |
|---|---|---|
| `account` | 否 | 按账号过滤 |

**响应** `200`：`{"models": [ Model ]}`，结构见 3.3。

### 5.10 创建模型

```
POST /admin/models
```

**请求体**：字段与 3.3 的 Model 同名（`id` 必填，`enabled` 缺省 `true`）。校验规则：

- `id`、`account`、`native_model` 必填（首尾空白剥除后非空）；
- `account` 须为已存在账号，否则 `404` `not_found`（message: `referenced account does not exist`）；
- `context_window` ≥ 0；
- `protocol` 须被账号所属 provider 支持，错误消息会列出可选集；
- `defaults`、`overrides` 须为 JSON 对象（数组/标量返回 `400`）；缺省或 `null` 落库为 `{}`。

**响应** `201`：`{"model": Model}`。

**错误**：`invalid_request`、`not_found`、`invalid_protocol`、`invalid_json`、`already_exists`。

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

```
GET /admin/models/{id}
```

路径段 `{id}` 为通配匹配，模型 id 中的 `/` 不会切分路径（如 `GET /admin/models/ds-1/v4`）。

**响应** `200`：`{"model": Model}`。

### 5.12 更新模型

```
PUT /admin/models/{id}
```

**请求体**与创建相同（`id` 以路径为准）。字段省略语义：

| 字段 | 省略或为空时 |
|---|---|
| `account` / `native_model` / `protocol` | 保留原值 |
| `context_window` | 置 `0`（清除声明） |
| `defaults` / `overrides` | 置 `{}`（清除） |
| `enabled` | 置 `true` |

> 注意：`context_window`、`defaults`、`overrides`、`enabled` 是**替换语义**；只改其中一项时须把其余项一并带上。

**响应** `200`：`{"model": Model}`。

### 5.13 删除模型

```
DELETE /admin/models/{id}
```

**响应** `204 No Content`（无响应体）。

---

## 6. 下发面 API

以下接口均要求 `Authorization: Bearer <MSU_DELIVERY_KEY>`，全部只读。

### 6.1 列举可用模型

```
GET /v1/models
```

**响应** `200`：

```json
{ "models": [ Listing ] }
```

元素结构见 3.4。`enabled` 综合了模型与账号两级开关。

### 6.2 解析目标

```
POST /v1/resolve
```

**请求体**：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `model_id` | string | 是 | 待解析的模型标识 |

**响应** `200`：ResolvedTarget，见 3.5。

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

**错误**（按判定顺序）：

| 顺序 | 条件 | 错误 |
|---|---|---|
| 1 | 模型不存在 | `404` `not_found` |
| 2 | 模型被禁用 | `409` `model_disabled` |
| 3 | 账号不存在 | `404` `not_found` |
| 4 | 账号被禁用 | `409` `account_disabled` |
| 5 | 账号引用的 provider 不在规格表 | `400` `invalid_provider` |

### 6.3 查询账号额度

```
GET /v1/accounts/{name}/quota
```

响应与错误与 5.7 完全一致（同一实现）。下发面提供此接口供数据面运行时使用。

### 6.4 查询上游可用模型

```
GET /v1/accounts/{name}/upstream-models
```

响应与错误与 5.8 完全一致（同一实现）。下发面提供此接口，供调用方在运行时核对上游模型可用性。

---

## 7. 快速上手

```bash
# 1. 起栈（密钥必须显式传入）
MSU_ADMIN_KEY=admin-key MSU_DELIVERY_KEY=delivery-key docker compose up -d

# 2. 建账号
curl -X POST http://localhost:8080/admin/accounts \
  -H "Authorization: Bearer admin-key" -H "Content-Type: application/json" \
  -d '{"name":"ds-1","provider_id":"deepseek","api_key":"sk-…"}'

# 3. 建模型
curl -X POST http://localhost:8080/admin/models \
  -H "Authorization: Bearer admin-key" -H "Content-Type: application/json" \
  -d '{"id":"ds-1/v4","account":"ds-1","native_model":"deepseek-v4.1-flash","protocol":"chat_completions","context_window":1000000,"defaults":{"temperature":0.6},"overrides":{"max_tokens":8192}}'

# 4. 调用方解析目标
curl -X POST http://localhost:8080/v1/resolve \
  -H "Authorization: Bearer delivery-key" -H "Content-Type: application/json" \
  -d '{"model_id":"ds-1/v4"}'
```

调用方拿到 ResolvedTarget 后，自行以 `base_url + headers + native_model` 构造对上游的请求，并按 `defaults ← 自身参数 ← overrides` 叠加请求参数。
