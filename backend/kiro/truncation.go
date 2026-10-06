package kiro

import (
	"crypto/sha256"
	"encoding/hex"
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

// looksTruncatedJSON is the brace/bracket balance + unclosed-string heuristic
// from parsers.py:_diagnose_json_truncation.
func looksTruncatedJSON(s string) bool {
	depth := 0
	inString := false
	escaped := false
	for _, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch r {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth > 0 || inString
}

// parseBracketToolCalls extracts [Called name with args: {...}] calls that some
// models emit as plain text (parsers.py:parse_bracket_tool_calls).
func parseBracketToolCalls(text string) []object {
	if !strings.Contains(strings.ToLower(text), "[called") {
		return nil
	}
	var calls []object
	lower := strings.ToLower(text)
	for pos := 0; ; {
		idx := strings.Index(lower[pos:], "[called")
		if idx < 0 {
			return calls
		}
		start := pos + idx + len("[called")
		rest := text[start:]
		restLower := lower[start:]
		// parsers.py:115 正则 \[Called\s+(\w+)\s+with\s+args:\s* 的\s+全是
		// 强制词边界:粘连形式(如 withargs:)不匹配。
		i := 0
		for i < len(rest) && isSpace(rest[i]) {
			i++
		}
		if i == 0 {
			pos = start
			continue
		}
		nameStart := i
		for i < len(rest) && isWord(rest[i]) {
			i++
		}
		name := rest[nameStart:i]
		if name == "" {
			pos = start
			continue
		}
		ws := i
		for i < len(rest) && isSpace(rest[i]) {
			i++
		}
		if i == ws || !strings.HasPrefix(restLower[i:], "with") {
			pos = start
			continue
		}
		i += len("with")
		ws = i
		for i < len(rest) && isSpace(rest[i]) {
			i++
		}
		if i == ws || !strings.HasPrefix(restLower[i:], "args:") {
			pos = start
			continue
		}
		i += len("args:")
		for i < len(rest) && isSpace(rest[i]) {
			i++
		}
		if i >= len(rest) || rest[i] != '{' {
			pos = start + i
			continue
		}
		end := matchingBrace(rest, i)
		if end < 0 {
			pos = start + i
			continue
		}
		input, err := decodeObject(rest[i : end+1])
		if err == nil {
			calls = append(calls, object{"toolUseId": "call_" + strings.ReplaceAll(newID(), "-", ""), "name": name, "input": input})
		}
		pos = start + end + 1
	}
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
func isWord(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
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
