package compact

import (
	"encoding/json"
	"testing"
)

// 构造 n 轮普通对话（user/assistant 交替）。
func anthropicTurns(n int) []any {
	msgs := []any{}
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			map[string]any{"role": "user", "content": "问题" + string(rune('a'+i))},
			map[string]any{"role": "assistant", "content": "回答" + string(rune('a'+i))},
		)
	}
	return msgs
}

func TestAnthropicCutPlainTurns(t *testing.T) {
	msgs := anthropicTurns(10) // 20 条
	prefix, tail, ok := (anthropicImpl{}).Cut(msgs, 6)
	if !ok {
		t.Fatal("cut failed")
	}
	// tail 从倒数第 6 个真人发言起：6 轮 = 12 条
	if len(tail) != 12 || len(prefix) != 8 {
		t.Fatalf("prefix=%d tail=%d, want 8/12", len(prefix), len(tail))
	}
	first := tail[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("tail[0] role = %v, want user", first["role"])
	}
}

func TestAnthropicCutPushesOutOfToolChain(t *testing.T) {
	// 尾部是工具链：user(真人) → assistant(tool_use) → user(tool_result) → assistant
	msgs := anthropicTurns(3)
	msgs = append(msgs,
		map[string]any{"role": "user", "content": "帮我查文件"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "read", "input": map[string]any{"path": "a"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "文件内容"},
		}},
		map[string]any{"role": "assistant", "content": "查完了"},
	)
	// keepTurns=1：切点必须落在「帮我查文件」这条真人发言上，
	// 绝不能落在其后的 tool_result 消息上（那会悬空引用 prefix 里的 tool_use）。
	prefix, tail, ok := (anthropicImpl{}).Cut(msgs, 1)
	if !ok {
		t.Fatal("cut failed")
	}
	if len(tail) != 4 {
		t.Fatalf("tail len = %d, want 4（整个工具链留在 tail）", len(tail))
	}
	if got := tail[0].(map[string]any)["content"]; got != "帮我查文件" {
		t.Fatalf("tail[0] = %v, want 真人发言", got)
	}
	if len(prefix) != 6 {
		t.Fatalf("prefix len = %d, want 6", len(prefix))
	}
}

func TestAnthropicCutWholeChainUnsafe(t *testing.T) {
	// 全会话只有一条真人发言：没有更早的切点，放弃压缩。
	msgs := []any{
		map[string]any{"role": "user", "content": "开始"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1"},
		}},
	}
	if _, _, ok := (anthropicImpl{}).Cut(msgs, 1); ok {
		t.Fatal("want ok=false（切点落在 i=0 时 prefix 为空）")
	}
}

func TestAnthropicCutTooFewMessages(t *testing.T) {
	if _, _, ok := (anthropicImpl{}).Cut(anthropicTurns(2), 6); ok {
		t.Fatal("消息数不足 keepTurns 时应 ok=false")
	}
}

func TestAnthropicSpliceAlternationAndCacheControl(t *testing.T) {
	body := map[string]any{
		"model":  "claude-x",
		"system": "你是助手",
		"messages": []any{
			map[string]any{"role": "user", "content": "旧问题"},
		},
	}
	tail := []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "新问题", "cache_control": map[string]any{"type": "ephemeral"}},
		}},
		map[string]any{"role": "assistant", "content": "新回答"},
	}
	out := (anthropicImpl{}).Splice(body, "摘要内容", tail)
	msgs := out["messages"].([]any)
	// [摘要user, 假assistant, 新user, 新assistant]：严格交替且首条 user
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	if len(msgs) != len(wantRoles) {
		t.Fatalf("messages len = %d, want %d", len(msgs), len(wantRoles))
	}
	for i, role := range wantRoles {
		if got := msgs[i].(map[string]any)["role"]; got != role {
			t.Fatalf("messages[%d] role = %v, want %s", i, got, role)
		}
	}
	// 摘要块带新断点
	sumBlock := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, ok := sumBlock["cache_control"]; !ok {
		t.Fatal("摘要块应带 cache_control 断点")
	}
	// tail 里的旧断点被剥离
	tailBlock := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, ok := tailBlock["cache_control"]; ok {
		t.Fatal("tail 里的 cache_control 应被剥离")
	}
	// system 不被改动
	if out["system"] != "你是助手" {
		t.Fatal("system 应保持不变")
	}
	// 原 tail 不被污染（深拷贝）
	origBlock := tail[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, ok := origBlock["cache_control"]; !ok {
		t.Fatal("Splice 不应修改调用方的 tail")
	}
}

func TestAnthropicBuildSummaryRequest(t *testing.T) {
	body := map[string]any{"system": "系统提示"}
	prefix := anthropicTurns(2) // 末条是 assistant
	suffix, req, err := (anthropicImpl{}).BuildSummaryRequest("claude-native", body, prefix, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if suffix != "/v1/messages" {
		t.Fatalf("suffix = %s", suffix)
	}
	if req["model"] != "claude-native" || req["max_tokens"] != 2048 {
		t.Fatalf("req = %v", req)
	}
	if _, ok := req["stream"]; ok {
		t.Fatal("摘要请求必须非流式")
	}
	if req["system"] != "系统提示" {
		t.Fatal("system 应带入摘要请求")
	}
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatal("末条必须是 user（压缩指令）")
	}
	// 深拷贝：原 prefix 不应被追加指令污染
	if len(prefix) != 4 {
		t.Fatal("prefix 被污染")
	}
}

func TestAnthropicBuildSummaryRequestEndsWithUser(t *testing.T) {
	// prefix 末条是 user（如 i=1 的极端切法）：指令并入末条而非新增，保持交替。
	prefix := []any{map[string]any{"role": "user", "content": "唯一问题"}}
	_, req, err := (anthropicImpl{}).BuildSummaryRequest("m", map[string]any{}, prefix, 100)
	if err != nil {
		t.Fatal(err)
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("指令应并入末条 user 消息，got %d 条", len(msgs))
	}
	content := msgs[0].(map[string]any)["content"].(string)
	if content == "唯一问题" {
		t.Fatal("指令未并入")
	}
	if prefix[0].(map[string]any)["content"] != "唯一问题" {
		t.Fatal("原 prefix 被污染")
	}
}

func TestAnthropicExtractSummary(t *testing.T) {
	raw := []byte(`{
		"content": [{"type":"text","text":"摘要正文"},{"type":"thinking","thinking":"..."}],
		"usage": {"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":50,"cache_creation_input_tokens":30}
	}`)
	text, u, err := (anthropicImpl{}).ExtractSummary(raw)
	if err != nil {
		t.Fatal(err)
	}
	if text != "摘要正文" {
		t.Fatalf("text = %q", text)
	}
	if u.InputTokens != 1000 || u.OutputTokens != 200 || u.CacheReadTokens != 50 || u.CacheWriteTokens != 30 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestAnthropicExtractSummaryNoText(t *testing.T) {
	raw := []byte(`{"content": [{"type":"thinking","thinking":"..."}]}`)
	if _, _, err := (anthropicImpl{}).ExtractSummary(raw); err == nil {
		t.Fatal("无 text 块应报错")
	}
}

// json 断言辅助：确保拼出的结构可正常序列化（防御 map 里混入非法值）。
func mustMarshal(t *testing.T, v any) {
	t.Helper()
	if _, err := json.Marshal(v); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
