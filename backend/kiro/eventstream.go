package kiro

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

const maxFrameBytes = 16 << 20
const maxHeaderBytes = 64 << 10

type wireEvent struct {
	kind string
	data object
}
type eventReader struct{ r io.Reader }

// Frames are length-delimited binary messages, not delimiter-scanned JSON.
// ReadFull handles arbitrary TCP slicing and distinguishes a clean boundary
// EOF from truncation in the prelude, headers, payload or message CRC.
func (p *eventReader) next() (wireEvent, error) {
	var prelude [12]byte
	n, err := io.ReadFull(p.r, prelude[:])
	if err != nil {
		if n == 0 && err == io.EOF {
			return wireEvent{}, io.EOF
		}
		return wireEvent{}, fmt.Errorf("kiro: truncated eventstream prelude: %w", err)
	}
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:]) {
		return wireEvent{}, fmt.Errorf("kiro: eventstream prelude CRC mismatch")
	}
	total := binary.BigEndian.Uint32(prelude[:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	if total < 16 || total > maxFrameBytes || headersLen > maxHeaderBytes || headersLen > total-16 {
		return wireEvent{}, fmt.Errorf("kiro: invalid eventstream lengths (frame=%d headers=%d)", total, headersLen)
	}
	rest := make([]byte, int(total)-12)
	if _, err := io.ReadFull(p.r, rest); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return wireEvent{}, fmt.Errorf("kiro: truncated eventstream frame: %w", err)
	}
	crc := crc32.NewIEEE()
	crc.Write(prelude[:])
	crc.Write(rest[:len(rest)-4])
	if crc.Sum32() != binary.BigEndian.Uint32(rest[len(rest)-4:]) {
		return wireEvent{}, fmt.Errorf("kiro: eventstream message CRC mismatch")
	}
	headers, err := parseEventHeaders(rest[:headersLen])
	if err != nil {
		return wireEvent{}, err
	}
	raw := rest[headersLen : len(rest)-4]
	data := object{}
	if len(raw) > 0 {
		var err error
		data, err = decodeObject(string(raw))
		if err != nil {
			return wireEvent{}, fmt.Errorf("kiro: invalid event JSON: %w", err)
		}
	}
	messageType := headers[":message-type"]
	kind := headers[":event-type"]
	if messageType == "exception" || messageType == "error" || strings.HasSuffix(strings.ToLower(kind), "exception") {
		name := headers[":exception-type"]
		if name == "" {
			name = headers[":error-code"]
		}
		if name == "" {
			name = kind
		}
		detail := str(data["message"])
		if detail == "" {
			detail = str(data["Message"])
		}
		if detail == "" {
			detail = headers[":error-message"]
		}
		return wireEvent{}, fmt.Errorf("kiro: upstream %s %s: %s", messageType, name, detail)
	}
	if messageType != "event" {
		return wireEvent{}, fmt.Errorf("kiro: invalid eventstream message type %q", messageType)
	}
	if kind == "" {
		return wireEvent{}, fmt.Errorf("kiro: eventstream event type missing")
	}
	nested := obj(data[kind])
	if nested != nil {
		data = nested
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return wireEvent{}, err
		}
		raw = fields[kind]
	}
	if len(obj(data["input"])) > 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return wireEvent{}, err
		}
		data["input"] = string(fields["input"])
	}
	if _, ok := data["exception"]; ok {
		return wireEvent{}, fmt.Errorf("kiro: upstream exception: %s", jsonText(data["exception"]))
	}
	// A payload is one primary event, not simultaneous content, tool and usage events.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
	primary := ""
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return wireEvent{}, err
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return wireEvent{}, err
		}
		switch key {
		case "content", "name", "input", "stop", "followupPrompt", "usage", "contextUsagePercentage", "text", "signature":
			primary = key
		}
		if primary != "" {
			break
		}
	}
	if primary != "" {
		for _, key := range []string{"content", "name", "input", "stop", "followupPrompt", "usage", "contextUsagePercentage", "text", "signature"} {
			if key == primary || ((primary == "name" || primary == "input" || primary == "stop") && (key == "name" || key == "input" || key == "stop")) || (primary == "content" && key == "followupPrompt") {
				continue
			}
			delete(data, key)
		}
	}
	return wireEvent{kind: kind, data: data}, nil
}

func parseEventHeaders(b []byte) (map[string]string, error) {
	out := map[string]string{}
	invalid := func() (map[string]string, error) { return nil, fmt.Errorf("kiro: malformed eventstream headers") }
	for len(b) > 0 {
		size := int(b[0])
		b = b[1:]
		if size == 0 || len(b) < size+1 {
			return invalid()
		}
		key := string(b[:size])
		typ := b[size]
		b = b[size+1:]
		var n int
		switch typ {
		case 0, 1:
			n = 0
		case 2:
			n = 1
		case 3:
			n = 2
		case 4:
			n = 4
		case 5, 8:
			n = 8
		case 9:
			n = 16
		case 6, 7:
			if len(b) < 2 {
				return invalid()
			}
			n = int(binary.BigEndian.Uint16(b[:2]))
			b = b[2:]
		default:
			return invalid()
		}
		if len(b) < n {
			return invalid()
		}
		if key == ":message-type" || key == ":event-type" || key == ":exception-type" || key == ":error-code" || key == ":error-message" {
			if typ != 7 {
				return invalid()
			}
			if _, exists := out[key]; exists {
				return invalid()
			}
			out[key] = string(b[:n])
		}
		b = b[n:]
	}
	return out, nil
}

func absoluteTokens(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil || i < 0 || i > 1<<40 {
		return 0, false
	}
	return int(i), true
}
