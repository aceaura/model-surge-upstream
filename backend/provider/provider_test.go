package provider

import (
	"testing"
)

func TestBuiltinSpecsWellFormed(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("no builtin providers registered")
	}
	seen := map[string]bool{}
	for _, s := range all {
		if seen[s.ID] {
			t.Errorf("duplicate id %q", s.ID)
		}
		seen[s.ID] = true
		if s.BaseURL == "" {
			t.Errorf("provider %q: empty base_url", s.ID)
		}
		if s.DisplayName == "" || s.Website == "" {
			t.Errorf("provider %q: missing display name or website", s.ID)
		}
		if len(s.Protocols) == 0 {
			t.Errorf("provider %q: no protocols", s.ID)
		}
		if s.Credential != CredAPIKey && s.Credential != CredOAuthRefresh {
			t.Errorf("provider %q: credential = %q, supported: api_key, oauth_refresh", s.ID, s.Credential)
		}
		if s.Auth != AuthBearer && s.Auth != AuthAnthropicKey {
			t.Errorf("provider %q: unknown auth scheme %q", s.ID, s.Auth)
		}
	}
}

func TestKiroNotRegistered(t *testing.T) {
	if _, ok := Get("kiro"); ok {
		t.Error("kiro is deferred out of this iteration but is registered")
	}
}

func TestBuiltinBilling(t *testing.T) {
	want := map[string]Billing{
		"anthropic": BillingPayGo,
		"openai":    BillingPayGo,
		"gemini":    BillingPayGo,
		// kimi 预设端点是 api.kimi.com/coding,即 Kimi For Coding 订阅产品。
		"kimi":         BillingSubscription,
		"ark":          BillingPayGo,
		"deepseek":     BillingPayGo,
		"openai-codex": BillingSubscription,
		"bailian":      BillingSubscription,
	}
	for id, billing := range want {
		s, ok := Get(id)
		if !ok {
			t.Errorf("provider %q not registered", id)
			continue
		}
		if s.Billing != billing {
			t.Errorf("provider %q billing = %q, want %q", id, s.Billing, billing)
		}
	}
}

func TestBuiltinRegion(t *testing.T) {
	want := map[string]string{
		"anthropic":    RegionGlobal,
		"openai":       RegionGlobal,
		"gemini":       RegionGlobal,
		"kimi":         RegionGlobal,
		"ark":          RegionGlobal,
		"deepseek":     RegionGlobal,
		"openai-codex": RegionGlobal,
		"bailian":      RegionCN,
	}
	for id, region := range want {
		s, ok := Get(id)
		if !ok {
			t.Errorf("provider %q not registered", id)
			continue
		}
		if s.Region != region {
			t.Errorf("provider %q region = %q, want %q", id, s.Region, region)
		}
	}
}

// 官网要对上计费模式与服务区域:订阅产品挂订阅站,按量 API 挂控制台,
// 别把 marketing 主页或另一产品线地址挂上来。
func TestBuiltinWebsite(t *testing.T) {
	want := map[string]string{
		"anthropic": "https://console.anthropic.com",
		"openai":    "https://platform.openai.com",
		"gemini":    "https://aistudio.google.com",
		// kimi 是订阅(Kimi For Coding),订阅站在 kimi.com;
		// platform.moonshot.cn 是按量平台,不挂。
		"kimi":     "https://www.kimi.com",
		"ark":      "https://console.volcengine.com/ark",
		"deepseek": "https://platform.deepseek.com",
		// openai-codex 是订阅(ChatGPT Plus/Pro),订阅站在 chatgpt.com;
		// platform.openai.com 是按量平台,已挂给 openai。
		"openai-codex": "https://chatgpt.com",
		"bailian":      "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal",
	}
	for id, website := range want {
		s, ok := Get(id)
		if !ok {
			t.Errorf("provider %q not registered", id)
			continue
		}
		if s.Website != website {
			t.Errorf("provider %q website = %q, want %q", id, s.Website, website)
		}
	}
}

func TestAllOrderStable(t *testing.T) {
	first, second := All(), All()
	if len(first) != len(second) {
		t.Fatalf("length differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("order differs at %d: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}

func TestAllReturnsCopy(t *testing.T) {
	got := All()
	got[0].ID = "mutated"
	if All()[0].ID == "mutated" {
		t.Error("All() exposes the internal slice")
	}
}

func TestGet(t *testing.T) {
	s, ok := Get("kimi")
	if !ok {
		t.Fatal("kimi should be registered")
	}
	if s.BaseURL != "https://api.kimi.com/coding" {
		t.Errorf("kimi base_url = %q", s.BaseURL)
	}
	if _, ok := Get("nope"); ok {
		t.Error("unknown id should not resolve")
	}
}

func TestSupports(t *testing.T) {
	ark, _ := Get("ark")
	if !ark.Supports(ProtocolAnthropic) {
		t.Error("ark should support anthropic")
	}
	if ark.Supports(ProtocolResponses) {
		t.Error("ark should not support responses")
	}
	if ark.Supports("") {
		t.Error("empty protocol should not be supported")
	}
}

func TestQuotaDeclaration(t *testing.T) {
	ds, _ := Get("deepseek")
	if ds.Quota == nil {
		t.Fatal("deepseek should declare a quota api")
	}
	if ds.Quota.Reset != ResetPrepaid {
		t.Errorf("deepseek reset = %q", ds.Quota.Reset)
	}
	anth, _ := Get("anthropic")
	if anth.Quota != nil {
		t.Error("anthropic declares no quota api in this iteration")
	}
}

func TestIDs(t *testing.T) {
	ids := IDs()
	if len(ids) != len(All()) {
		t.Fatalf("IDs length %d != All length %d", len(ids), len(All()))
	}
}

func TestOpenAICodexSpec(t *testing.T) {
	s, ok := Get("openai-codex")
	if !ok {
		t.Fatal("openai-codex should be registered")
	}
	if s.Credential != CredOAuthRefresh {
		t.Errorf("credential = %q, want oauth_refresh", s.Credential)
	}
	if s.BaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Errorf("base_url = %q", s.BaseURL)
	}
	if !s.Supports(ProtocolResponses) || len(s.Protocols) != 1 {
		t.Errorf("订阅端点只支持 responses, protocols = %v", s.Protocols)
	}
	if s.Models == nil || s.Models.Path != "/models" {
		t.Errorf("codex /models 清单端点是推理档声明的数据源,应声明 Models, got %+v", s.Models)
	}
	if s.DisplayName != "OpenAI" {
		t.Errorf("display_name = %q, 与按量 openai 同名才能在级联里同厂商分组", s.DisplayName)
	}
}

func TestBailianSpec(t *testing.T) {
	s, ok := Get("bailian")
	if !ok {
		t.Fatal("bailian should be registered")
	}
	if s.BaseURL != "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode" {
		t.Errorf("base_url = %q", s.BaseURL)
	}
	if s.DisplayName != "Aliyun Bailian" || s.Auth != AuthBearer || s.Credential != CredAPIKey {
		t.Errorf("unexpected bailian spec: %+v", s)
	}
	if len(s.Protocols) != 1 || !s.Supports(ProtocolChatCompletions) {
		t.Errorf("protocols = %v, want chat_completions only", s.Protocols)
	}
	if s.Models != nil || s.Quota != nil {
		t.Errorf("unverified endpoints must not be declared: %+v", s)
	}
}

func TestRegisterRejects(t *testing.T) {
	cases := map[string]Spec{
		"duplicate id": {ID: "kimi", BaseURL: "https://x", Protocols: []string{ProtocolAnthropic}},
		"empty id":     {BaseURL: "https://x", Protocols: []string{ProtocolAnthropic}},
		"empty base":   {ID: "fresh-a", Protocols: []string{ProtocolAnthropic}},
		"no protocol":  {ID: "fresh-b", BaseURL: "https://x"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			register(spec)
		})
	}
}
