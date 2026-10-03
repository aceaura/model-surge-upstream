package usage

import (
	"fmt"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/provider"
)

func TestFromAnthropicResponse(t *testing.T) {
	body := []byte(`{"id":"msg_1","model":"claude-sonnet-4-5","usage":{
		"input_tokens":10,"output_tokens":20,
		"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}`)
	u, ok := FromResponse(provider.ProtocolAnthropic, body)
	if !ok {
		t.Fatal("anthropic 响应应解析出用量")
	}
	if u.InputTokens != 10 || u.OutputTokens != 20 || u.CacheReadTokens != 30 || u.CacheWriteTokens != 40 {
		t.Fatalf("四桶解析错误: %+v", u)
	}
	if u.Semantics != SemanticsFresh {
		t.Fatal("anthropic 应为 fresh 语义")
	}
	if u.FreshInput() != 10 {
		t.Fatalf("fresh 语义净输入应原样: %d", u.FreshInput())
	}
	if u.MessageID != "msg_1" || u.Model != "claude-sonnet-4-5" {
		t.Fatalf("id/model 解析错误: %+v", u)
	}
}

func TestFromOpenAIResponse(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","model":"gpt-6-luna","usage":{
		"prompt_tokens":100,"completion_tokens":50,
		"prompt_tokens_details":{"cached_tokens":40}}}`)
	u, ok := FromResponse(provider.ProtocolChatCompletions, body)
	if !ok {
		t.Fatal("openai 响应应解析出用量")
	}
	if u.InputTokens != 100 || u.OutputTokens != 50 || u.CacheReadTokens != 40 {
		t.Fatalf("四桶解析错误: %+v", u)
	}
	if u.Semantics != SemanticsTotal {
		t.Fatal("openai 应为 total 语义")
	}
	if u.FreshInput() != 60 {
		t.Fatalf("total 语义净输入应扣缓存: %d", u.FreshInput())
	}
}

func TestFromResponsesResponse(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-6-luna","usage":{
		"input_tokens":80,"output_tokens":30,
		"input_tokens_details":{"cached_tokens":20}}}`)
	u, ok := FromResponse(provider.ProtocolResponses, body)
	if !ok {
		t.Fatal("responses 响应应解析出用量")
	}
	if u.InputTokens != 80 || u.CacheReadTokens != 20 || u.FreshInput() != 60 {
		t.Fatalf("responses 解析错误: %+v", u)
	}
}

func TestFromGeminiResponse(t *testing.T) {
	body := []byte(`{"modelVersion":"gemini-2.5-pro","responseId":"r-1","usageMetadata":{
		"promptTokenCount":70,"candidatesTokenCount":25,"thoughtsTokenCount":5,
		"totalTokenCount":100,"cachedContentTokenCount":10}}`)
	u, ok := FromResponse(provider.ProtocolGemini, body)
	if !ok {
		t.Fatal("gemini 响应应解析出用量")
	}
	if u.InputTokens != 70 || u.OutputTokens != 30 || u.CacheReadTokens != 10 {
		t.Fatalf("gemini 解析错误: %+v", u)
	}
	if u.FreshInput() != 60 {
		t.Fatalf("gemini 净输入应扣缓存读取: %d", u.FreshInput())
	}
}

func TestFromResponseRejectsErrorBody(t *testing.T) {
	if _, ok := FromResponse(provider.ProtocolAnthropic, []byte(`{"error":{"message":"boom"}}`)); ok {
		t.Fatal("错误体不应解析出用量")
	}
	if _, ok := FromResponse(provider.ProtocolChatCompletions, []byte(`not json`)); ok {
		t.Fatal("非法 JSON 不应解析出用量")
	}
}

func sse(lines ...string) *Sniffer {
	s := NewSniffer(provider.ProtocolAnthropic, "text/event-stream")
	for _, l := range lines {
		s.Write([]byte(l))
	}
	return s
}

func TestAnthropicStream(t *testing.T) {
	s := sse(
		"event: message_start\n",
		`data: {"type":"message_start","message":{"id":"msg_9","model":"claude-x","usage":{"input_tokens":5,"cache_read_input_tokens":60,"cache_creation_input_tokens":7}}}`+"\n\n",
		`data: {"type":"content_block_delta","delta":{"text":"hi"}}`+"\n\n",
		`data: {"type":"message_delta","usage":{"output_tokens":12}}`+"\n\n",
		"data: [DONE]\n\n",
	)
	u, ok := s.Result()
	if !ok {
		t.Fatal("anthropic 流应解析出用量")
	}
	if u.InputTokens != 5 || u.OutputTokens != 12 || u.CacheReadTokens != 60 || u.CacheWriteTokens != 7 {
		t.Fatalf("流式四桶错误: %+v", u)
	}
	if u.MessageID != "msg_9" {
		t.Fatalf("流式 id 错误: %+v", u)
	}
}

func TestAnthropicStreamSplitAcrossWrites(t *testing.T) {
	// 一条 data 行被拆到两次 Write，行缓冲必须拼回。
	full := `data: {"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n"
	s := NewSniffer(provider.ProtocolAnthropic, "text/event-stream")
	s.Write([]byte(full[:20]))
	s.Write([]byte(full[20:]))
	s.Write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\n"))
	u, ok := s.Result()
	if !ok || u.InputTokens != 3 || u.OutputTokens != 4 {
		t.Fatalf("跨 Write 拼行失败: ok=%v u=%+v", ok, u)
	}
}

func TestOpenAIStreamNeedsIncludeUsage(t *testing.T) {
	s := NewSniffer(provider.ProtocolChatCompletions, "text/event-stream")
	s.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"h\"}}]}\n\n"))
	s.Write([]byte(`data: {"id":"c1","usage":{"prompt_tokens":9,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}` + "\n\n"))
	u, ok := s.Result()
	if !ok {
		t.Fatal("openai 流末尾 chunk 应解析出用量")
	}
	if u.InputTokens != 9 || u.OutputTokens != 2 || u.CacheReadTokens != 4 || u.MessageID != "c1" {
		t.Fatalf("openai 流式解析错误: %+v", u)
	}
}

func TestResponsesStream(t *testing.T) {
	s := NewSniffer(provider.ProtocolResponses, "text/event-stream")
	s.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"h\"}\n\n"))
	s.Write([]byte(`data: {"type":"response.completed","response":{"id":"r2","usage":{"input_tokens":11,"output_tokens":3,"input_tokens_details":{"cached_tokens":1}}}}` + "\n\n"))
	u, ok := s.Result()
	if !ok || u.InputTokens != 11 || u.OutputTokens != 3 || u.CacheReadTokens != 1 {
		t.Fatalf("responses 流式解析错误: ok=%v u=%+v", ok, u)
	}
}

func TestGeminiStream(t *testing.T) {
	s := NewSniffer(provider.ProtocolGemini, "text/event-stream")
	s.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"h"}]}}]}` + "\n\n"))
	s.Write([]byte(`data: {"modelVersion":"gemini-2.5-pro","usageMetadata":{"promptTokenCount":8,"totalTokenCount":14,"cachedContentTokenCount":2}}` + "\n\n"))
	u, ok := s.Result()
	if !ok || u.InputTokens != 8 || u.OutputTokens != 6 || u.CacheReadTokens != 2 {
		t.Fatalf("gemini 流式解析错误: ok=%v u=%+v", ok, u)
	}
}

func TestNonStreamSnifferBuffersBody(t *testing.T) {
	s := NewSniffer(provider.ProtocolChatCompletions, "application/json")
	body := `{"id":"x","usage":{"prompt_tokens":1,"completion_tokens":2}}`
	// 分多次喂入，模拟 io.Copy 的多段写入。
	for i := 0; i < len(body); i += 7 {
		end := i + 7
		if end > len(body) {
			end = len(body)
		}
		s.Write([]byte(body[i:end]))
	}
	u, ok := s.Result()
	if !ok || u.InputTokens != 1 || u.OutputTokens != 2 {
		t.Fatalf("非流式嗅探失败: ok=%v u=%+v", ok, u)
	}
}

func TestEmptyUsageNotBillable(t *testing.T) {
	s := NewSniffer(provider.ProtocolAnthropic, "text/event-stream")
	s.Write([]byte(`data: {"type":"message_delta","usage":{"output_tokens":0}}` + "\n\n"))
	if _, ok := s.Result(); ok {
		t.Fatal("全 0 用量不应视为有效")
	}
}

func TestDerivedMetrics(t *testing.T) {
	u := Usage{InputTokens: 60, OutputTokens: 40, CacheReadTokens: 900, CacheWriteTokens: 0, Semantics: SemanticsFresh}
	if u.RealTotal() != 1000 {
		t.Fatalf("真实消耗应为四桶之和: %d", u.RealTotal())
	}
	// 命中率 = 读取 /（净输入 + 写入 + 读取）= 900/960。
	if got := u.HitRate(); got < 0.9374 || got > 0.9376 {
		t.Fatalf("命中率应≈0.9375: %f", got)
	}
	zero := Usage{}
	if zero.HitRate() != 0 {
		t.Fatal("分母为 0 时命中率应为 0")
	}
}

func TestStreamWithoutNewlineTerminator(t *testing.T) {
	// 上游末尾不带换行：最后一行仍要被消费。
	s := NewSniffer(provider.ProtocolChatCompletions, "text/event-stream")
	s.Write([]byte(`data: {"usage":{"prompt_tokens":6,"completion_tokens":7}}`))
	u, ok := s.Result()
	if !ok || u.InputTokens != 6 || u.OutputTokens != 7 {
		t.Fatalf("无换行末尾行未消费: ok=%v u=%+v", ok, u)
	}
}

func TestOpenAICacheFieldPrecedence(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolChatCompletions, provider.ProtocolResponses} {
		main, other, counts := "prompt_tokens_details", "input_tokens_details", `"prompt_tokens":100,"completion_tokens":20,"completion_tokens_details":{"reasoning_tokens":7}`
		if protocol == provider.ProtocolResponses {
			main, other, counts = other, main, `"input_tokens":100,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":7}`
		}
		cases := []struct {
			name, fields string
			read         int64
		}{
			{"explicit zero", fmt.Sprintf(`"%s":{"cached_tokens":0},"%s":{"cached_tokens":80},"cached_tokens":60,"prompt_cache_hit_tokens":40`, main, other), 0},
			{"nested wins", fmt.Sprintf(`"%s":{"cached_tokens":30},"%s":{"cached_tokens":80},"cached_tokens":60,"prompt_cache_hit_tokens":40`, main, other), 30},
			{"other protocol ignored", fmt.Sprintf(`"%s":{"cached_tokens":80}`, other), 0},
			{"top fallback", fmt.Sprintf(`"%s":{},"%s":{"cached_tokens":80},"cached_tokens":60`, main, other), 60},
			{"top zero", `"cached_tokens":0`, 0},
		}
		if protocol == provider.ProtocolChatCompletions {
			cases = append(cases, struct {
				name, fields string
				read         int64
			}{"deepseek hit", `"prompt_cache_hit_tokens":40,"prompt_cache_miss_tokens":60,"cached_tokens":70`, 40}, struct {
				name, fields string
				read         int64
			}{"deepseek zero", `"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":100,"cached_tokens":70`, 0}, struct {
				name, fields string
				read         int64
			}{"deepseek miss only", `"prompt_cache_miss_tokens":100`, 0})
		} else {
			cases = append(cases, struct {
				name, fields string
				read         int64
			}{"chat hit ignored", `"prompt_cache_hit_tokens":40,"cached_tokens":60`, 60})
		}
		for _, tc := range cases {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				var first Usage
				for i, model := range []string{"deepseek-chat", "unrelated-model"} {
					u, ok := FromResponse(protocol, []byte(fmt.Sprintf(`{"model":%q,"usage":{%s,%s}}`, model, counts, tc.fields)))
					if !ok || u.InputTokens != 100 || u.OutputTokens != 20 || u.CacheReadTokens != tc.read || u.CacheWriteTokens != 0 || u.Semantics != SemanticsTotal {
						t.Fatalf("ok=%v usage=%+v", ok, u)
					}
					u.Model = ""
					if i == 0 {
						first = u
					} else if u != first {
						t.Fatalf("model changed usage: %+v / %+v", first, u)
					}
				}
			})
		}
	}
}

func TestGeminiOutputFields(t *testing.T) {
	cases := []struct {
		name, fields string
		out          int64
	}{
		{"thoughts and candidates", `"candidatesTokenCount":25,"thoughtsTokenCount":5,"totalTokenCount":999`, 30},
		{"no total", `"candidatesTokenCount":25,"thoughtsTokenCount":5`, 30},
		{"thoughts only", `"thoughtsTokenCount":5`, 5},
		{"candidates only", `"candidatesTokenCount":25`, 25},
		{"explicit candidates zero", `"candidatesTokenCount":0,"totalTokenCount":999`, 0},
		{"explicit thoughts zero", `"thoughtsTokenCount":0,"totalTokenCount":999`, 0},
		{"both zero", `"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":999`, 0},
		{"total fallback", `"totalTokenCount":100`, 30},
		{"negative fallback", `"totalTokenCount":20`, 0},
		{"no output fields", `"cachedContentTokenCount":10`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var first Usage
			for i, model := range []string{"gemini-2.5-pro", "other-model"} {
				u, ok := FromResponse(provider.ProtocolGemini, []byte(fmt.Sprintf(`{"modelVersion":%q,"usageMetadata":{"promptTokenCount":70,%s}}`, model, tc.fields)))
				if !ok || u.OutputTokens != tc.out || u.CacheWriteTokens != 0 {
					t.Fatalf("ok=%v usage=%+v", ok, u)
				}
				u.Model = ""
				if i == 0 {
					first = u
				} else if u != first {
					t.Fatalf("model changed usage: %+v / %+v", first, u)
				}
			}
		})
	}
}

func TestGeminiStreamLastCumulativeUsage(t *testing.T) {
	s := NewSniffer(provider.ProtocolGemini, "text/event-stream")
	for _, fields := range []string{
		`"promptTokenCount":70,"candidatesTokenCount":20,"thoughtsTokenCount":8,"cachedContentTokenCount":10`,
		`"promptTokenCount":80,"candidatesTokenCount":30,"thoughtsTokenCount":5,"cachedContentTokenCount":15`,
		`"promptTokenCount":80,"candidatesTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":999,"cachedContentTokenCount":0`,
	} {
		s.Write([]byte(`data: {"usageMetadata":{` + fields + "}}\n\n"))
	}
	u, ok := s.Result()
	if !ok || u.InputTokens != 80 || u.OutputTokens != 0 || u.CacheReadTokens != 0 || u.CacheWriteTokens != 0 {
		t.Fatalf("ok=%v usage=%+v", ok, u)
	}
}

func TestAnthropicStreamFieldReplacement(t *testing.T) {
	for _, model := range []string{"claude-x", "other-model"} {
		s := sse(fmt.Sprintf(`data: {"type":"message_start","message":{"model":%q,"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}}`+"\n\n", model))
		steps := []struct {
			fields                     string
			input, output, read, write int64
		}{
			{`"input_tokens":20`, 20, 2, 30, 40},
			{`"input_tokens":5,"output_tokens":12`, 5, 12, 30, 40},
			{`"cache_read_input_tokens":60`, 5, 12, 60, 40},
			{`"cache_creation_input_tokens":7`, 5, 12, 60, 7},
			{`"input_tokens":5,"output_tokens":12,"cache_read_input_tokens":60,"cache_creation_input_tokens":7`, 5, 12, 60, 7},
			{`"input_tokens":0,"cache_read_input_tokens":0`, 0, 12, 0, 7},
			{`"output_tokens":0,"cache_creation_input_tokens":0,"input_tokens":3`, 3, 0, 0, 0},
		}
		for _, step := range steps {
			s.Write([]byte(`data: {"type":"message_delta","usage":{` + step.fields + "}}\n\n"))
			u, ok := s.Result()
			if !ok || u.InputTokens != step.input || u.OutputTokens != step.output || u.CacheReadTokens != step.read || u.CacheWriteTokens != step.write || u.Semantics != SemanticsFresh || u.FreshInput() != step.input {
				t.Fatalf("model=%s fields=%s ok=%v usage=%+v", model, step.fields, ok, u)
			}
		}
	}
}

func TestAnthropicStreamStartOnlyOutput(t *testing.T) {
	s := sse(`data: {"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":8,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}` + "\n\n")
	u, ok := s.Result()
	if !ok || u.InputTokens != 0 || u.OutputTokens != 8 || u.Semantics != SemanticsFresh {
		t.Fatalf("ok=%v usage=%+v", ok, u)
	}
}
