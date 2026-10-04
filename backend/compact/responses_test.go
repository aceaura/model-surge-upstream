package compact

import (
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/usage"
)

func responsesUserMsg(text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}
}

func responsesAssistantMsg(text string) map[string]any {
	return map[string]any{
		"type":    "message",
		"role":    "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

func responsesTurns(n int) []any {
	var items []any
	for i := 0; i < n; i++ {
		items = append(items,
			responsesUserMsg("问题"+string(rune('a'+i))),
			responsesAssistantMsg("回答"+string(rune('a'+i))),
		)
	}
	return items
}

func TestResponsesCutPlainTurns(t *testing.T) {
	items := responsesTurns(10)
	prefix, tail, ok := (responsesImpl{}).Cut(items, 6)
	if !ok {
		t.Fatal("cut failed")
	}
	if len(tail) != 12 || len(prefix) != 8 {
		t.Fatalf("prefix=%d tail=%d, want 8/12", len(prefix), len(tail))
	}
	if !isResponsesUserMessage(tail[0]) {
		t.Fatal("tail[0] 必须是 user 消息")
	}
}

func TestResponsesCutNeverLandsOnToolItem(t *testing.T) {
	items := responsesTurns(2)
	items = append(items,
		responsesUserMsg("调个工具"),
		map[string]any{"type": "function_call", "call_id": "c1", "name": "read", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "结果"},
		responsesAssistantMsg("完成"),
	)
	// keepTurns=1：切点必须落在「调个工具」上，不能落在 function_call_output
	// 上（那会悬空引用 prefix 里的 function_call）。
	_, tail, ok := (responsesImpl{}).Cut(items, 1)
	if !ok {
		t.Fatal("cut failed")
	}
	if len(tail) != 4 {
		t.Fatalf("tail len = %d, want 4", len(tail))
	}
	first := tail[0].(map[string]any)
	if first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("tail[0] = %v", first)
	}
}

func TestResponsesCutLegacyShapeWithoutType(t *testing.T) {
	// 旧形态 input item 没有 type 字段，只有 role/content。
	items := []any{
		map[string]any{"role": "user", "content": "一"},
		map[string]any{"role": "assistant", "content": "二"},
		map[string]any{"role": "user", "content": "三"},
	}
	_, tail, ok := (responsesImpl{}).Cut(items, 1)
	if !ok {
		t.Fatal("cut failed")
	}
	if len(tail) != 1 || tail[0].(map[string]any)["content"] != "三" {
		t.Fatalf("tail = %v", tail)
	}
}

func TestResponsesCutTooFew(t *testing.T) {
	if _, _, ok := (responsesImpl{}).Cut(responsesTurns(1), 1); ok {
		t.Fatal("want ok=false")
	}
}

func TestResponsesMessagesStringInput(t *testing.T) {
	// input 为字符串形态时 Messages 返回 nil，Cut 失败走 fail-open。
	body := map[string]any{"input": "整段提示"}
	if msgs := (responsesImpl{}).Messages(body); msgs != nil {
		t.Fatalf("msgs = %v, want nil", msgs)
	}
}

func TestResponsesSplice(t *testing.T) {
	body := map[string]any{
		"model":        "gpt-x",
		"instructions": "你是助手",
		"input":        responsesTurns(1),
	}
	tail := []any{
		responsesUserMsg("新问题"),
		responsesAssistantMsg("新回答"),
	}
	out := (responsesImpl{}).Splice(body, "摘要内容", tail)
	if out["instructions"] != "你是助手" {
		t.Fatal("instructions 应原样保留")
	}
	items := out["input"].([]any)
	// [摘要user, 新user, 新assistant]
	if len(items) != 3 {
		t.Fatalf("input len = %d, want 3", len(items))
	}
	sum := items[0].(map[string]any)
	if sum["role"] != "user" || sum["type"] != "message" {
		t.Fatalf("摘要 item = %v", sum)
	}
	part := sum["content"].([]any)[0].(map[string]any)
	if part["type"] != "input_text" || !strings.Contains(part["text"].(string), "摘要内容") {
		t.Fatalf("摘要内容段 = %v", part)
	}
	if !strings.HasPrefix(part["text"].(string), summaryPreamble) {
		t.Fatal("摘要应带前缀说明")
	}
}

func TestResponsesBuildSummaryRequest(t *testing.T) {
	items := responsesTurns(3)
	body := map[string]any{"instructions": "系统提示", "input": items}
	prefix, _, ok := (responsesImpl{}).Cut(items, 1)
	if !ok {
		t.Fatal("cut failed")
	}
	suffix, req, err := (responsesImpl{}).BuildSummaryRequest("gpt-native", body, prefix, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if suffix != "/v1/responses" {
		t.Fatalf("suffix = %s", suffix)
	}
	if req["model"] != "gpt-native" || req["max_output_tokens"] != 2048 {
		t.Fatalf("req = %v", req)
	}
	if req["instructions"] != "系统提示" {
		t.Fatal("instructions 应带入摘要请求")
	}
	if _, ok := req["stream"]; ok {
		t.Fatal("摘要请求默认非流式（codex 硬约束由 Run 另行施加）")
	}
	full := req["input"].([]any)
	if len(full) != len(prefix)+1 {
		t.Fatalf("input len = %d, want %d", len(full), len(prefix)+1)
	}
	last := full[len(full)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatal("末条应为压缩指令 user 消息")
	}
}

func TestResponsesExtractSummaryJSON(t *testing.T) {
	raw := []byte(`{
		"output": [{"type":"message","role":"assistant","content":[{"type":"output_text","text":"摘要正文"}]}],
		"usage": {"input_tokens":1500,"output_tokens":300,"input_tokens_details":{"cached_tokens":400}}
	}`)
	text, u, err := (responsesImpl{}).ExtractSummary(raw)
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

func TestResponsesExtractSummaryOutputTextFallback(t *testing.T) {
	raw := []byte(`{"output_text":"便捷字段"}`)
	text, _, err := (responsesImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "便捷字段" {
		t.Fatalf("text = %q", text)
	}
}

func TestResponsesExtractSummarySSE(t *testing.T) {
	// 标准 Responses 上游：completed 事件带完整 output。
	raw := []byte("event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"摘\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"摘要正文\"}]}],\"usage\":{\"input_tokens\":800,\"output_tokens\":120}}}\n\n")
	text, u, err := (responsesImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "摘要正文" {
		t.Fatalf("text = %q", text)
	}
	if u.InputTokens != 800 || u.OutputTokens != 120 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestResponsesExtractSummarySSECodexShape(t *testing.T) {
	// codex 订阅端点实测形态：completed 的 output 恒为空数组，
	// 正文只经 output_text.delta 流出。
	raw := []byte("event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"摘要\"}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"正文\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":3413,\"output_tokens\":419}}}\n\n")
	text, u, err := (responsesImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "摘要正文" {
		t.Fatalf("text = %q", text)
	}
	if u.InputTokens != 3413 || u.OutputTokens != 419 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestResponsesExtractSummarySSEOutputTextDone(t *testing.T) {
	// delta 缺失时回退 output_text.done 的完整 text。
	raw := []byte("event: response.output_text.done\n" +
		"data: {\"type\":\"response.output_text.done\",\"text\":\"done 形态\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n")
	text, _, err := (responsesImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "done 形态" {
		t.Fatalf("text = %q", text)
	}
}

func TestResponsesExtractSummarySSEWithoutCompleted(t *testing.T) {
	raw := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")
	if _, _, err := (responsesImpl{}).ExtractSummary(raw); err == nil {
		t.Fatal("want error")
	}
}

func TestResponsesSummaryUsageMatchesParser(t *testing.T) {
	raw := []byte(`{"id":"resp-1","model":"gpt-native","output":[{"type":"message","content":[{"type":"output_text","text":"摘要正文"}]}],"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":30}}}`)
	text, got, err := (responsesImpl{}).ExtractSummary(raw)
	want, _ := usage.FromResponse(provider.ProtocolResponses, raw)
	if err != nil || text != "摘要正文" || got != want {
		t.Fatalf("text=%q got=%+v want=%+v err=%v", text, got, want, err)
	}
}
