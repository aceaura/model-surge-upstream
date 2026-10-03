package codex

import (
	"regexp"
	"strings"
	"testing"
)

func TestShapeBodyForcesContract(t *testing.T) {
	body := map[string]any{
		"model":       "gpt-5-codex",
		"store":       true,
		"stream":      false,
		"temperature": 0.9,
		"top_p":       0.8,
		// overrides 里塞 true 也得被压掉:约束最后应用。
		"max_output_tokens": 4096,
	}
	out := ShapeBody(body, "gpt-5-codex")
	if out["store"] != false {
		t.Errorf("store = %v, want forced false", out["store"])
	}
	if out["stream"] != true {
		t.Errorf("stream = %v, want forced true", out["stream"])
	}
	for _, f := range droppedFields {
		if _, ok := out[f]; ok {
			t.Errorf("%s should be stripped", f)
		}
	}
	inst, _ := out["instructions"].(string)
	if inst == "" {
		t.Error("instructions should be injected when absent")
	}
}

func TestShapeBodyKeepsClientInstructions(t *testing.T) {
	out := ShapeBody(map[string]any{"instructions": "be terse"}, "gpt-5-codex")
	if out["instructions"] != "be terse" {
		t.Errorf("client instructions overwritten: %v", out["instructions"])
	}
	// 空白串视为空缺,照样注入。
	out = ShapeBody(map[string]any{"instructions": "  "}, "gpt-5-codex")
	if out["instructions"] == "  " {
		t.Error("blank instructions should be replaced by official prompt")
	}
}

func TestShapeBodyReasoningInclude(t *testing.T) {
	out := ShapeBody(map[string]any{"reasoning": map[string]any{"effort": "high"}}, "gpt-5-codex")
	inc, _ := out["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", out["include"])
	}
	// 已有同值不重复追加。
	out = ShapeBody(map[string]any{
		"reasoning": map[string]any{"effort": "high"},
		"include":   []any{"reasoning.encrypted_content"},
	}, "gpt-5-codex")
	if inc, _ = out["include"].([]any); len(inc) != 1 {
		t.Errorf("include duplicated: %v", out["include"])
	}
	// 无 reasoning 不加 include。
	out = ShapeBody(map[string]any{}, "gpt-5-codex")
	if _, ok := out["include"]; ok {
		t.Error("include should not be set without reasoning")
	}
}

func TestInstructionsForModel(t *testing.T) {
	cases := map[string]string{
		"gpt-5-codex":        gpt5Codex,
		"gpt-5.1-codex-max":  gpt5Codex,
		"gpt-5.2":            gpt52,
		"gpt-5.1":            gpt51,
		"unknown-future-x":   gpt5Codex,
		"GPT-5-CODEX-IGNcase": gpt5Codex,
	}
	for model, want := range cases {
		if got := InstructionsForModel(model); got != want {
			t.Errorf("InstructionsForModel(%q) picked wrong prompt", model)
		}
	}
	if strings.TrimSpace(gpt5Codex) == "" || strings.TrimSpace(gpt51) == "" || strings.TrimSpace(gpt52) == "" {
		t.Error("embedded prompts must be non-empty")
	}
}

func TestMapSuffix(t *testing.T) {
	if got := MapSuffix("/v1/responses"); got != "/responses" {
		t.Errorf("MapSuffix = %q", got)
	}
	if got := MapSuffix("/v1/other"); got != "/v1/other" {
		t.Errorf("unrelated suffix changed: %q", got)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestSessionID(t *testing.T) {
	a := SessionID("gpt-1", "cache-key-1")
	if !uuidRe.MatchString(a) {
		t.Errorf("not uuid-shaped: %q", a)
	}
	if SessionID("gpt-1", "cache-key-1") != a {
		t.Error("same account+key should be stable")
	}
	if SessionID("gpt-2", "cache-key-1") == a {
		t.Error("different account should isolate")
	}
	if SessionID("gpt-1", "cache-key-2") == a {
		t.Error("different cache key should isolate")
	}
}

func TestHeaders(t *testing.T) {
	h := Headers("at-123", "acc-456")
	want := map[string]string{
		"Authorization":      "Bearer at-123",
		"chatgpt-account-id": "acc-456",
		"OpenAI-Beta":        "responses=experimental",
		"Accept":             "text/event-stream",
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("header %q = %q, want %q", k, h[k], v)
		}
	}
	if h["originator"] == "" || h["version"] == "" {
		t.Error("originator/version must be set: upstream routes model cohorts by the pair")
	}
	if !strings.Contains(h["User-Agent"], h["originator"]) {
		t.Errorf("UA %q should carry originator", h["User-Agent"])
	}
}
