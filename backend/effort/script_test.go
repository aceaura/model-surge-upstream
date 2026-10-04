package effort

import (
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// kimiScript 是 kimi K3 的档位映射:思考不可关,档位走顶层
// reasoning_effort(anthropic 外壳但不吃 output_config)。元数据不参与
// 映射——档号到上行值的表写在脚本体内,查不到(未声明档)不落字段,
// 上游吃自家默认 max。
const kimiScript = `({
	apply: function(ctx) {
		var effort = { "1": "low", "2": "high", "3": "max" }[ctx.level];
		if (effort) {
			ctx.request.reasoning_effort = effort;
		}
		return ctx.request;
	}
})`

func TestValidateScript(t *testing.T) {
	if err := ValidateScript(kimiScript); err != nil {
		t.Fatalf("valid script rejected: %v", err)
	}
	for name, code := range map[string]string{
		"empty":         "",
		"syntax error":  `({apply: function(})`,
		"missing apply": `({extractor: function(r){ return r; }})`,
		"not an object": `42`,
	} {
		if err := ValidateScript(code); err == nil {
			t.Fatalf("%s: expected validation error", name)
		} else if !apperr.Is(err, apperr.InvalidRequest) {
			t.Fatalf("%s: code = %v, want invalid_request", name, apperr.CodeOf(err))
		}
	}
}

func TestRunScript(t *testing.T) {
	efforts := []Entry{{Name: "1", Value: "low"}, {Name: "2", Value: "high"}}
	body := map[string]any{
		"model":      "k3-256k",
		"max_tokens": float64(100),
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	}
	out, err := RunScript(kimiScript, ScriptContext{
		Level: "2", Protocol: "anthropic",
		Efforts: efforts, Request: body,
	})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if out["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", out["reasoning_effort"])
	}
	// 原请求键原样保留,类型不漂。
	if out["model"] != "k3-256k" || out["max_tokens"] != float64(100) {
		t.Fatalf("request keys not preserved: %v", out)
	}
	if _, ok := out["messages"].([]any); !ok {
		t.Fatalf("messages type drifted: %T", out["messages"])
	}
}

func TestRunScriptUnknownLevelSkipsField(t *testing.T) {
	out, err := RunScript(kimiScript, ScriptContext{
		Level: "9", Protocol: "anthropic",
		Request: map[string]any{"model": "k3-256k"},
	})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if _, ok := out["reasoning_effort"]; ok {
		t.Fatalf("unknown level should not set reasoning_effort: %v", out)
	}
}

func TestRunScriptFailures(t *testing.T) {
	for name, code := range map[string]string{
		"throw":        `({apply: function(ctx){ throw new Error("boom"); }})`,
		"non-object":   `({apply: function(ctx){ return 42; }})`,
		"missing fn":   `({map: function(ctx){ return ctx.request; }})`,
		"syntax error": `({apply: function(})`,
	} {
		_, err := RunScript(code, ScriptContext{Level: "1", Request: map[string]any{}})
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if !apperr.Is(err, apperr.EffortScriptFailed) {
			t.Fatalf("%s: code = %v, want effort_script_failed", name, apperr.CodeOf(err))
		}
	}
}

func TestRunScriptReadsContext(t *testing.T) {
	code := `({apply: function(ctx) {
		ctx.request.echo = [ctx.level, String(ctx.value), ctx.protocol, String(ctx.efforts.length)];
		return ctx.request;
	}})`
	out, err := RunScript(code, ScriptContext{
		Level: "1", Protocol: "chat_completions",
		Efforts: []Entry{{Name: "1", Value: "low"}},
		Request: map[string]any{},
	})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	echo, ok := out["echo"].([]any)
	if !ok || len(echo) != 4 {
		t.Fatalf("echo = %v", out["echo"])
	}
	// ctx 不再带 value(元数据不参与映射),读出来是 undefined。
	if echo[0] != "1" || echo[1] != "undefined" || echo[2] != "chat_completions" || echo[3] != "1" {
		t.Fatalf("echo = %v", echo)
	}
}

func TestRunScriptTimeout(t *testing.T) {
	_, err := RunScript(`({apply: function(ctx){ for(;;){} }})`,
		ScriptContext{Level: "1", Request: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want timeout interrupt", err)
	}
}
