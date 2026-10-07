package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
)

// Truncation recovery (KiroaaS truncation_state.py + truncation_recovery.py):
// when the upstream cuts a generation mid-stream, the notice is stored here and
// injected into the NEXT request that carries the truncated artifact. Entries
// have no TTL and are consumed on first match (pop semantics).
const (
	truncationUserMessage = "[System Notice] Your previous response was truncated by the API due to " +
		"output size limitations. This is not an error on your part. " +
		"If you need to continue, please adapt your approach rather than repeating the same output."
	truncationToolNotice = "[API Limitation] Your tool call was truncated by the upstream API due to output size limits.\n\n" +
		"If the tool result below shows an error or unexpected behavior, this is likely a CONSEQUENCE of the truncation, " +
		"not the root cause. The tool call itself was cut off before it could be fully transmitted.\n\n" +
		"Repeating the exact same operation will be truncated again. Consider adapting your approach."
)

type toolTruncation struct{ name string }

var truncationStore = struct {
	sync.Mutex
	tools   map[string]toolTruncation
	content map[string]bool
}{tools: map[string]toolTruncation{}, content: map[string]bool{}}

func contentHash(content string) string {
	runes := []rune(content)
	if len(runes) > 500 {
		runes = runes[:500]
	}
	sum := sha256.Sum256([]byte(string(runes)))
	return hex.EncodeToString(sum[:])[:16]
}

func saveToolTruncation(id, name string) {
	if id == "" {
		return
	}
	truncationStore.Lock()
	truncationStore.tools[id] = toolTruncation{name: name}
	truncationStore.Unlock()
}

func popToolTruncation(id string) bool {
	truncationStore.Lock()
	_, ok := truncationStore.tools[id]
	delete(truncationStore.tools, id)
	truncationStore.Unlock()
	return ok
}

func saveContentTruncation(content string) {
	if content == "" {
		return
	}
	truncationStore.Lock()
	truncationStore.content[contentHash(content)] = true
	truncationStore.Unlock()
}

func popContentTruncation(content string) bool {
	if content == "" {
		return false
	}
	truncationStore.Lock()
	ok := truncationStore.content[contentHash(content)]
	delete(truncationStore.content, contentHash(content))
	truncationStore.Unlock()
	return ok
}

// looksTruncatedJSON replicates parsers.py:_diagnose_json_truncation's naive
// heuristic: brace/bracket counts ignore string context, so `{"a":1}}`
// (mismatched counts) and braces inside strings both count as truncated.
func looksTruncatedJSON(s string) bool {
	stripped := strings.TrimSpace(s)
	if stripped == "" {
		return false
	}
	if strings.HasPrefix(stripped, "{") && !strings.HasSuffix(stripped, "}") {
		return true
	}
	if strings.HasPrefix(stripped, "[") && !strings.HasSuffix(stripped, "]") {
		return true
	}
	if strings.Count(stripped, "{") != strings.Count(stripped, "}") {
		return true
	}
	if strings.Count(stripped, "[") != strings.Count(stripped, "]") {
		return true
	}
	quotes := 0
	for i := 0; i < len(stripped); i++ {
		if stripped[i] == '\\' && i+1 < len(stripped) {
			i++
			continue
		}
		if stripped[i] == '"' {
			quotes++
		}
	}
	return quotes%2 != 0
}

const bracketSpace = `[\s\p{Z}\x{0085}\x{000b}\x{001c}-\x{001f}]`

var bracketToolPattern = regexp.MustCompile(`(?i)\[Called` + bracketSpace + `+([\p{L}\p{N}_]+)` + bracketSpace + `+with` + bracketSpace + `+args:` + bracketSpace + `*`)

// parseBracketToolCalls extracts [Called name with args: {...}] calls that some
// models emit as plain text (parsers.py:parse_bracket_tool_calls).
func parseBracketToolCalls(text string) []object {
	if !strings.Contains(text, "[Called") {
		return nil
	}
	var calls []object
	for _, match := range bracketToolPattern.FindAllStringSubmatchIndex(text, -1) {
		start := match[1]
		brace := strings.IndexByte(text[start:], '{')
		if brace < 0 {
			continue
		}
		start += brace
		end := matchingBrace(text, start)
		if end < 0 {
			continue
		}
		raw := text[start : end+1]
		input, err := decodeObject(raw)
		if err == nil {
			calls = append(calls, object{"toolUseId": "call_" + strings.ReplaceAll(newID(), "-", "")[:8], "name": text[match[2]:match[3]], "input": input, "raw": raw})
		}
	}
	return calls
}

// matchingBrace finds the closing brace for the one at start, string/escape
// aware (parsers.py:find_matching_brace).
func matchingBrace(s string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
