package kiro

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func stringHeader(name, value string) []byte {
	b := []byte{byte(len(name))}
	b = append(b, []byte(name)...)
	b = append(b, 7, byte(len(value)>>8), byte(len(value)))
	return append(b, []byte(value)...)
}
func frameWithHeaders(headers, payload []byte) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b, uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(b[4:], uint32(len(headers)))
	binary.BigEndian.PutUint32(b[8:], crc32.ChecksumIEEE(b[:8]))
	b = append(b, headers...)
	b = append(b, payload...)
	c := make([]byte, 4)
	binary.BigEndian.PutUint32(c, crc32.ChecksumIEEE(b))
	return append(b, c...)
}
func frame(kind string, payload object) []byte {
	h := append(stringHeader(":message-type", "event"), stringHeader(":event-type", kind)...)
	return frameWithHeaders(h, []byte(jsonText(payload)))
}
func exceptionFrame() []byte {
	h := append(stringHeader(":message-type", "exception"), stringHeader(":exception-type", "ThrottlingException")...)
	return frameWithHeaders(h, []byte(`{"message":"slow down"}`))
}
func joinedFrames(fs ...[]byte) []byte { return bytes.Join(fs, nil) }
func endFrame() []byte {
	return joinedFrames(frame("metadataEvent", object{"usage": 0.25}), frame("metadataEvent", object{"contextUsagePercentage": 95}))
}
func tokenFrame() []byte {
	return joinedFrames(frame("usageEvent", object{"usage": object{"inputTokens": 17, "outputTokens": 9}}), frame("metadataEvent", object{"contextUsagePercentage": 95}))
}
func toolDefinition(protocol string) []any {
	schema := object{"type": "object", "properties": object{"x": object{"type": "integer"}}, "required": []any{}, "additionalProperties": false}
	if protocol == "anthropic" {
		return []any{object{"name": "lookup", "description": "Look up x", "input_schema": schema}}
	}
	return []any{object{"type": "function", "function": object{"name": "lookup", "description": "Look up x", "parameters": schema}}}
}
func clientRequest(t *testing.TB, base, protocol string, stream bool) *http.Request {
	(*t).Helper()
	path := "/v1/messages"
	if protocol == "openai" {
		path = "/v1/chat/completions"
	}
	data := object{"model": "claude-sonnet-4-6", "stream": stream, "max_tokens": 1024, "messages": []any{object{"role": "user", "content": "hello"}}, "tools": toolDefinition(protocol), "stream_options": object{"include_usage": true}}
	r, err := http.NewRequest("POST", base+path, strings.NewReader(jsonText(data)))
	if err != nil {
		(*t).Fatal(err)
	}
	for k, v := range Headers("native-token", "arn:aws:codewhisperer:eu-west-1:123:profile/p") {
		r.Header.Set(k, v)
	}
	return r
}
func do(t *testing.T, base, protocol string, stream bool) (*http.Response, []byte, error) {
	t.Helper()
	tb := testing.TB(t)
	r := clientRequest(&tb, base, protocol, stream)
	resp, err := NewTransport(nil).RoundTrip(r)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}
func stub(t *testing.T, frames []byte, inspect func(*http.Request, object)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		m, err := decodeObject(string(b))
		if err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/generateAssistantResponse" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if inspect != nil {
			inspect(r, m)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("Content-Encoding", "identity")
		w.WriteHeader(200)
		for pos := 0; pos < len(frames); {
			n := 1 + (pos % 19)
			if pos+n > len(frames) {
				n = len(frames) - pos
			}
			if _, err := w.Write(frames[pos : pos+n]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			pos += n
		}
	}))
	t.Cleanup(server.Close)
	return server
}
func events(t *testing.T, data []byte) []object {
	t.Helper()
	var out []object
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxToolBytes+1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			out = append(out, object{"done": true})
			continue
		}
		v, err := decodeObject(raw)
		if err != nil {
			t.Fatalf("bad SSE JSON: %v", err)
		}
		out = append(out, v)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func parseResult(t *testing.T, b []byte) object {
	t.Helper()
	m, err := decodeObject(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func number(v any) int { n, _ := absoluteTokens(v); return n }

func TestBothProtocolsStreamAndNonstream(t *testing.T) {
	wire := joinedFrames(
		frame("assistantResponseEvent", object{"content": "ha"}),
		frame("assistantResponseEvent", object{"content": "ha"}), // Identical text chunks are real deltas, not duplicates.
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_1", "input": object{}}),
		frame("toolUseEvent", object{"input": "{\"x\":"}),
		frame("toolUseEvent", object{"input": "1}", "stop": true}),
		frame("assistantResponseEvent", object{"content": "then"}),
		frame("toolUseEvent", object{"name": "lookup", "toolUseId": "call_2", "input": object{"x": 2}, "stop": true}),
		tokenFrame(),
	)
	server := stub(t, wire, func(r *http.Request, p object) {
		if r.Header.Get("Authorization") != "Bearer native-token" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		if p["profileArn"] != "arn:aws:codewhisperer:eu-west-1:123:profile/p" {
			t.Errorf("profile=%v", p["profileArn"])
		}
	})
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+"/"+map[bool]string{true: "stream", false: "nonstream"}[stream], func(t *testing.T) {
				resp, data, err := do(t, server.URL, protocol, stream)
				if err != nil {
					t.Fatal(err)
				}
				if resp.Header.Get("Content-Length") != "" || resp.Header.Get("Content-Encoding") != "" || resp.ContentLength != -1 {
					t.Fatal("stale response framing headers")
				}
				if stream {
					es := events(t, data)
					if protocol == "anthropic" {
						assertAnthropicStream(t, es)
					} else {
						assertOpenAIStream(t, es)
					}
				} else {
					m := parseResult(t, data)
					usage := obj(m["usage"])
					if protocol == "anthropic" {
						if m["stop_reason"] != "tool_use" {
							t.Fatal(m)
						}
						blocks := list(m["content"])
						// streaming_anthropic.py:761-786: 非流式块顺序恒为
						// thinking → text → tool_use,正文聚成单个 text 块。
						if len(blocks) != 3 {
							t.Fatalf("blocks: %v", blocks)
						}
						if obj(blocks[0])["text"] != "hahathen" {
							t.Fatal(blocks)
						}
						for i, index := range []int{1, 2} {
							b := obj(blocks[index])
							if str(b["id"]) != []string{"call_1", "call_2"}[i] || number(obj(b["input"])["x"]) != i+1 {
								t.Fatal(blocks)
							}
						}
						if number(usage["input_tokens"]) != 17 || number(usage["output_tokens"]) != 9 {
							t.Fatal(usage)
						}
					} else {
						choice := obj(list(m["choices"])[0])
						message := obj(choice["message"])
						if choice["finish_reason"] != "tool_calls" || message["content"] != "hahathen" {
							t.Fatal(m)
						}
						calls := list(message["tool_calls"])
						if len(calls) != 2 || obj(obj(calls[1])["function"])["arguments"] != `{"x": 2}` {
							t.Fatal(calls)
						}
						if number(usage["prompt_tokens"]) != 17 || number(usage["completion_tokens"]) != 9 || number(usage["total_tokens"]) != 26 {
							t.Fatal(usage)
						}
					}
					if obj(usage["kiro_usage_source"])["input_tokens"] != "upstream:absolute_tokens" {
						t.Fatal(usage)
					}
				}
			})
		}
	}
}
func assertAnthropicStream(t *testing.T, es []object) {
	t.Helper()
	open := map[int]bool{}
	stopped := map[int]bool{}
	arguments := map[int]string{}
	types := map[int]string{}
	ids := map[int]string{}
	var text string
	var final object
	if es[0]["type"] != "message_start" || es[len(es)-1]["type"] != "message_stop" {
		t.Fatal(es)
	}
	for _, e := range es {
		index := number(e["index"])
		switch str(e["type"]) {
		case "content_block_start":
			if open[index] || stopped[index] {
				t.Fatal("repeated index")
			}
			open[index] = true
			block := obj(e["content_block"])
			types[index] = str(block["type"])
			ids[index] = str(block["id"])
		case "content_block_delta":
			if !open[index] || stopped[index] {
				t.Fatal("delta outside block")
			}
			d := obj(e["delta"])
			if d["type"] == "text_delta" {
				text += str(d["text"])
			} else if d["type"] == "input_json_delta" {
				arguments[index] += str(d["partial_json"])
			}
		case "content_block_stop":
			if !open[index] || stopped[index] {
				t.Fatal("stop outside block")
			}
			stopped[index] = true
		case "message_delta":
			final = e
		}
	}
	// streaming_core.py:362-368: 工具事件在流末尾统一交出,正文保持单个
	// text 块,两个工具块依次收尾(十八轮更正,此前按逐工具实时发块断言)。
	if text != "hahathen" || len(open) != 3 || len(stopped) != 3 || types[1] != "tool_use" || ids[1] != "call_1" || ids[2] != "call_2" || arguments[1] != `{"x": 1}` || arguments[2] != `{"x": 2}` {
		t.Fatalf("text=%s indices=%v args=%v", text, types, arguments)
	}
	if obj(final["delta"])["stop_reason"] != "tool_use" || number(obj(final["usage"])["input_tokens"]) != 17 || number(obj(final["usage"])["output_tokens"]) != 9 {
		t.Fatal(final)
	}
}
func assertOpenAIStream(t *testing.T, es []object) {
	t.Helper()
	var text, finish string
	args := map[int]string{}
	ids := map[int]string{}
	var usage object
	for _, e := range es {
		if u := obj(e["usage"]); u != nil {
			usage = u
		}
		if len(list(e["choices"])) == 0 {
			continue
		}
		c := obj(list(e["choices"])[0])
		d := obj(c["delta"])
		text += str(d["content"])
		if r := str(c["finish_reason"]); r != "" {
			finish = r
		}
		for _, v := range list(d["tool_calls"]) {
			tc := obj(v)
			i := number(tc["index"])
			if id := str(tc["id"]); id != "" {
				ids[i] = id
			}
			args[i] += str(obj(tc["function"])["arguments"])
		}
	}
	if text != "hahathen" || finish != "tool_calls" || ids[0] != "call_1" || ids[1] != "call_2" || args[0] != `{"x": 1}` || args[1] != `{"x": 2}` {
		t.Fatalf("text=%s finish=%s ids=%v args=%v", text, finish, ids, args)
	}
	if number(usage["prompt_tokens"]) != 17 || number(usage["completion_tokens"]) != 9 || es[len(es)-1]["done"] != true {
		t.Fatal(es)
	}
}

func TestTextOnlyEstimatesAndStopReasons(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+map[bool]string{true: "stream", false: "nonstream"}[stream], func(t *testing.T) {
				server := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "abcdefgh"}), frame("metadataEvent", object{"usage": 2.5, "contextUsagePercentage": 99, "stopReason": "max_tokens"})), nil)
				_, data, err := do(t, server.URL, protocol, stream)
				if err != nil {
					t.Fatal(err)
				}
				var usage object
				var reason string
				if stream {
					for _, e := range events(t, data) {
						if u := obj(e["usage"]); u != nil {
							usage = u
						}
						if e["type"] == "message_delta" {
							reason = str(obj(e["delta"])["stop_reason"])
						}
						if len(list(e["choices"])) > 0 {
							if r := str(obj(list(e["choices"])[0])["finish_reason"]); r != "" {
								reason = r
							}
						}
					}
				} else {
					m := parseResult(t, data)
					usage = obj(m["usage"])
					if protocol == "anthropic" {
						reason = str(m["stop_reason"])
					} else {
						reason = str(obj(list(m["choices"])[0])["finish_reason"])
					}
				}
				output, input := number(usage["output_tokens"]), number(usage["input_tokens"])
				expected := "max_tokens"
				if protocol == "openai" {
					output, input = number(usage["completion_tokens"]), number(usage["prompt_tokens"])
					expected = "length"
				}
				// contextUsagePercentage=99 无绝对 input:total=int(0.99×200000)
				// =198000,prompt=198000-3=197997(streaming_core.py:510-535)。
				if output != 3 || input != 197997 || reason != expected {
					t.Fatalf("usage=%v reason=%s", usage, reason)
				}
				if str(obj(usage["kiro_usage_source"])["input_tokens"]) != "derived:context_usage_percentage" {
					t.Fatal(usage)
				}
			})
		}
	}
}

func TestRequestHistorySystemImagesAndTools(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		t.Run(protocol, func(t *testing.T) {
			var messages []any
			var assistant, result, image object
			if protocol == "anthropic" {
				// models_anthropic.py:229: role 是 Literal["user","assistant"],
				// system/developer 等角色被参考实现 422 拒绝。
				messages = []any{object{"role": "user", "content": "first"}, object{"role": "user", "content": "second"}}
				assistant = object{"role": "assistant", "content": []any{object{"type": "tool_use", "id": "history_1", "name": "lookup", "input": object{"x": 42}}}}
				result = object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "history_1", "is_error": true, "content": "failed lookup"}}}
				image = object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}}
			} else {
				messages = []any{object{"role": "system", "content": "system instruction"}, object{"role": "developer", "content": "developer instruction"}, object{"role": "user", "content": "first"}, object{"role": "user", "content": "second"}}
				assistant = object{"role": "assistant", "content": nil, "tool_calls": []any{object{"id": "history_1", "type": "function", "function": object{"name": "lookup", "arguments": `{"x":42}`}}}}
				result = object{"role": "tool", "tool_call_id": "history_1", "content": "lookup result"}
				image = object{"type": "image_url", "image_url": object{"url": "data:image/png;base64,aGVsbG8="}}
			}
			messages = append(messages, assistant, result, object{"role": "assistant", "content": "old answer"}, object{"role": "user", "content": []any{object{"type": "text", "text": "new question"}, image}})
			root := object{"model": "claude-sonnet-4-6", "max_tokens": 1024, "messages": messages, "tools": toolDefinition(protocol), "reasoning_effort": "xhigh"}
			server := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame()), func(r *http.Request, p object) {
				if r.URL.RawQuery != "" || r.Host == "client-host.invalid" {
					t.Errorf("caller endpoint hints survived: %s %s", r.Host, r.URL)
				}
				for _, h := range []string{HeaderProvider, HeaderProfileARN, "X-Api-Key", "Proxy-Authorization", "Cookie", "Anthropic-Version", "X-Msu-Upstream-Base-Url"} {
					if r.Header.Get(h) != "" {
						t.Errorf("leaked %s", h)
					}
				}
				if r.Header.Get("X-Amz-Target") != "AmazonCodeWhispererStreamingService.GenerateAssistantResponse" || r.Header.Get("X-Amzn-Kiro-Agent-Mode") != "vibe" {
					t.Error("caller overrode native identity")
				}
				state := obj(p["conversationState"])
				history := list(state["history"])
				var usesMsg, resultsMsg int
				if protocol == "openai" {
					// converters_core.py:1728-1740: 合并→首条user→归一→交错,
					// developer 归一为 user 后与相邻 user 之间插合成 assistant 占位。
					if len(history) != 8 {
						t.Fatalf("history=%v", history)
					}
					first := obj(obj(history[0])["userInputMessage"])
					if first["content"] != "system instruction"+truncationSystemAddition+"\n\n(empty placeholder)" {
						t.Errorf("system/history=%v", first)
					}
					if obj(obj(history[2])["userInputMessage"])["content"] != "developer instruction" ||
						obj(obj(history[4])["userInputMessage"])["content"] != "first\nsecond" {
						t.Errorf("normalized history=%v", history)
					}
					usesMsg, resultsMsg = 5, 6
				} else {
					if len(history) != 4 {
						t.Fatalf("history=%v", history)
					}
					if obj(obj(history[0])["userInputMessage"])["content"] != strings.TrimSpace(truncationSystemAddition)+"\n\nfirst\nsecond" {
						t.Errorf("history=%v", history)
					}
					usesMsg, resultsMsg = 1, 2
				}
				uses := list(obj(obj(history[usesMsg])["assistantResponseMessage"])["toolUses"])
				if len(uses) != 1 || obj(uses[0])["toolUseId"] != "history_1" || number(obj(obj(uses[0])["input"])["x"]) != 42 {
					t.Errorf("toolUses=%v", uses)
				}
				results := list(obj(obj(obj(history[resultsMsg])["userInputMessage"])["userInputMessageContext"])["toolResults"])
				if len(results) != 1 || obj(results[0])["toolUseId"] != "history_1" {
					t.Errorf("toolResults=%v", results)
				}
				// converters_core.py:819/845: status 恒 success,失败只经文本传达。
				wantText := "lookup result"
				if protocol == "anthropic" {
					wantText = "failed lookup"
				}
				if obj(results[0])["status"] != "success" || str(obj(list(obj(results[0])["content"])[0])["text"]) != wantText {
					t.Errorf("tool result mangled: %v", results)
				}
				current := obj(obj(state["currentMessage"])["userInputMessage"])
				if current["modelId"] != "claude-sonnet-4.6" || current["content"] != "new question" {
					t.Errorf("current=%v", current)
				}
				images := list(current["images"])
				if len(images) != 1 || obj(images[0])["format"] != "png" || obj(obj(images[0])["source"])["bytes"] != "aGVsbG8=" {
					t.Errorf("images=%v", images)
				}
				context := obj(current["userInputMessageContext"])
				if context["images"] != nil {
					t.Error("images inside context")
				}
				spec := obj(obj(list(context["tools"])[0])["toolSpecification"])
				schema := obj(obj(spec["inputSchema"])["json"])
				if _, ok := schema["required"]; ok {
					t.Error("empty required not sanitized")
				}
				if _, ok := schema["additionalProperties"]; ok {
					t.Error("additionalProperties not sanitized")
				}
				if obj(obj(p["additionalModelRequestFields"])["output_config"])["effort"] != "high" {
					t.Errorf("effort=%v", p)
				}
			})
			path := "/v1/messages"
			if protocol == "openai" {
				path = "/v1/chat/completions"
			}
			raw := jsonText(root)
			req, _ := http.NewRequest("POST", server.URL+path+"?api_key=client-secret", strings.NewReader(raw))
			req.Host = "client-host.invalid"
			for k, v := range Headers("native-token", "profile") {
				req.Header.Set(k, v)
			}
			for _, h := range []string{"X-Api-Key", "Proxy-Authorization", "Cookie", "Anthropic-Version", "X-Msu-Upstream-Base-Url", "X-Amz-Target", "X-Amzn-Kiro-Agent-Mode"} {
				req.Header.Set(h, "client-unrelated-secret")
			}
			beforeHeader := req.Header.Clone()
			beforeURL := *req.URL
			beforeBody := req.Body
			resp, err := NewTransport(nil).RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if jsonText(beforeHeader) != jsonText(req.Header) || *req.URL != beforeURL || req.Host != "client-host.invalid" || req.Body != beforeBody {
				t.Fatal("original request mutated")
			}
			saved, _ := io.ReadAll(req.Body)
			if string(saved) != raw {
				t.Fatal("replayable original body consumed")
			}
		})
	}
}

// splitReader makes every framing boundary test deterministic, independent of
// how net/http coalesces HTTP chunks.
type splitReader struct {
	data []byte
	step int
}

func (r *splitReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.step
	if n > len(p) {
		n = len(p)
	}
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}
func TestFrameSplittingCRCAndTruncation(t *testing.T) {
	good := frame("assistantResponseEvent", object{"content": "中文 {\"content\":\"embedded\"} 😀"})
	for step := 1; step <= len(good)+1; step++ {
		p := eventReader{r: &splitReader{data: append([]byte(nil), good...), step: step}}
		e, err := p.next()
		if err != nil || e.data["content"] != "中文 {\"content\":\"embedded\"} 😀" {
			t.Fatalf("step %d: %v %v", step, e, err)
		}
		if _, err := p.next(); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	}
	for n := 1; n < len(good); n++ {
		p := eventReader{r: bytes.NewReader(good[:n])}
		if _, err := p.next(); err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("truncation %d accepted: %v", n, err)
		}
	}
	for _, index := range []int{0, 4, 8, 12, len(good) - 5, len(good) - 1} {
		bad := append([]byte(nil), good...)
		bad[index] ^= 1
		p := eventReader{r: bytes.NewReader(bad)}
		if _, err := p.next(); err == nil || !strings.Contains(err.Error(), "CRC") {
			t.Fatalf("corruption %d: %v", index, err)
		}
	}
	for _, sizes := range [][2]uint32{{15, 0}, {maxFrameBytes + 1, 0}, {16, maxHeaderBytes + 1}, {20, 5}} {
		b := make([]byte, 12)
		binary.BigEndian.PutUint32(b, sizes[0])
		binary.BigEndian.PutUint32(b[4:], sizes[1])
		binary.BigEndian.PutUint32(b[8:], crc32.ChecksumIEEE(b[:8]))
		p := eventReader{r: bytes.NewReader(b)}
		if _, err := p.next(); err == nil || !strings.Contains(err.Error(), "length") {
			t.Fatal(err)
		}
	}
}
func TestEventHeaderTypes(t *testing.T) {
	h := append(stringHeader(":message-type", "event"), stringHeader(":event-type", "assistantResponseEvent")...)
	for typ := byte(0); typ <= 9; typ++ {
		h = append(h, 1, 'x', typ)
		switch typ {
		case 2:
			h = append(h, 1)
		case 3:
			h = append(h, 0, 1)
		case 4:
			h = append(h, 0, 0, 0, 1)
		case 5, 8:
			h = append(h, make([]byte, 8)...)
		case 6, 7:
			h = append(h, 0, 1, 'a')
		case 9:
			h = append(h, make([]byte, 16)...)
		}
	}
	p := eventReader{r: bytes.NewReader(frameWithHeaders(h, []byte(`{"content":"ok"}`)))}
	if _, err := p.next(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{{2, 'a'}, {1, 'a', 77}, {1, 'a', 7, 0}, {1, 'a', 7, 0, 10, 'b'}} {
		if _, err := parseEventHeaders(bad); err == nil {
			t.Fatalf("accepted headers %v", bad)
		}
	}
	dup := append(stringHeader(":event-type", "x"), stringHeader(":event-type", "y")...)
	if _, err := parseEventHeaders(dup); err == nil {
		t.Fatal("accepted duplicate event-type")
	}
}

func TestFailuresAreNotSuccessfulCompletions(t *testing.T) {
	corrupted := frame("assistantResponseEvent", object{"content": "bad"})
	corrupted[len(corrupted)-1] ^= 1
	cases := map[string][]byte{
		"exception":    exceptionFrame(),
		"crc":          corrupted,
		"truncated":    frame("assistantResponseEvent", object{"content": "x"})[:20],
		"invalid-json": frameWithHeaders(append(stringHeader(":message-type", "event"), stringHeader(":event-type", "assistantResponseEvent")...), []byte(`{"content":`)),
	}
	for name, wire := range cases {
		for _, protocol := range []string{"anthropic", "openai"} {
			for _, stream := range []bool{false, true} {
				t.Run(name+"/"+protocol+map[bool]string{true: "stream", false: "nonstream"}[stream], func(t *testing.T) {
					server := stub(t, wire, nil)
					_, data, err := do(t, server.URL, protocol, stream)
					if err == nil {
						t.Fatalf("accepted failure: %s", data)
					}
					body := string(data)
					// streaming_openai.py:431-441: openai 流式中段错误先发
					// [DONE] 再中断连接,属参考实现的预期形状;其余路径不得
					// 出现成功收尾标记。
					disguised := strings.Contains(body, `"type":"message_stop"`) || strings.Contains(body, `"finish_reason":"stop"`)
					if !disguised && !(stream && protocol == "openai") {
						disguised = strings.Contains(body, "[DONE]")
					}
					if disguised {
						t.Fatalf("failure disguised as success: %s", body)
					}
					// streaming_anthropic.py:726-732: anthropic 流式中段错误
					// 以 error 事件发出;openai 不发错误事件。
					if stream && protocol == "anthropic" && !strings.Contains(body, `"type":"error"`) {
						t.Fatalf("missing stream error event: %s (%v)", body, err)
					}
				})
			}
		}
	}
}

func TestImageLeniency(t *testing.T) {
	// converters_core.py:390-392/750-770: URL 图片、空 data、坏 data URL 跳过
	// 或原样放行,请求不在边界被拒。image_url 块不在此列:
	// models_anthropic.py 的 ContentBlock 联合类型只含 image,anthropic
	// 请求带 image_url 在边界 422(见下方负例)。
	blocks := []any{
		object{"type": "text", "text": "look"},
		object{"type": "image", "source": object{"type": "url", "url": "https://example.invalid/x.png"}},
		object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": ""}},
		object{"type": "image", "source": object{"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}},
	}
	root := object{"model": "claude-sonnet-4-6", "max_tokens": 64, "messages": []any{object{"role": "user", "content": blocks}}}
	server := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame()), func(_ *http.Request, p object) {
		images := list(obj(obj(obj(p["conversationState"])["currentMessage"])["userInputMessage"])["images"])
		if len(images) != 1 || obj(images[0])["format"] != "png" {
			t.Errorf("images=%v", images)
		}
	})
	req, _ := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(jsonText(root)))
	for k, v := range Headers("native-token", "") {
		req.Header.Set(k, v)
	}
	resp, err := NewTransport(nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// extensions/server_tool_blocks.py:121-131: 宽化联合以 UnknownContentBlock
	// 兜底,消息级 image_url 被接受并静默忽略——不再 422,图片也不上行。
	ignored := object{"model": "claude-sonnet-4-6", "max_tokens": 64, "messages": []any{object{"role": "user", "content": []any{
		object{"type": "image_url", "image_url": object{"url": "https://example.invalid/y.png"}},
	}}}}
	server2 := stub(t, joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame()), func(_ *http.Request, p object) {
		images := list(obj(obj(obj(p["conversationState"])["currentMessage"])["userInputMessage"])["images"])
		if len(images) != 0 {
			t.Errorf("images=%v", images)
		}
	})
	req, _ = http.NewRequest("POST", server2.URL+"/v1/messages", strings.NewReader(jsonText(ignored)))
	for k, v := range Headers("native-token", "") {
		req.Header.Set(k, v)
	}
	resp2, err := NewTransport(nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
}

func TestLenientToolFrames(t *testing.T) {
	// parsers.py:408-427: 孤儿碎片/stop 帧静默忽略;未声明的工具调用在无
	// 严格 tool_choice 时照常透传(parsers.py 无白名单校验)。
	cases := map[string][]byte{
		"orphan-tool":     joinedFrames(frame("toolUseEvent", object{"input": "{}", "stop": true}), endFrame()),
		"disallowed-tool": joinedFrames(frame("toolUseEvent", object{"name": "other", "input": object{}, "stop": true}), endFrame()),
	}
	for name, wire := range cases {
		for _, protocol := range []string{"anthropic", "openai"} {
			server := stub(t, wire, nil)
			_, data, err := do(t, server.URL, protocol, false)
			if err != nil {
				t.Fatalf("%s/%s: %v", name, protocol, err)
			}
			m := parseResult(t, data)
			if name == "disallowed-tool" {
				if protocol == "anthropic" && len(list(m["content"])) != 1 {
					t.Fatalf("%s/%s: %v", name, protocol, m)
				}
				if protocol == "openai" && len(list(obj(obj(list(m["choices"])[0])["message"])["tool_calls"])) != 1 {
					t.Fatalf("%s/%s: %v", name, protocol, m)
				}
			}
		}
	}
}

func TestEmptyStreamReturns200(t *testing.T) {
	// streaming_openai.py:787: 空流(无内容、无终止标记)仍回 200,
	// 空 content + 正常结束,不再是错误。
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			server := stub(t, endFrame(), nil)
			_, data, err := do(t, server.URL, protocol, stream)
			if err != nil {
				t.Fatalf("%s stream=%v: %v", protocol, stream, err)
			}
			if protocol == "anthropic" {
				if !strings.Contains(string(data), `"stop_reason":"end_turn"`) {
					t.Fatalf("%s stream=%v: %s", protocol, stream, data)
				}
			} else if !strings.Contains(string(data), `"finish_reason":"stop"`) {
				t.Fatalf("%s stream=%v: %s", protocol, stream, data)
			}
		}
	}
}

func TestNativeThinkingAndSignature(t *testing.T) {
	server := stub(t, joinedFrames(frame("reasoningContentEvent", object{"text": "think"}), frame("reasoningContentEvent", object{"signature": "signed"}), frame("assistantResponseEvent", object{"content": "answer"}), endFrame()), nil)
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			_, data, err := do(t, server.URL, protocol, stream)
			if err != nil {
				t.Fatal(err)
			}
			if protocol == "anthropic" {
				if stream {
					if !strings.Contains(string(data), "thinking_delta") || !strings.Contains(string(data), "signature_delta") {
						t.Fatal(string(data))
					}
				} else {
					blocks := list(parseResult(t, data)["content"])
					if obj(blocks[0])["thinking"] != "think" || obj(blocks[0])["signature"] != "signed" || obj(blocks[1])["text"] != "answer" {
						t.Fatal(blocks)
					}
				}
			} else if !strings.Contains(string(data), `"reasoning_content":"think"`) {
				t.Fatal(string(data))
			}
		}
	}
}

func TestTransparentNonKiroAndDefaultBase(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://example.invalid/not-a-supported-path", strings.NewReader("unmodified"))
	req.Header.Set(HeaderProvider, "other")
	req.Header.Set("X-Api-Key", "secret")
	expected := &http.Response{StatusCode: 207, Header: http.Header{"Content-Length": []string{"4"}}, Body: io.NopCloser(strings.NewReader("body"))}
	var got *http.Request
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) { got = r; return expected, nil })
	resp, err := NewTransport(base).RoundTrip(req)
	if err != nil || got != req || resp != expected {
		t.Fatal("non-Kiro request/response not transparent")
	}
	if NewTransport(nil).(*transport).base != http.DefaultTransport {
		t.Fatal("nil base not default transport")
	}
	for _, path := range []string{"/v1/models", "/v1/responses", "/generateAssistantResponse", "/v1/messages/count_tokens"} {
		req, _ := http.NewRequest("POST", "https://example.invalid"+path, strings.NewReader("{}"))
		req.Header.Set(HeaderProvider, ProviderID)
		if _, err := NewTransport(base).RoundTrip(req); err == nil {
			t.Fatalf("unsupported path %s accepted", path)
		}
	}
}

func TestHTTPErrorsPreservedAndBodiesClosed(t *testing.T) {
	defer func(backoff func(int) time.Duration) { retryBackoff = backoff }(retryBackoff)
	retryBackoff = func(int) time.Duration { return 0 }
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				io.WriteString(w, `{"message":"throttled"}`)
			}))
			_, data, err := do(t, server.URL, protocol, stream)
			server.Close()
			// routes_anthropic.py:622-670/routes_openai.py:584: 状态码保留,
			// 错误体重写为客户端协议形状。
			want := `{"error":{"message":"throttled","type":"api_error"},"type":"error"}`
			if protocol == "openai" {
				want = `{"error":{"code":429,"message":"throttled","type":"kiro_api_error"}}`
			}
			if err != nil || string(data) != want {
				t.Fatalf("%s: %s %v", protocol, data, err)
			}
			if attempts != maxRetryAttempts {
				t.Fatalf("attempts=%d, want %d", attempts, maxRetryAttempts)
			}
		}
	}
}

type trackedBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *trackedBody) Close() error { b.closes.Add(1); return nil }
func TestUnderlyingBodyOwnership(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			wire := joinedFrames(frame("assistantResponseEvent", object{"content": "ok"}), endFrame())
			if failure {
				wire = exceptionFrame()
			}
			body := &trackedBody{Reader: bytes.NewReader(wire)}
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
			})
			tb := testing.TB(t)
			req := clientRequest(&tb, "https://example.invalid", "openai", stream)
			resp, err := NewTransport(base).RoundTrip(req)
			if stream {
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body.Close()
			}
			if failure && err == nil {
				t.Fatal("failure accepted")
			}
			if !failure && err != nil {
				t.Fatal(err)
			}
			if body.closes.Load() != 1 {
				t.Fatalf("closed %d times", body.closes.Load())
			}
		}
	}
}

func TestRealtimeTextAndCloseCancellation(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, cancelInstead := range []bool{false, true} {
			t.Run(protocol+map[bool]string{true: "cancel", false: "close"}[cancelInstead], func(t *testing.T) {
				serverCanceled := make(chan struct{})
				release := make(chan struct{})
				var once sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer once.Do(func() { close(serverCanceled) })
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					w.Write(frame("assistantResponseEvent", object{"content": "immediate"}))
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-release:
					}
				}))
				defer server.Close()
				defer close(release)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				tb := testing.TB(t)
				req := clientRequest(&tb, server.URL, protocol, true).WithContext(ctx)
				resp, err := NewTransport(nil).RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				reader := bufio.NewReader(resp.Body)
				seen := make(chan error, 1)
				go func() {
					for {
						line, err := reader.ReadString('\n')
						if err != nil {
							seen <- err
							return
						}
						if strings.Contains(line, "immediate") {
							seen <- nil
							return
						}
					}
				}()
				select {
				case err := <-seen:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("text buffered waiting for upstream EOF")
				}
				// Start a blocked next read, then close/cancel. No stream pump exists to leak.
				blocked := make(chan error, 1)
				go func() { _, err := io.ReadAll(resp.Body); blocked <- err }()
				if cancelInstead {
					cancel()
				} else {
					resp.Body.Close()
				}
				select {
				case err := <-blocked:
					if err == nil {
						t.Fatal("cancel returned successful EOF")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("read did not unblock")
				}
				select {
				case <-serverCanceled:
				case <-time.After(3 * time.Second):
					t.Fatal("upstream not canceled")
				}
			})
		}
	}
}

func TestNonstreamCancellation(t *testing.T) {
	canceled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame("assistantResponseEvent", object{"content": "partial"}))
		w.(http.Flusher).Flush()
		close(canceled)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tb := testing.TB(t)
	req := clientRequest(&tb, server.URL, "anthropic", false).WithContext(ctx)
	result := make(chan error, 1)
	go func() { _, err := NewTransport(nil).RoundTrip(req); result <- err }()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream not started")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("nonstream cancel succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nonstream cancel blocked")
	}
}

func TestRound25ResponseConsecutiveSignatures(t *testing.T) {
	for _, ending := range []string{"text", "tool", "finalize"} {
		t.Run(ending, func(t *testing.T) {
			s := newResponseState(requestOptions{protocol: "anthropic", stream: true})
			var emitted []object
			s.emit = func(_ string, event object) { emitted = append(emitted, event) }
			for _, data := range []object{{"text": "secret"}, {"signature": "s1"}, {"signature": "s2"}, {"text": "more"}} {
				if err := s.accept(wireEvent{kind: "reasoningContentEvent", data: data}); err != nil {
					t.Fatal(err)
				}
			}
			if s.blockType != "thinking" || s.blockIndex != 0 || len(emitted) != 5 {
				t.Fatalf("thinking closed early: block=%s index=%d events=%v", s.blockType, s.blockIndex, emitted)
			}
			for i, signature := range []string{"s1", "s2"} {
				event := emitted[i+2]
				if event["type"] != "content_block_delta" || event["index"] != 0 || obj(event["delta"])["signature"] != signature {
					t.Fatalf("signature not in original block: %v", event)
				}
			}
			if ending == "text" {
				if err := s.accept(wireEvent{data: object{"content": "answer"}}); err != nil {
					t.Fatal(err)
				}
				before := len(emitted)
				if err := s.accept(wireEvent{data: object{"signature": "late"}}); err != nil || len(emitted) != before {
					t.Fatalf("late signature emitted: events=%v err=%v", emitted, err)
				}
			} else if ending == "tool" {
				if err := s.toolEvent(object{"name": "lookup", "toolUseId": "call25", "input": "{}", "stop": true}); err != nil {
					t.Fatal(err)
				}
			}
			s.terminal = true
			if err := s.finalize(); err != nil {
				t.Fatal(err)
			}
			stops := 0
			for _, event := range emitted {
				if event["type"] == "content_block_stop" && event["index"] == 0 {
					stops++
				}
			}
			if stops != 1 || s.blockType != "" {
				t.Fatalf("thinking stop count=%d block=%s events=%v", stops, s.blockType, emitted)
			}
		})
	}
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol+"/collect-last-wins", func(t *testing.T) {
			s := newResponseState(requestOptions{protocol: protocol})
			for _, data := range []object{{"text": "secret"}, {"signature": "s1"}, {"signature": "s2"}, {"content": "answer"}, {"signature": "late"}} {
				if err := s.accept(wireEvent{kind: "reasoningContentEvent", data: data}); err != nil {
					t.Fatal(err)
				}
			}
			s.terminal = true
			if err := s.finalize(); err != nil {
				t.Fatal(err)
			}
			if s.fullThinking.String() != "secret" || s.fullText.String() != "answer" || s.thinkingSignature != "late" {
				t.Fatalf("collection changed: thinking=%q text=%q signature=%q", s.fullThinking.String(), s.fullText.String(), s.thinkingSignature)
			}
			if protocol == "anthropic" && obj(s.blocks[0])["signature"] != "late" {
				t.Fatalf("last signature lost: %v", s.blocks)
			}
		})
	}
	t.Run("openai/stream-drops-signatures", func(t *testing.T) {
		s := newResponseState(requestOptions{protocol: "openai", stream: true})
		var emitted []object
		s.emit = func(_ string, event object) { emitted = append(emitted, event) }
		for _, data := range []object{{"text": "secret"}, {"signature": "s1"}, {"signature": "s2"}, {"text": "more"}} {
			if err := s.accept(wireEvent{kind: "reasoningContentEvent", data: data}); err != nil {
				t.Fatal(err)
			}
		}
		if len(emitted) != 2 || s.fullThinking.String() != "secretmore" {
			t.Fatalf("OpenAI signature affected output: %v", emitted)
		}
	})
}

func TestRound25ResponseAnthropicStartUsage(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "estimated"
		if absolute {
			name = "absolute"
		}
		t.Run(name, func(t *testing.T) {
			s := newResponseState(requestOptions{protocol: "anthropic", policyMode: "none", inputEstimate: 17})
			if err := s.accept(wireEvent{data: object{"content": "answer"}}); err != nil {
				t.Fatal(err)
			}
			usage := object{"cacheReadInputTokens": json.Number("7"), "cacheCreationInputTokens": json.Number("3")}
			if absolute {
				usage["outputTokens"] = json.Number("29")
			}
			if err := s.accept(wireEvent{data: object{"usage": usage}}); err != nil {
				t.Fatal(err)
			}
			if err := s.finalize(); err != nil {
				t.Fatal(err)
			}
			final := s.anthropicUsage()
			s.options.stream = true
			var emitted []object
			s.emit = func(_ string, event object) { emitted = append(emitted, event) }
			s.start()
			s.finishStream()
			start := obj(obj(emitted[0]["message"])["usage"])
			if start["output_tokens"] != 0 || final["output_tokens"] == 0 {
				t.Fatalf("start=%v final=%v", start, final)
			}
			for key, want := range final {
				if key != "output_tokens" && !reflect.DeepEqual(start[key], want) {
					t.Fatalf("usage superset lost: key=%s start=%v final=%v", key, start, final)
				}
			}
			if !reflect.DeepEqual(obj(emitted[1]["usage"]), final) || !reflect.DeepEqual(s.anthropicUsage(), final) {
				t.Fatalf("start changed final usage: events=%v final=%v", emitted, final)
			}
		})
	}
}

func TestRound25ResponseCacheUsageReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, policy string
		stream, replace        bool
	}{
		{"strict-anthropic-stream", "anthropic", "none", true, true},
		{"strict-anthropic-collect", "anthropic", "none", false, true},
		{"strict-openai-stream", "openai", "none", true, true},
		{"strict-openai-collect", "openai", "none", false, true},
		{"ordinary-anthropic-collect", "anthropic", "", false, true},
		{"ordinary-anthropic-stream", "anthropic", "", true, false},
		{"ordinary-openai-stream", "openai", "", true, false},
		{"ordinary-openai-collect", "openai", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newResponseState(requestOptions{protocol: tc.protocol, policyMode: tc.policy, stream: tc.stream})
			seed := object{"inputTokens": json.Number("17"), "outputTokens": json.Number("29"), "cacheReadInputTokens": json.Number("7"), "cacheCreationInputTokens": json.Number("3")}
			for _, usage := range []any{object{"cacheReadInputTokens": json.Number("0")}, object{"cacheReadInputTokens": "invalid", "cacheCreationInputTokens": nil}, object{}, json.Number("0.25"), false, "invalid", []any{}} {
				if err := s.accept(wireEvent{data: object{"usage": seed}}); err != nil {
					t.Fatal(err)
				}
				if err := s.accept(wireEvent{data: object{"usage": usage}}); err != nil {
					t.Fatal(err)
				}
				read, create, hasRead, hasCreate := 7, 3, true, true
				if tc.replace {
					read, create, hasRead, hasCreate = 0, 0, false, false
				}
				if _, ok := obj(usage)["cacheReadInputTokens"].(json.Number); ok {
					read, hasRead = 0, true
				}
				if s.cacheRead != read || s.cacheCreation != create || s.cacheReadAbs != hasRead || s.cacheCreateAbs != hasCreate {
					t.Errorf("usage=%v cache=%d/%d presence=%v/%v want=%d/%d %v/%v", usage, s.cacheRead, s.cacheCreation, s.cacheReadAbs, s.cacheCreateAbs, read, create, hasRead, hasCreate)
				}
				if s.inputTokens != 17 || s.outputCount() != 29 || !s.inputAbsolute || !s.outputAbsolute {
					t.Fatalf("absolute usage extension lost: %v", s.anthropicUsage())
				}
			}
			if err := s.accept(wireEvent{data: object{"usage": seed}}); err != nil {
				t.Fatal(err)
			}
			for _, event := range []wireEvent{{data: object{"usage": nil}}, {kind: "metadataEvent", data: object{"outputTokens": json.Number("31")}}} {
				if err := s.accept(event); err != nil {
					t.Fatal(err)
				}
			}
			if s.cacheRead != 7 || s.cacheCreation != 3 || !s.cacheReadAbs || !s.cacheCreateAbs || s.outputCount() != 31 {
				t.Fatalf("null/metadata reset cache: %v", s.anthropicUsage())
			}
		})
	}
}

func TestRound25ResponseNativeThinkingNonString(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			s := newResponseState(requestOptions{protocol: protocol, stream: stream})
			for _, value := range []any{nil, false, json.Number("25"), object{"text": "private"}, []any{"private"}} {
				if err := s.accept(wireEvent{kind: "reasoningContentEvent", data: object{"text": value}}); err != nil {
					t.Errorf("protocol=%s stream=%v native thinking frame aborted: %v", protocol, stream, err)
				}
				if s.outputRunes != 0 || s.blockIndex != -1 || s.fullThinking.Len() != 0 {
					t.Fatalf("invalid native thinking changed state: %+v", s)
				}
			}
			if err := s.accept(wireEvent{kind: "reasoningContentEvent", data: object{"text": "secret"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.accept(wireEvent{data: object{"content": "answer", "contextUsagePercentage": json.Number("0")}}); err != nil {
				t.Fatal(err)
			}
			if err := s.finalize(); err != nil || s.fullThinking.String() != "secret" || s.fullText.String() != "answer" {
				t.Fatalf("valid followup lost: thinking=%q text=%q err=%v", s.fullThinking.String(), s.fullText.String(), err)
			}
		}
	}
}

func TestRound25ResponseToolScalarIDs(t *testing.T) {
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, stream := range []bool{false, true} {
			for _, id := range []any{json.Number("25"), json.Number("25.5"), json.Number("1e2"), json.Number("9007199254740993"), json.Number("1" + strings.Repeat("0", 310)), float64(25), int(25), int64(25), true, "25", json.Number("0"), json.Number("-0.0"), float64(0), int(0), int64(0), false, nil, ""} {
				falsy := id == nil || id == false || id == "" || id == json.Number("0") || id == json.Number("-0.0") || id == float64(0) || id == int(0) || id == int64(0)
				s := newResponseState(requestOptions{protocol: protocol, stream: stream})
				var emitted []object
				s.emit = func(_ string, event object) { emitted = append(emitted, event) }
				if err := s.toolEvent(object{"name": "lookup", "toolUseId": id, "input": "{}", "stop": true}); err != nil {
					t.Fatal(err)
				}
				if err := s.finalize(); err != nil {
					t.Fatal(err)
				}
				if len(s.finalTools) != 1 || !reflect.DeepEqual(s.finalTools[0].id, id) {
					t.Errorf("protocol=%s stream=%v lost scalar id=%#v tools=%v", protocol, stream, id, s.finalTools)
				}
				var got any
				if !stream {
					result := s.response()
					if protocol == "anthropic" {
						got = obj(list(result["content"])[0])["id"]
					} else {
						message := obj(obj(list(result["choices"])[0])["message"])
						got = obj(list(message["tool_calls"])[0])["id"]
					}
				} else {
					s.finishStream()
					for _, event := range emitted {
						if protocol == "anthropic" && event["type"] == "content_block_start" {
							got = obj(event["content_block"])["id"]
						} else if protocol == "openai" {
							delta := obj(obj(list(event["choices"])[0])["delta"])
							if calls := list(delta["tool_calls"]); len(calls) > 0 {
								got = obj(calls[0])["id"]
							}
						}
					}
				}
				if protocol == "anthropic" && falsy {
					if !strings.HasPrefix(str(got), "toolu_") || len(str(got)) != 30 {
						t.Errorf("Anthropic falsy id did not fallback: id=%#v got=%#v", id, got)
					}
				} else if !reflect.DeepEqual(got, id) {
					t.Errorf("protocol=%s stream=%v output id=%#v want=%#v", protocol, stream, got, id)
				}
				if encoded, err := json.Marshal(object{"id": got}); err != nil || !json.Valid(encoded) {
					t.Fatalf("id output is not JSON: err=%v", err)
				}
			}
		}
	}
}

func TestRound25ResponseToolIDDedup(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b any
		want int
	}{
		{"int-float", json.Number("1"), json.Number("1.0"), 1},
		{"int-exponent", json.Number("1"), json.Number("1e0"), 1},
		{"int-bool", json.Number("1"), true, 1},
		{"int-string", json.Number("1"), "1", 2},
		{"bool-string", true, "true", 2},
		{"distinct-numbers", json.Number("1"), json.Number("2"), 2},
		{"large-int-rounded-float", json.Number("9007199254740993"), json.Number("9007199254740993.0"), 2},
		{"large-int-equal-float", json.Number("9007199254740992"), json.Number("9007199254740993.0"), 1},
		{"large-distinct-ints", json.Number("9007199254740992"), json.Number("9007199254740993"), 2},
		{"falsy-number-bool", json.Number("0"), false, 2},
		{"falsy-null-empty", nil, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newResponseState(requestOptions{protocol: "openai"})
			for i, id := range []any{tc.a, tc.b} {
				args := "{}"
				if i == 1 {
					args = `{"x": 25}`
				}
				if err := s.toolEvent(object{"name": "lookup", "toolUseId": id, "input": args, "stop": true}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.finalize(); err != nil {
				t.Fatal(err)
			}
			if len(s.finalTools) != tc.want {
				t.Fatalf("dedup count=%d want=%d ids=%#v/%#v", len(s.finalTools), tc.want, tc.a, tc.b)
			}
			last := s.finalTools[len(s.finalTools)-1]
			if !reflect.DeepEqual(last.id, tc.b) || last.args != `{"x": 25}` {
				t.Fatalf("better arguments/id not retained: id=%#v args=%q", last.id, last.args)
			}
		})
	}
}

func TestRound25ResponseToolIDTruncation(t *testing.T) {
	const stringID = "round25-string-truncated"
	popToolTruncation(stringID)
	defer popToolTruncation(stringID)
	for _, id := range []any{json.Number("250025"), true, nil, stringID} {
		truncationStore.Lock()
		before := len(truncationStore.tools)
		truncationStore.Unlock()
		s := newResponseState(requestOptions{protocol: "openai"})
		if err := s.toolEvent(object{"name": "lookup", "toolUseId": id, "input": `{"x":`, "stop": true}); err != nil {
			t.Fatal(err)
		}
		if err := s.finalize(); err != nil {
			t.Fatal(err)
		}
		truncationStore.Lock()
		after := len(truncationStore.tools)
		truncationStore.Unlock()
		if id == stringID {
			if after != before+1 || !popToolTruncation(stringID) {
				t.Fatal("string truncation registration lost")
			}
		} else if after != before {
			t.Errorf("non-string id registered truncation: id=%#v before=%d after=%d", id, before, after)
		}
	}
}

func TestRound26ResponseArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []string
		want []string
	}{
		{"unicode", []string{`{"中":"中文😀","a":"é"}`, `{"\u4e2d":"\u4e2d\u6587\ud83d\ude00","a":"\u00e9"}`}, []string{`{"中": "中文😀", "a": "é"}`}},
		{"key-order", []string{`{"中":1,"a":2}`, `{"a":2,"中":1}`}, []string{`{"中": 1, "a": 2}`, `{"a": 2, "中": 1}`}},
		{"duplicate-keys", []string{`{"中":1,"a":2,"\u4e2d":"文"}`, `{"中":"文","a":2}`}, []string{`{"中": "文", "a": 2}`}},
		{"isolated-surrogates", []string{`{"\ud800":"\ud801","\ud802":"中"}`, `{"\ud803":"\ud801","\ud802":"中"}`}, []string{`{"\ud800": "\ud801", "\ud802": "中"}`, `{"\ud803": "\ud801", "\ud802": "中"}`}},
	} {
		for _, protocol := range []string{"openai", "anthropic"} {
			for _, path := range []struct {
				name, policy string
				stream       bool
			}{{"ordinary-collect", "", false}, {"ordinary-stream", "", true}, {"strict-collect", "required", false}, {"strict-stream", "required", true}} {
				t.Run(tc.name+"/"+protocol+"/"+path.name, func(t *testing.T) {
					s := newResponseState(requestOptions{protocol: protocol, policyMode: path.policy, stream: path.stream, inputEstimate: 17})
					s.outputRunes, s.terminal = 13, true
					usage := s.openAIUsage()
					s.options.allowedTools = map[string]bool{"lookup": true}
					var emitted []object
					s.emit = func(_ string, event object) { emitted = append(emitted, event) }
					var inputs []string
					for i, raw := range tc.raw {
						if err := s.toolEvent(object{"name": "lookup", "toolUseId": strings.Repeat("a", i+1), "input": raw, "stop": true}); err != nil {
							t.Fatal(err)
						}
						ft := s.tools[i]
						ascii, _ := normalizeOrderedJSON(raw, true)
						if ft.args != ascii {
							t.Fatalf("native arguments changed before dedup: %q want=%q", ft.args, ascii)
						}
						inputs = append(inputs, jsonText(ft.input))
					}
					if err := s.finalize(); err != nil {
						t.Fatal(err)
					}
					if len(s.finalTools) != len(tc.want) || s.toolCount != len(tc.want) {
						t.Fatalf("dedup count=%d want=%d", len(s.finalTools), len(tc.want))
					}
					for i, ft := range s.finalTools {
						want := tc.want[i]
						if path.policy == "" {
							want, _ = normalizeOrderedJSON(want, true)
						}
						if ft.args != want || jsonText(ft.input) != inputs[i] {
							t.Errorf("args=%q want=%q input=%s original=%s", ft.args, want, jsonText(ft.input), inputs[i])
						}
					}
					if !reflect.DeepEqual(s.openAIUsage(), usage) {
						t.Errorf("argument normalization changed token counts/source: got=%v want=%v", s.openAIUsage(), usage)
					}
					if protocol == "anthropic" && path.stream {
						var partials []string
						for _, event := range emitted {
							delta := obj(event["delta"])
							if delta["type"] == "input_json_delta" {
								partials = append(partials, str(delta["partial_json"]))
							}
						}
						if !reflect.DeepEqual(partials, tc.want) {
							t.Errorf("UTF-8 partial arguments=%q want=%q", partials, tc.want)
						}
					}
					if protocol == "openai" {
						var calls []any
						if path.stream {
							s.finishStream()
							for _, event := range emitted {
								delta := obj(obj(list(event["choices"])[0])["delta"])
								if got := list(delta["tool_calls"]); len(got) > 0 {
									calls = got
								}
							}
						} else {
							calls = list(obj(obj(list(s.response()["choices"])[0])["message"])["tool_calls"])
						}
						if len(calls) != len(s.finalTools) {
							t.Fatalf("output calls=%v", calls)
						}
						for i, call := range calls {
							if obj(obj(call)["function"])["arguments"] != s.finalTools[i].args {
								t.Errorf("output arguments diverged: %v", call)
							}
						}
					}
				})
			}
		}
	}
	t.Run("strict-native-ascii-length-dedup", func(t *testing.T) {
		s := newResponseState(requestOptions{protocol: "openai", policyMode: "required", allowedTools: map[string]bool{"lookup": true}})
		for _, raw := range []string{`{"x":"中文"}`, `{"long":123456}`} {
			if err := s.toolEvent(object{"name": "lookup", "toolUseId": "same-id", "input": raw, "stop": true}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.finalize(); err != nil {
			t.Fatal(err)
		}
		if len(s.finalTools) != 1 || s.finalTools[0].args != `{"x": "中文"}` || obj(s.finalTools[0].input)["x"] != "中文" {
			t.Fatalf("native ASCII length selection changed: tools=%v", s.finalTools)
		}
	})
}

func TestRound26ResponseTruncationOutputID(t *testing.T) {
	for _, path := range []struct {
		name, protocol, policy string
		stream                 bool
	}{
		{"anthropic-stream", "anthropic", "", true}, {"anthropic-collect", "anthropic", "", false},
		{"openai-stream", "openai", "", true}, {"openai-collect", "openai", "", false},
		{"strict-anthropic-stream", "anthropic", "required", true}, {"strict-anthropic-collect", "anthropic", "required", false},
		{"strict-openai-stream", "openai", "required", true}, {"strict-openai-collect", "openai", "required", false},
	} {
		for _, tc := range []struct {
			name string
			id   any
		}{{"empty", ""}, {"zero", json.Number("0")}, {"false", false}, {"nil", nil}, {"truthy-number", json.Number("26")}, {"truthy-bool", true}, {"string", "round26-truncated-" + path.name}} {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				original, isString := tc.id.(string)
				popToolTruncation(original)
				t.Cleanup(func() { popToolTruncation(original) })
				s := newResponseState(requestOptions{protocol: path.protocol, policyMode: path.policy, stream: path.stream, allowedTools: map[string]bool{"lookup": true}})
				var emitted []object
				s.emit = func(_ string, event object) { emitted = append(emitted, event) }
				if err := s.toolEvent(object{"name": "lookup", "toolUseId": tc.id, "input": `{"x":`, "stop": true}); err != nil {
					t.Fatal(err)
				}
				if err := s.finalize(); path.policy != "" {
					var violation *toolViolation
					if !errors.As(err, &violation) || popToolTruncation(original) {
						t.Fatalf("strict malformed tool: err=%v", err)
					}
					return
				} else if err != nil {
					t.Fatal(err)
				}
				var outputID any
				if path.protocol == "anthropic" {
					if path.stream {
						for _, event := range emitted {
							if event["type"] == "content_block_start" && obj(event["content_block"])["type"] == "tool_use" {
								outputID = obj(event["content_block"])["id"]
							}
						}
					} else {
						outputID = obj(list(s.response()["content"])[0])["id"]
					}
				} else {
					outputID = s.finalTools[0].id
				}
				id, outputIsString := outputID.(string)
				t.Cleanup(func() { popToolTruncation(id) })
				want := outputIsString && id != "" && (path.protocol == "openai" && isString || path.protocol == "anthropic" && path.stream)
				if path.protocol == "anthropic" && !toolIDTruthy(tc.id) && (!strings.HasPrefix(id, "toolu_") || len(id) != 30) {
					t.Fatalf("missing fallback output id: %#v", outputID)
				}
				for attempt := 0; attempt < 2; attempt++ {
					content := object{"text": "result"}
					injectTruncationNotices([]message{{role: "user", results: []any{object{"toolUseId": outputID, "content": []any{content}}}}})
					got := strings.Contains(str(content["text"]), truncationToolNotice)
					if got != (want && attempt == 0) {
						t.Errorf("output id=%#v attempt=%d notice=%v want=%v", outputID, attempt, got, want && attempt == 0)
					}
				}
				if popToolTruncation(id) || popToolTruncation(original) || !reflect.DeepEqual(s.finalTools[0].id, tc.id) {
					t.Fatal("truncation not consumed or native ID mutated")
				}
			})
		}
	}
}

func TestRound26ResponseAliasDedupMatrix(t *testing.T) {
	const name = "mcp.lookup"
	registerToolNames([]string{name})
	alias := aliasToolName(name)
	for _, protocol := range []string{"anthropic", "openai"} {
		for _, path := range []struct {
			name, policy string
			stream       bool
		}{{"ordinary-stream", "", true}, {"ordinary-collect", "", false}, {"strict-stream", "named", true}, {"strict-collect", "named", false}} {
			for _, bracket := range []bool{false, true} {
				label := "native-only"
				if bracket {
					label = "with-bracket"
				}
				t.Run(protocol+"/"+path.name+"/"+label, func(t *testing.T) {
					s := newResponseState(requestOptions{protocol: protocol, stream: path.stream, policyMode: path.policy, policyTool: name, allowedTools: map[string]bool{name: true}})
					var emitted []object
					s.emit = func(_ string, event object) { emitted = append(emitted, event) }
					for i, wireName := range []string{alias, name} {
						if err := s.toolEvent(object{"name": wireName, "toolUseId": []string{"a", "b"}[i], "input": "{}", "stop": true}); err != nil {
							t.Fatal(err)
						}
						if s.tools[i].name != wireName {
							t.Errorf("native alias restored before raw dedup: name=%q wire=%q", s.tools[i].name, wireName)
						}
					}
					if bracket {
						if err := s.emitBlock("text", "[Called "+alias+" with args: {}]", false); err != nil {
							t.Fatal(err)
						}
					}
					if err := s.finalize(); err != nil {
						t.Fatal(err)
					}
					want := 2
					if path.policy == "" && protocol == "openai" || bracket && (path.policy != "" || !path.stream) {
						want = 1
					} else if bracket {
						want = 3
					}
					if len(s.finalTools) != want || s.toolCount != want {
						t.Fatalf("final tools=%d want=%d", len(s.finalTools), want)
					}
					if s.finalTools[0].id != "a" || want > 1 && s.finalTools[1].id != "b" {
						t.Fatalf("native call order/IDs changed: tools=%v", s.finalTools)
					}
					for _, ft := range s.finalTools {
						if ft.name != name {
							t.Fatalf("output name not restored: %q", ft.name)
						}
					}
					if protocol == "anthropic" && path.stream {
						count := 0
						for _, event := range emitted {
							block := obj(event["content_block"])
							if event["type"] == "content_block_start" && block["type"] == "tool_use" {
								count++
								if block["name"] != name {
									t.Errorf("stream name not restored: %v", block)
								}
							}
						}
						if count != want {
							t.Errorf("output blocks=%d want=%d", count, want)
						}
					}
				})
			}
		}
	}
}

// 空 seed 与旧 IDE 公式逐字节一致;不同账号名派生不同指纹,同账号名稳定。
func TestFingerprintForSeedIsolation(t *testing.T) {
	hostname, _ := os.Hostname()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	legacy := sha256.Sum256([]byte(hostname + "-" + username + "-kiro-gateway"))
	if got := FingerprintFor(""); got != hex.EncodeToString(legacy[:]) {
		t.Fatalf("empty seed changed legacy formula: %s", got)
	}
	a, b, a2 := FingerprintFor("kiro-1"), FingerprintFor("kiro-2"), FingerprintFor("kiro-1")
	if a == b {
		t.Fatalf("different accounts share fingerprint: %s", a)
	}
	if a != a2 {
		t.Fatalf("same account fingerprint unstable: %s vs %s", a, a2)
	}
	if a == FingerprintFor("") {
		t.Fatalf("seeded fingerprint collides with base device")
	}
	if !strings.HasSuffix(ChatUserAgentFor("kiro-1"), a) || !strings.HasSuffix(IDEUserAgentFor("kiro-1"), a) {
		t.Fatalf("UA builders not using seeded fingerprint")
	}
	if HeadersFor("t", "", "kiro-1")["User-Agent"] != ChatUserAgentFor("kiro-1") {
		t.Fatalf("HeadersFor UA mismatch")
	}
}
