package provider

// 内置规格在单个 init 中按固定顺序注册，保证 All() 顺序稳定。
func init() {
	register(Spec{
		ID:          "anthropic",
		DisplayName: "Anthropic",
		Website:     "https://www.anthropic.com",
		BaseURL:     "https://api.anthropic.com",
		Protocols:   []string{ProtocolAnthropic},
		Auth:        AuthAnthropicKey,
		Credential:  CredAPIKey,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	register(Spec{
		ID:          "openai",
		DisplayName: "OpenAI",
		Website:     "https://openai.com",
		BaseURL:     "https://api.openai.com",
		Protocols:   []string{ProtocolChatCompletions, ProtocolResponses},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	register(Spec{
		ID:          "gemini",
		DisplayName: "Google Gemini",
		Website:     "https://ai.google.dev",
		BaseURL:     "https://generativelanguage.googleapis.com",
		Protocols:   []string{ProtocolGemini, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Models:      &ModelsAPI{Path: "/v1beta/models", Method: "GET"},
	})
	register(Spec{
		ID:          "kimi",
		DisplayName: "Moonshot Kimi",
		Website:     "https://platform.moonshot.cn",
		BaseURL:     "https://api.moonshot.cn/coding",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthAnthropicKey,
		Credential:  CredAPIKey,
		Models:      &ModelsAPI{Path: "/v1/models", Method: "GET"},
	})
	// ark 的 responses 端点实测不可用，故只声明两个协议；其模型列举端点
	// 实测各路径恒回 401，故不声明 Models——查不到比查错了好。
	register(Spec{
		ID:          "ark",
		DisplayName: "Volcengine Ark",
		Website:     "https://www.volcengine.com/product/ark",
		BaseURL:     "https://ark.cn-beijing.volces.com/api/v3",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
	})
	register(Spec{
		ID:          "deepseek",
		DisplayName: "DeepSeek",
		Website:     "https://platform.deepseek.com",
		BaseURL:     "https://api.deepseek.com",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
		Quota: &QuotaAPI{
			Path:   "/user/balance",
			Method: "GET",
			Kind:   MeterBalance,
			Unit:   UnitCurrency,
			Reset:  ResetPrepaid,
		},
		Models: &ModelsAPI{Path: "/models", Method: "GET"},
	})
}
