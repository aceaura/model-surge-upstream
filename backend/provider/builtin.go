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
	})
	register(Spec{
		ID:          "openai",
		DisplayName: "OpenAI",
		Website:     "https://openai.com",
		BaseURL:     "https://api.openai.com",
		Protocols:   []string{ProtocolChatCompletions, ProtocolResponses},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
	})
	register(Spec{
		ID:          "gemini",
		DisplayName: "Google Gemini",
		Website:     "https://ai.google.dev",
		BaseURL:     "https://generativelanguage.googleapis.com",
		Protocols:   []string{ProtocolGemini, ProtocolChatCompletions},
		Auth:        AuthBearer,
		Credential:  CredAPIKey,
	})
	register(Spec{
		ID:          "kimi",
		DisplayName: "Moonshot Kimi",
		Website:     "https://platform.moonshot.cn",
		BaseURL:     "https://api.moonshot.cn/coding",
		Protocols:   []string{ProtocolAnthropic, ProtocolChatCompletions},
		Auth:        AuthAnthropicKey,
		Credential:  CredAPIKey,
	})
	// ark 的 responses 端点实测不可用，故只声明两个协议。
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
			Reset:  ResetPrepaid,
		},
	})
}
