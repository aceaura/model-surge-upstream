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
		if s.Credential != CredAPIKey {
			t.Errorf("provider %q: credential = %q, this iteration supports only api_key", s.ID, s.Credential)
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
	if s.BaseURL != "https://api.moonshot.cn/coding" {
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
