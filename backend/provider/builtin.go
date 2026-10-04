package provider

// 内置规格在单个 init 中按固定顺序注册，保证 All() 顺序稳定。
func init() {
	register(Spec{
		ID:          "anthropic",
		DisplayName: "Anthropic",
		Website:     "https://console.anthropic.com",
		BaseURL:     "https://api.anthropic.com",
		Protocols:   []string{ProtocolAnthropic},
		Auth:        AuthAnthropicKey,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	register(Spec{
		ID:          "openai",
		DisplayName: "OpenAI",
		Website:     "https://platform.openai.com",
		BaseURL:     "https://api.openai.com",
		Protocols:   []string{ProtocolChatCompletions, ProtocolResponses},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	register(Spec{
		ID:          "gemini",
		DisplayName: "Google Gemini",
		Website:     "https://aistudio.google.com",
		BaseURL:     "https://generativelanguage.googleapis.com",
		Protocols:   []string{ProtocolGemini, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Models:      &ModelsAPI{Path: "/v1beta/models", Method: "GET"},
	})
	// ChatGPT 订阅(Plus/Pro 登录态):走 backend-api 的 codex 端点,只支持
	// Responses 协议;凭据是 OAuth 刷新型,请求整形(强制 store/stream/
	// instructions 与额外头)在 resolve/proxyplane 按本 provider 收口。
	// /models 清单端点存在(sub2api 同款硬编码路径),按模型携带
	// supported_reasoning_levels 声明——推理档动态适配的数据源。
	register(Spec{
		ID:          "openai-codex",
		DisplayName: "OpenAI",
		Website:     "https://chatgpt.com",
		BaseURL:     "https://chatgpt.com/backend-api/codex",
		Protocols:   []string{ProtocolResponses},
		Auth:        AuthBearer,
		Credential:  CredOAuthRefresh,
		Billing:     BillingSubscription,
		Region:      RegionGlobal,
		Models:      &ModelsAPI{Path: "/models", Method: "GET"},
	})
	register(Spec{
		ID:          "kimi",
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
		Models:     &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	// ark 的 responses 端点实测不可用，故只声明两个协议；其模型列举端点
	// 实测各路径恒回 401，故不声明 Models——查不到比查错了好。
	register(Spec{
		ID:          "ark",
		DisplayName: "Volcengine Ark",
		Website:     "https://console.volcengine.com/ark",
		BaseURL:     "https://ark.cn-beijing.volces.com/api/v3",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
	})
	register(Spec{
		ID:          "deepseek",
		DisplayName: "DeepSeek",
		Website:     "https://platform.deepseek.com",
		BaseURL:     "https://api.deepseek.com",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingPayGo,
		Region:      RegionGlobal,
		Quota: &QuotaAPI{
			Path:   "/user/balance",
			Method: "GET",
			Kind:   MeterBalance,
			Unit:   UnitCurrency,
			Reset:  ResetPrepaid,
		},
		Models: &ModelsAPI{Path: "/models", Method: "GET"},
	})
	register(Spec{
		ID:          "bailian",
		DisplayName: "Aliyun Bailian",
		Website:     "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal",
		BaseURL:     "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode",
		Protocols:   []string{ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Billing:     BillingSubscription,
		Region:      RegionCN,
	})
}
