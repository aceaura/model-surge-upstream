package compact

import (
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

func chatTurns(n int) []any {
	msgs := []any{map[string]any{"role": "system", "content": "你是助手"}}
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": "问题" + string(rune('a'+i))},
			map[string]any{"role": "assistant", "content": "回答" + string(rune('a'+i))},
		)
	}
	return msgs
}

func TestChatCutPlainTurns(t *testing.T) {
	msgs := chatTurns(10) // 1 system + 20
	prefix, tail, ok := (chatImpl{}).Cut(msgs, 6)
	if !ok {
		t.Fatal("cut failed")
	}
	// system 不进 prefix/tail；tail = 最近 6 轮 = 12 条
	if len(tail) != 12 || len(prefix) != 8 {
		t.Fatalf("prefix=%d tail=%d, want 8/12", len(prefix), len(tail))
	}
	if tail[0].(map[string]any)["role"] != "user" {
		t.Fatal("tail[0] 必须是 user")
	}
	for _, m := range prefix {
		if m.(map[string]any)["role"] == "system" {
			t.Fatal("prefix 不应含 system")
		}
	}
}

func TestChatCutNeverLandsOnToolMessage(t *testing.T) {
	msgs := chatTurns(2)
	msgs = append(msgs,
		map[string]any{"role": "user", "content": "调个工具"},
		map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": "c1", "function": map[string]any{"name": "read", "arguments": "{}"}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "结果"},
		map[string]any{"role": "assistant", "content": "完成"},
	)
	// keepTurns=1：切点必须落在「调个工具」上，不能落在 role=tool 上
	// （那会悬空引用 prefix 里的 tool_calls）。
	_, tail, ok := (chatImpl{}).Cut(msgs, 1)
	if !ok {
		t.Fatal("cut failed")
	}
	if len(tail) != 4 {
		t.Fatalf("tail len = %d, want 4", len(tail))
	}
	if tail[0].(map[string]any)["role"] == "tool" {
		t.Fatal("tail 首条绝不能是 role=tool")
	}
	if tail[0].(map[string]any)["content"] != "调个工具" {
		t.Fatalf("tail[0] = %v", tail[0])
	}
}

func TestChatCutTooFew(t *testing.T) {
	// 只有 1 system + 1 轮：prefix（除 system）为空，不可切。
	if _, _, ok := (chatImpl{}).Cut(chatTurns(1), 1); ok {
		t.Fatal("want ok=false")
	}
}

func TestChatSplice(t *testing.T) {
	body := map[string]any{
		"model":    "gpt-x",
		"messages": chatTurns(1),
	}
	tail := []any{
		map[string]any{"role": "user", "content": "新问题"},
		map[string]any{"role": "assistant", "content": "新回答"},
	}
	out := (chatImpl{}).Splice(body, "摘要内容", tail)
	msgs := out["messages"].([]any)
	// [system, 摘要user, 新user, 新assistant]
	if len(msgs) != 4 {
		t.Fatalf("messages len = %d, want 4", len(msgs))
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatal("首条应保持 system")
	}
	sum := msgs[1].(map[string]any)
	if sum["role"] != "user" {
		t.Fatalf("摘要消息 role = %v, want user", sum["role"])
	}
	if s, _ := sum["content"].(string); s == "" || s == "摘要内容" {
		t.Fatalf("摘要应带前缀说明, got %q", s)
	}
}

func TestChatExtractSummary(t *testing.T) {
	raw := []byte(`{
		"choices": [{"message": {"role":"assistant","content":"摘要正文"}}],
		"usage": {"prompt_tokens":1500,"completion_tokens":300,"prompt_tokens_details":{"cached_tokens":400}}
	}`)
	text, u, err := (chatImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "摘要正文" {
		t.Fatalf("text = %q", text)
	}
	if u.InputTokens != 1500 || u.OutputTokens != 300 || u.CacheReadTokens != 400 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestChatExtractSummaryContentParts(t *testing.T) {
	raw := []byte(`{"choices": [{"message": {"content": [{"type":"text","text":"块形态"}]}}]}`)
	text, _, err := (chatImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "块形态" {
		t.Fatalf("text = %q", text)
	}
}

func TestChatSummaryUsageMatchesParser(t *testing.T) {
	for _, fields := range []string{
		`"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":0},"input_tokens_details":{"cached_tokens":80},"prompt_cache_hit_tokens":40,"cached_tokens":60`,
		`"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":40,"prompt_cache_miss_tokens":60,"cached_tokens":70`,
		`"prompt_tokens":100,"completion_tokens":20,"cached_tokens":30,"completion_tokens_details":{"reasoning_tokens":7}`,
		`"prompt_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0}`,
		"",
	} {
		raw := []byte(`{"id":"summary-id","model":"arbitrary-model","choices":[{"message":{"content":" 摘要正文 "}}]`)
		if fields != "" {
			raw = append(raw, []byte(`,"usage":{`+fields+`}`)...)
		}
		raw = append(raw, '}')
		text, got, err := (chatImpl{}).ExtractSummary(raw)
		want, _ := usage.FromResponse(provider.ProtocolChatCompletions, raw)
		if err != nil || text != "摘要正文" || got != want {
			t.Fatalf("fields=%s text=%q got=%+v want=%+v err=%v", fields, text, got, want, err)
		}
	}
}

func TestChatBuildSummaryRequest(t *testing.T) {
	msgs := chatTurns(3)
	body := map[string]any{"messages": msgs}
	prefix, _, ok := (chatImpl{}).Cut(msgs, 1)
	if !ok {
		t.Fatal("cut failed")
	}
	suffix, req, err := (chatImpl{}).BuildSummaryRequest("gpt-native", body, prefix, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if suffix != "/v1/chat/completions" {
		t.Fatalf("suffix = %s", suffix)
	}
	full := req["messages"].([]any)
	// system + prefix + 压缩指令
	if full[0].(map[string]any)["role"] != "system" {
		t.Fatal("摘要请求首条应为 system")
	}
	last := full[len(full)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatal("末条应为压缩指令 user 消息")
	}
	if _, ok := req["stream"]; ok {
		t.Fatal("摘要请求必须非流式")
	}
}
