package chat

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
	"github.com/aceaura/model-surge-upstream/backend/codex"
	"github.com/aceaura/model-surge-upstream/backend/kiro"
	"github.com/aceaura/model-surge-upstream/backend/provider"
	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

// "aGVsbG8=" = "hello"，一张合法的小图占位。
func tinyImage() ImageAttachment {
	return ImageAttachment{Mime: "image/png", Data: "aGVsbG8="}
}

// TestValidateImages 锁定待发消息的校验边界：纯图可发、同空拒绝、
// 张数/mime/base64/大小各有上限，且全部归为 InvalidRequest。
func TestValidateImages(t *testing.T) {
	oversize := ImageAttachment{
		Mime: "image/png",
		Data: base64.StdEncoding.EncodeToString(make([]byte, maxImageBytes+1)),
	}
	cases := []struct {
		name    string
		content string
		images  []ImageAttachment
		wantErr bool
	}{
		{name: "纯文本", content: "你好"},
		{name: "纯图片", images: []ImageAttachment{tinyImage()}},
		{name: "图文混合", content: "看图", images: []ImageAttachment{tinyImage()}},
		{name: "同空拒绝", wantErr: true},
		{name: "张数超限", content: "x",
			images:  []ImageAttachment{tinyImage(), tinyImage(), tinyImage(), tinyImage(), tinyImage()},
			wantErr: true},
		{name: "mime 白名单外", content: "x",
			images:  []ImageAttachment{{Mime: "image/svg+xml", Data: "aGVsbG8="}},
			wantErr: true},
		{name: "base64 非法", content: "x",
			images:  []ImageAttachment{{Mime: "image/png", Data: "!!!"}},
			wantErr: true},
		{name: "空图片", content: "x",
			images:  []ImageAttachment{{Mime: "image/png", Data: ""}},
			wantErr: true},
		{name: "单张超限", content: "x",
			images:  []ImageAttachment{oversize},
			wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImages(tc.content, tc.images)
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望校验失败")
				}
				if !apperr.Is(err, apperr.InvalidRequest) {
					t.Fatalf("错误码 = %v，期望 invalid_request", apperr.CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("期望通过，得到 %v", err)
			}
		})
	}
}

type chatResolveFunc func(context.Context, string) (resolve.ResolvedTarget, error)

func (f chatResolveFunc) Resolve(ctx context.Context, modelID string) (resolve.ResolvedTarget, error) {
	return f(ctx, modelID)
}

type chatInvalidateFunc func(string, string)

func (f chatInvalidateFunc) Invalidate(name, token string) { f(name, token) }

func chatKiroFrame(event, payload string) []byte {
	var headers []byte
	for _, pair := range [][2]string{{":message-type", "event"}, {":event-type", event}, {":content-type", "application/json"}} {
		headers = append(headers, byte(len(pair[0])))
		headers = append(headers, pair[0]...)
		headers = append(headers, 7, byte(len(pair[1])>>8), byte(len(pair[1])))
		headers = append(headers, pair[1]...)
	}
	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame[:4], uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, headers...)
	frame = append(frame, payload...)
	return binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
}

func TestServiceCompleteKiroTokenRefresh(t *testing.T) {
	for _, protocol := range []string{provider.ProtocolAnthropic, provider.ProtocolChatCompletions} {
		for _, outcome := range []string{"success", "forbidden", "resolve failure"} {
			t.Run(protocol+"/"+outcome, func(t *testing.T) {
				const oldToken, freshToken = "secret-old-access", "secret-fresh-access"
				var bodies []map[string]any
				var auths []string
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/generateAssistantResponse" || r.Method != http.MethodPost {
						t.Errorf("not native Kiro POST: %s %s", r.Method, r.URL.Path)
					}
					if r.Header.Get(kiro.HeaderProvider) != "" || r.Header.Get(kiro.HeaderProfileARN) != "" {
						t.Error("internal routing headers escaped")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					bodies = append(bodies, body)
					auths = append(auths, r.Header.Get("Authorization"))
					if len(bodies) == 1 || outcome != "success" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprintf(w, `{"message":"%s %s"}`, oldToken, freshToken)
						return
					}
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					_, _ = w.Write(chatKiroFrame("assistantResponseEvent", `{"content":"abcdefgh"}`))
					_, _ = w.Write(chatKiroFrame("metadataEvent", `{"usage":2.5}`))
					_, _ = w.Write(chatKiroFrame("metadataEvent", `{"contextUsagePercentage":1}`))
					_, _ = w.Write(chatKiroFrame("messageStopEvent", `{"stopReason":"end_turn"}`))
				}))
				defer up.Close()
				initial := resolve.ResolvedTarget{
					ModelID: "resolved-alias", Account: "kiro-old", ProviderID: kiro.ProviderID,
					Protocol: protocol, BaseURL: up.URL, NativeModel: "claude-sonnet-4.5",
					Headers:  kiro.Headers(oldToken, "arn:aws:codewhisperer:us-east-1:123:profile/test"),
					Defaults: json.RawMessage(`{"temperature":0.3}`), Overrides: json.RawMessage(`{"top_p":0.7}`),
					EffortEnabled: true,
				}
				fresh := initial
				fresh.Account = "kiro-fresh"
				fresh.Headers = kiro.Headers(freshToken, initial.Headers[kiro.HeaderProfileARN])
				invalidations, resolves := 0, 0
				inv := chatInvalidateFunc(func(name, token string) {
					invalidations++
					if name != initial.Account || token != oldToken {
						t.Error("invalidation did not use first target's account and bearer token")
					}
				})
				resolver := chatResolveFunc(func(ctx context.Context, modelID string) (resolve.ResolvedTarget, error) {
					resolves++
					if invalidations != 1 || modelID != "selected-model" || ctx.Err() != nil {
						t.Error("resolve must follow invalidation and use original selected model ID/context")
					}
					if outcome == "resolve failure" {
						return resolve.ResolvedTarget{}, errors.New("refresh failed: " + oldToken + " " + freshToken)
					}
					return fresh, nil
				})
				var records []UsageRecord
				svc := NewService(nil, resolver, func(ctx context.Context, rec UsageRecord) {
					if ctx.Done() != nil {
						t.Error("usage context must be detached")
					}
					records = append(records, rec)
				})
				if svc.WithTokenInvalidator(inv) != svc {
					t.Fatal("WithTokenInvalidator must return the service")
				}
				hist := history()
				hist[2].Attachments = []ImageAttachment{tinyImage()}
				before, _ := json.Marshal(hist)
				reply, final, err := svc.complete(context.Background(), initial, "selected-model", "session-unchanged", "high", hist)
				wantCalls := 2
				wantTarget := fresh
				wantStatus, wantInput, wantOutput := http.StatusOK, int64(1997), int64(3)
				if outcome == "forbidden" {
					wantStatus, wantInput, wantOutput = http.StatusForbidden, 0, 0
				}
				if outcome == "resolve failure" {
					wantCalls, wantTarget = 1, initial
					wantStatus, wantInput, wantOutput = http.StatusForbidden, 0, 0
				}
				if len(bodies) != wantCalls || invalidations != 1 || resolves != 1 {
					t.Fatalf("HTTP calls=%d invalidations=%d resolves=%d; want %d/1/1", len(bodies), invalidations, resolves, wantCalls)
				}
				if auths[0] != "Bearer "+oldToken || (wantCalls == 2 && auths[1] != "Bearer "+freshToken) {
					t.Error("retry did not use refreshed bearer token")
				}
				if !reflect.DeepEqual(final, wantTarget) || len(records) != 1 {
					t.Fatalf("final target matches=%v records=%d", reflect.DeepEqual(final, wantTarget), len(records))
				}
				rec := records[0]
				if !reflect.DeepEqual(rec.Target, wantTarget) || rec.StatusCode != wantStatus || rec.Usage.InputTokens != wantInput || rec.Usage.OutputTokens != wantOutput || rec.DurationMS < 0 {
					t.Errorf("final usage: status=%d input=%d output=%d target matches=%v", rec.StatusCode, rec.Usage.InputTokens, rec.Usage.OutputTokens, reflect.DeepEqual(rec.Target, wantTarget))
				}
				if outcome == "success" {
					if err != nil || reply != "abcdefgh" || rec.ErrorMessage != "" {
						t.Fatalf("reply=%q err=%v", reply, err)
					}
				} else {
					if err == nil || reply != "" || rec.ErrorMessage != err.Error() {
						t.Fatal("failed completion must return and record an error")
					}
					for _, token := range []string{oldToken, freshToken} {
						if strings.Contains(fmt.Sprintf("%+v", err), token) || strings.Contains(rec.ErrorMessage, token) {
							t.Error("credential leaked in returned/recorded error")
						}
					}
				}
				after, _ := json.Marshal(hist)
				if string(before) != string(after) {
					t.Error("completion mutated caller history")
				}
				for _, body := range bodies {
					state := body["conversationState"].(map[string]any)
					if len(state["history"].([]any)) != 2 {
						t.Error("history duplicated or lost")
					}
					current := state["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
					if text, _ := current["content"].(string); !strings.Contains(text, "<thinking_effort>high</thinking_effort>") || !strings.HasSuffix(text, "1+1?") {
						t.Error("selected effort/current user message changed")
					}
					if len(current["images"].([]any)) != 1 {
						t.Error("image attachment lost")
					}
					// Native conversion generates a new conversation ID for every Complete call.
					delete(state, "conversationId")
				}
				if wantCalls == 2 && !reflect.DeepEqual(bodies[0], bodies[1]) {
					t.Error("retry changed native request body/history/effort")
				}
			})
		}
	}
}

func TestServiceCompleteNoTokenRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, providerID, protocol string
		status                     int
		nilInvalidator             bool
	}{
		{"nil invalidator", kiro.ProviderID, provider.ProtocolChatCompletions, 403, true},
		{"Kiro 401", kiro.ProviderID, provider.ProtocolChatCompletions, 401, false},
		{"Kiro 400", kiro.ProviderID, provider.ProtocolChatCompletions, 400, false},
		{"Kiro 200", kiro.ProviderID, provider.ProtocolChatCompletions, 200, false},
		{"other provider 403", "openai", provider.ProtocolChatCompletions, 403, false},
		{"Codex 403", codex.ProviderID, provider.ProtocolResponses, 403, false},
		{"Codex 401", codex.ProviderID, provider.ProtocolResponses, 401, false},
		{"Bailian 403", "bailian.cn.subscribe.coding-plan", provider.ProtocolChatCompletions, 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, resolves, invalidations, records := 0, 0, 0, 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.status == 200 {
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					_, _ = w.Write(chatKiroFrame("assistantResponseEvent", `{"content":"ok"}`))
					_, _ = w.Write(chatKiroFrame("metadataEvent", `{"contextUsagePercentage":1}`))
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":"denied"}`)
			}))
			defer up.Close()
			initial := target(tc.protocol)
			initial.ProviderID, initial.Account, initial.BaseURL = tc.providerID, "account", up.URL
			initial.Headers = map[string]string{"Authorization": "Bearer old-access"}
			if tc.providerID == kiro.ProviderID {
				initial.NativeModel = "claude-sonnet-4.5"
				initial.Headers = kiro.Headers("old-access", "")
			}
			svc := NewService(nil, chatResolveFunc(func(context.Context, string) (resolve.ResolvedTarget, error) {
				resolves++
				return initial, nil
			}), func(_ context.Context, rec UsageRecord) {
				records++
				if rec.StatusCode != tc.status || !reflect.DeepEqual(rec.Target, initial) {
					t.Error("non-retried usage target/status changed")
				}
			})
			if !tc.nilInvalidator {
				svc.WithTokenInvalidator(chatInvalidateFunc(func(string, string) { invalidations++ }))
			}
			reply, _, err := svc.complete(context.Background(), initial, initial.ModelID, "session", "", history())
			if (err == nil) != (tc.status == 200) || (tc.status == 200 && reply != "ok") {
				t.Fatalf("reply=%q err=%v", reply, err)
			}
			if calls != 1 || resolves != 0 || invalidations != 0 || records != 1 {
				t.Fatalf("HTTP/resolve/invalidate/record = %d/%d/%d/%d, want 1/0/0/1", calls, resolves, invalidations, records)
			}
		})
	}
}
