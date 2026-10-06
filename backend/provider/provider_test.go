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
		if s.Credential != CredAPIKey && s.Credential != CredOAuthRefresh && s.Credential != CredKiroRefresh {
			t.Errorf("provider %q: unsupported credential = %q", s.ID, s.Credential)
		}
		if s.Auth != AuthBearer && s.Auth != AuthAnthropicKey {
			t.Errorf("provider %q: unknown auth scheme %q", s.ID, s.Auth)
		}
	}
}

func TestKiroRegistered(t *testing.T) {
	s, ok := Get("kiro.global.subscribe.standard")
	if !ok {
		t.Fatal("kiro should be registered")
	}
	if s.Credential != CredKiroRefresh || s.Auth != AuthBearer || s.Billing != BillingSubscription {
		t.Fatalf("kiro spec = %+v", s)
	}
	if !s.Supports(ProtocolAnthropic) || !s.Supports(ProtocolChatCompletions) || s.Supports(ProtocolResponses) {
		t.Fatalf("kiro protocols = %v", s.Protocols)
	}
	if s.Models == nil || !s.QuotaQueryable {
		t.Fatalf("kiro queries = %+v", s)
	}
}

func TestBuiltinBilling(t *testing.T) {
	want := map[string]Billing{
		"anthropic.global.api.standard": BillingPayGo,
		"openai.global.api.standard":    BillingPayGo,
		"gemini.global.api.standard":    BillingPayGo,
		// kimi 预设端点是 api.kimi.com/coding,即 Kimi For Coding 订阅产品。
		"kimi.global.subscribe.coding":           BillingSubscription,
		"ark.global.api.standard":               BillingPayGo,
		"deepseek.global.api.standard":          BillingPayGo,
		"openai.global.subscribe.codex":          BillingSubscription,
		"bailian.cn.subscribe.token-plan": BillingSubscription,
		"bailian.cn.subscribe.coding-plan": BillingSubscription,
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
		"anthropic.global.api.standard":         RegionGlobal,
		"openai.global.api.standard":            RegionGlobal,
		"gemini.global.api.standard":            RegionGlobal,
		"kimi.global.subscribe.coding":           RegionGlobal,
		"ark.global.api.standard":               RegionGlobal,
		"deepseek.global.api.standard":          RegionGlobal,
		"openai.global.subscribe.codex":          RegionGlobal,
		"bailian.cn.subscribe.token-plan": RegionCN,
		"bailian.cn.subscribe.coding-plan": RegionCN,
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

func TestBuiltinPlan(t *testing.T) {
	want := map[string]string{
		"anthropic.global.api.standard":         PlanStandard,
		"openai.global.api.standard":            PlanStandard,
		"gemini.global.api.standard":            PlanStandard,
		"kimi.global.subscribe.coding":           PlanStandard,
		"ark.global.api.standard":               PlanStandard,
		"deepseek.global.api.standard":          PlanStandard,
		"openai.global.subscribe.codex":          PlanStandard,
		"kiro.global.subscribe.standard":                  PlanStandard,
		"bailian.cn.subscribe.token-plan": "Token Plan",
		"bailian.cn.subscribe.coding-plan": "Coding Plan",
	}
	for id, plan := range want {
		s, ok := Get(id)
		if !ok {
			t.Errorf("provider %q not registered", id)
			continue
		}
		if s.Plan != plan {
			t.Errorf("provider %q plan = %q, want %q", id, s.Plan, plan)
		}
	}
}

// 官网要对上计费模式与服务区域:订阅产品挂订阅站,按量 API 挂控制台,
// 别把 marketing 主页或另一产品线地址挂上来。
func TestBuiltinWebsite(t *testing.T) {
	want := map[string]string{
		"anthropic.global.api.standard": "https://console.anthropic.com",
		"openai.global.api.standard":    "https://platform.openai.com",
		"gemini.global.api.standard":    "https://aistudio.google.com",
		// kimi 是订阅(Kimi For Coding),订阅站在 kimi.com;
		// platform.moonshot.cn 是按量平台,不挂。
		"kimi.global.subscribe.coding":  "https://www.kimi.com",
		"ark.global.api.standard":      "https://console.volcengine.com/ark",
		"deepseek.global.api.standard": "https://platform.deepseek.com",
		// openai.global.subscribe.codex 是订阅(ChatGPT 登录态),订阅站在 chatgpt.com;
		// platform.openai.com 是按量平台,已挂给 openai.global.api.standard。
		"openai.global.subscribe.codex":          "https://chatgpt.com",
		"bailian.cn.subscribe.token-plan": "https://bailian.console.aliyun.com/cn-beijing/subscription/token-plan/personal",
		"bailian.cn.subscribe.coding-plan": "https://bailian.console.aliyun.com/cn-beijing/subscription/coding-plan/personal",
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
	s, ok := Get("kimi.global.subscribe.coding")
	if !ok {
		t.Fatal("kimi.global.subscribe.coding should be registered")
	}
	if s.BaseURL != "https://api.kimi.com/coding" {
		t.Errorf("kimi base_url = %q", s.BaseURL)
	}
	if _, ok := Get("nope"); ok {
		t.Error("unknown id should not resolve")
	}
}

func TestSupports(t *testing.T) {
	ark, _ := Get("ark.global.api.standard")
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
	ds, _ := Get("deepseek.global.api.standard")
	if !ds.QuotaQueryable {
		t.Fatal("deepseek should declare a builtin quota query")
	}
	anth, _ := Get("anthropic.global.api.standard")
	if anth.QuotaQueryable {
		t.Error("anthropic declares no quota query in this iteration")
	}
}

func TestIDs(t *testing.T) {
	ids := IDs()
	if len(ids) != len(All()) {
		t.Fatalf("IDs length %d != All length %d", len(ids), len(All()))
	}
}

func TestOpenAICodexSpec(t *testing.T) {
	s, ok := Get("openai.global.subscribe.codex")
	if !ok {
		t.Fatal("openai.global.subscribe.codex should be registered")
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
		t.Errorf("display_name = %q, 与按量 openai.global.api.standard 同名才能在级联里同厂商分组", s.DisplayName)
	}
}

func TestBailianSpec(t *testing.T) {
	s, ok := Get("bailian.cn.subscribe.token-plan")
	if !ok {
		t.Fatal("bailian.cn.subscribe.token-plan should be registered")
	}
	if s.BaseURL != "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode" {
		t.Errorf("base_url = %q", s.BaseURL)
	}
	if s.DisplayName != "Aliyun Bailian" || s.Auth != AuthBearer || s.Credential != CredAPIKey {
		t.Errorf("unexpected bailian spec: %+v", s)
	}
	if len(s.Protocols) != 2 || !s.Supports(ProtocolChatCompletions) || !s.Supports(ProtocolResponses) {
		t.Errorf("protocols = %v, want chat_completions and responses", s.Protocols)
	}
	if s.Models == nil || s.Models.Path != "/v1/models" || s.Models.Method != "GET" {
		t.Errorf("models = %+v, want GET /v1/models", s.Models)
	}
	if !s.QuotaQueryable {
		t.Fatal("bailian should declare the builtin console quota query")
	}
}

func TestBailianCodingSpec(t *testing.T) {
	s, ok := Get("bailian.cn.subscribe.coding-plan")
	if !ok {
		t.Fatal("bailian.cn.subscribe.coding-plan should be registered")
	}
	if s.BaseURL != "https://coding.dashscope.aliyuncs.com" {
		t.Errorf("base_url = %q", s.BaseURL)
	}
	if s.DisplayName != "Aliyun Bailian" || s.Plan != "Coding Plan" {
		t.Errorf("display_name/plan = %q/%q, 须与 Token Plan 同厂商分组、套餐区分", s.DisplayName, s.Plan)
	}
	// 探针实测:chat completions 401(存在)、responses 404(不存在)、
	// anthropic 在 /apps/anthropic 独立路径(单 BaseURL 表达不了)。
	if len(s.Protocols) != 1 || !s.Supports(ProtocolChatCompletions) {
		t.Errorf("protocols = %v, want chat_completions only", s.Protocols)
	}
	if s.Models == nil || s.Models.Path != "/v1/models" || s.Models.Method != "GET" {
		t.Errorf("models = %+v, want GET /v1/models(探针 200)", s.Models)
	}
	if s.QuotaQueryable {
		t.Error("Coding Plan 额度按请求数计且控制台无开放查询 API,不应声明内置额度查询")
	}
}

func TestRegisterRejects(t *testing.T) {
	cases := map[string]Spec{
		"duplicate id": {ID: "kimi.global.subscribe.coding", BaseURL: "https://x", Protocols: []string{ProtocolAnthropic}},
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
