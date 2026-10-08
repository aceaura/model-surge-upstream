package proxyplane

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// rectifierConfig 是模型 rectifier 字段的运行期形态:kiro 上游 200 但
// 通篇无正文、以拒答(content_filter/refusal)收尾时自动重发同一请求。
// 只有「正文流出之前」的拒答能透明重试——字节一旦发给客户端就收不回,
// 思考/工具调用流出后的中途拒答仍按原样透传。
type rectifierConfig struct {
	Enabled         bool    `json:"enabled"`
	Retries         int     `json:"retries"`
	IntervalSeconds float64 `json:"interval_seconds"`
}

// parseRectifier 读出有效配置:enabled 缺省 false;开启时 retries/interval
// 缺省回落 2 次/2 秒。非开启态返回值不参与决策。
func parseRectifier(raw json.RawMessage) rectifierConfig {
	cfg := rectifierConfig{}
	_ = json.Unmarshal(raw, &cfg)
	if cfg.Enabled {
		if cfg.Retries <= 0 {
			cfg.Retries = 2
		}
		if cfg.IntervalSeconds <= 0 {
			cfg.IntervalSeconds = 2
		}
	}
	return cfg
}

// sniffVerdict 是预读响应后的判定。
type sniffVerdict int

const (
	// verdictPass 非拒答(或无法判定):把预读字节原样放行。
	verdictPass sniffVerdict = iota
	// verdictRefusal 通篇无正文且以拒答收尾:可安全重试。
	verdictRefusal
)

// sniffCap 是拒答判定预读的上限。拒答流极短(几百字节);预读超过上限
// 仍未见正文属异常形态,直接放行,不为判定卡住正常大响应。
const sniffCap = 256 << 10

// replayBody 是预读字节与底层响应体拼接出的重放 reader:Close 必须
// 落到真正的响应体上,调用方拿它整体替换 resp.Body。
type replayBody struct {
	io.Reader
	closer io.Closer
}

func (b *replayBody) Close() error { return b.closer.Close() }

// sniffRefusal 预读响应体判定「空拒答」:流式按 SSE 事件增量解析,见到
// 正文(文本/思考/工具调用)即放行并回拼接好的重放 reader;流尽仍无正文
// 且收尾原因是 content_filter/refusal 才判拒答。非流式读全量 JSON 判定。
// 返回的 reader 含已预读字节,调用方无论判定如何都必须用它替换 resp.Body。
func sniffRefusal(resp io.ReadCloser, contentType string) (sniffVerdict, io.ReadCloser) {
	if strings.Contains(contentType, "text/event-stream") {
		return sniffStream(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp, sniffCap+1))
	if err != nil {
		_ = resp.Close()
		return verdictPass, io.NopCloser(bytes.NewReader(body))
	}
	if len(body) > sniffCap {
		// 超上限不做判定:预读部分与残余体拼接放行,不截断正常响应。
		return verdictPass, &replayBody{Reader: io.MultiReader(bytes.NewReader(body), resp), closer: resp}
	}
	_ = resp.Close()
	return sniffJSON(body), io.NopCloser(bytes.NewReader(body))
}

// sniffJSON 判定非流式响应:两族的拒答都是 finish/stop 原因为拒答值且
// 正文为空。
func sniffJSON(body []byte) sniffVerdict {
	var probe struct {
		// openai 形态
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string          `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		// anthropic 形态
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return verdictPass
	}
	for _, c := range probe.Choices {
		if c.FinishReason == "content_filter" && c.Message.Content == "" && len(c.Message.ToolCalls) == 0 {
			return verdictRefusal
		}
	}
	if probe.StopReason == "refusal" && len(probe.Content) == 0 {
		return verdictRefusal
	}
	return verdictPass
}

// streamProbe 是 SSE 增量解析的累积状态:见到任一正文事件即结论放行,
// 拒答收尾原因先记账,流尽时无正文才升级为拒答判定。
type streamProbe struct {
	hasContent bool
	refusal    bool
}

// feed 消费一个 data: 载荷(不含 "data: " 前缀)。
func (p *streamProbe) feed(data []byte) {
	var ev struct {
		Type string `json:"type"`
		// openai chunk
		Choices []struct {
			Delta struct {
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		// anthropic message_delta
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return
	}
	for _, c := range ev.Choices {
		if c.Delta.Content != "" || c.Delta.ReasoningContent != "" || len(c.Delta.ToolCalls) > 0 {
			p.hasContent = true
		}
		if c.FinishReason == "content_filter" {
			p.refusal = true
		}
	}
	switch ev.Type {
	case "content_block_delta":
		// anthropic 正文/思考/工具输入都走 content_block_delta。
		p.hasContent = true
	case "message_delta":
		if ev.Delta.StopReason == "refusal" {
			p.refusal = true
		}
	}
}

// sniffStream 逐行预读 SSE:正文事件出现即放行(已发字节收不回,中途
// 拒答不重试);流到 [DONE]/EOF 仍无正文且记到拒答收尾才判拒答。
func sniffStream(resp io.ReadCloser) (sniffVerdict, io.ReadCloser) {
	br := bufio.NewReader(resp)
	var buf bytes.Buffer
	probe := streamProbe{}
	for buf.Len() <= sniffCap {
		line, err := br.ReadBytes('\n')
		buf.Write(line)
		if d, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			d = bytes.TrimSpace(d)
			if bytes.Equal(d, []byte("[DONE]")) {
				if probe.refusal && !probe.hasContent {
					_ = resp.Close()
					return verdictRefusal, io.NopCloser(&buf)
				}
			} else {
				probe.feed(d)
			}
		}
		if probe.hasContent {
			break
		}
		if err != nil {
			// EOF/断流:流尽无正文且记到拒答 → 拒答;否则按现状放行。
			_ = resp.Close()
			if probe.refusal && !probe.hasContent {
				return verdictRefusal, io.NopCloser(&buf)
			}
			return verdictPass, io.NopCloser(&buf)
		}
	}
	// 已见正文(或预读超限):预读字节与残余流拼接放行。必须继续经由 br
	// 读——bufio 已预读的缓冲字节不在 resp 的读游标里,直接拼 resp 会丢。
	return verdictPass, &replayBody{Reader: io.MultiReader(&buf, br), closer: resp}
}
