package provider

// 内置规格在单个 init 中按固定顺序注册，保证 All() 顺序稳定。
// ID 命名:厂商[-区域]/服务——Global 区域省略,按量计费开放 API 一律标
// /api,订阅服务标套餐 slug(codex/token-plan/coding-plan);kiro 无可命名
// 套餐,保留裸 id。
func init() {
	register(Spec{
		ID:          "anthropic/api",
		DisplayName: "Anthropic",
		Website:     "https://console.anthropic.com",
		BaseURL:     "https://api.anthropic.com",
		Protocols:   []string{ProtocolAnthropic},
		Auth:        AuthAnthropicKey,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Plan:        PlanStandard,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	register(Spec{
		ID:          "openai/api",
		DisplayName: "OpenAI",
		Website:     "https://platform.openai.com",
		BaseURL:     "https://api.openai.com",
		Protocols:   []string{ProtocolChatCompletions, ProtocolResponses},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Plan:        PlanStandard,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	register(Spec{
		ID:          "gemini/api",
		DisplayName: "Google Gemini",
		Website:     "https://aistudio.google.com",
		BaseURL:     "https://generativelanguage.googleapis.com",
		Protocols:   []string{ProtocolGemini, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Plan:        PlanStandard,
		Models:      &ModelsAPI{Path: "/v1beta/models", Method: "GET"},
	})
	// ChatGPT 订阅登录态:走 backend-api 的 codex 端点,只支持
	// Responses 协议;凭据是 OAuth 刷新型,请求整形(强制 store/stream/
	// instructions 与额外头)在 resolve/proxyplane 按本 provider 收口。
	// /models 清单端点存在(sub2api 同款硬编码路径),按模型携带
	// supported_reasoning_levels 声明——推理档动态适配的数据源。
	// Plus/Pro 只是订阅档位、服务类型相同,不拆变体。
	register(Spec{
		ID:          "openai/codex",
		DisplayName: "OpenAI",
		Website:     "https://chatgpt.com",
		BaseURL:     "https://chatgpt.com/backend-api/codex",
		Protocols:   []string{ProtocolResponses},
		Auth:        AuthBearer,
		Credential:  CredOAuthRefresh,
		Billing:     BillingSubscription,
		Region:      RegionGlobal,
		Plan:        PlanStandard,
		Models:      &ModelsAPI{Path: "/models", Method: "GET"},
		// 额度走 backend-api/wham/usage(不在 codex 子路径下,内置实现写
		// 绝对地址),回 5 小时/周两个滚动窗的已用百分比。
		QuotaQueryable: true,
	})
	register(Spec{
		ID:          "kimi/coding",
		DisplayName: "Moonshot Kimi",
		Website:     "https://www.kimi.com",
		// Kimi For Coding 订阅端点在 api.kimi.com;api.moonshot.cn 没有
		// /coding 路由(恒 404 url.not_found),/anthropic 只认平台密钥。
		BaseURL:    "https://api.kimi.com/coding",
		Protocols:  []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:       AuthAnthropicKey,
		Credential: CredAPIKey,
		Billing:    BillingSubscription,
		Region:     RegionGlobal,
		Plan:       PlanStandard,
		Models:     &ModelsAPI{Path: "/v1/models", Method: "GET"},
		// 5 小时/7 天窗走 API key;网页 refresh_token 另补会员月度额度。
		QuotaQueryable: true,
	})
	// ark 的 responses 端点实测不可用，故只声明两个协议；其模型列举端点
	// 实测各路径恒回 401，故不声明 Models——查不到比查错了好。
	register(Spec{
		ID:          "ark/api",
		DisplayName: "Volcengine Ark",
		Website:     "https://console.volcengine.com/ark",
		BaseURL:     "https://ark.cn-beijing.volces.com/api/v3",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Plan:        PlanStandard,
	})
	register(Spec{
		ID:             "deepseek/api",
		DisplayName:    "DeepSeek",
		Website:        "https://platform.deepseek.com",
		BaseURL:        "https://api.deepseek.com",
		Protocols:      []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:           AuthBearer,
		Credential:     CredAPIKey,
		Billing:        BillingPayGo,
		Region:         RegionGlobal,
		Plan:           PlanStandard,
		QuotaQueryable: true,
		Models:         &ModelsAPI{Path: "/models", Method: "GET"},
	})
	register(Spec{
		ID:             "kiro",
		DisplayName:    "Kiro",
		Website:        "https://kiro.dev",
		BaseURL:        "https://runtime.us-east-1.kiro.dev",
		Protocols:      []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:           AuthBearer,
		Credential:     CredKiroRefresh,
		Billing:        BillingSubscription,
		Region:         RegionGlobal,
		Plan:           PlanStandard,
		Models:         &ModelsAPI{Path: "/ListAvailableModels", Method: "GET"},
		QuotaQueryable: true,
	})
	register(Spec{
		ID:             "bailian-cn/token-plan",
		DisplayName:    "Aliyun Bailian",
		Website:        "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal",
		BaseURL:        "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode",
		Protocols:      []string{ProtocolChatCompletions, ProtocolResponses},
		Auth:           AuthBearer,
		Credential:     CredAPIKey,
		Billing:        BillingSubscription,
		Region:         RegionCN,
		Plan:           "Token Plan",
		Models:         &ModelsAPI{Path: "/v1/models", Method: "GET"},
		QuotaQueryable: true,
	})
	// 百炼 Coding Plan(华北2,sk-sp- 专属密钥,与 Token Plan 密钥不通用):
	// 探针实测 chat completions 与 models 存在、responses 恒 404,故只声明
	// chat_completions;Anthropic 兼容在 /apps/anthropic 独立路径,单 BaseURL
	// 规格表达不了,不接入。套餐额度按请求数计(月 9 万次),控制台无开放
	// 查询 API,不声明 QuotaQueryable。
	register(Spec{
		ID:          "bailian-cn/coding-plan",
		DisplayName: "Aliyun Bailian",
		Website:     "https://bailian.console.aliyun.com/cn-beijing/subscription/coding-plan/personal",
		BaseURL:     "https://coding.dashscope.aliyuncs.com",
		Protocols:   []string{ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingSubscription,
		Region:      RegionCN,
		Plan:        "Coding Plan",
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
}
