package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
)

// extensions/tool_name_alias.py(app_entry 部署恒装):不匹配
// ^[A-Za-z0-9_-]{1,64}$ 的工具名(超长、含点号的 MCP 名、空名)生成
// t_<sha256[:12]>_<suffix> 别名上行,响应侧恢复原名。别名表进程级全局,
// 跨请求稳定(对齐 Python 的模块级字典)。
var (
	toolNamePattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	toolSuffixUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

	toolNameMu        sync.Mutex
	toolNameToAlias   = map[string]string{}
	toolNameFromAlias = map[string]string{}
	toolNameReserved  = map[string]bool{}
)

func needsToolAlias(name string) bool {
	return !toolNamePattern.MatchString(name)
}

// toolAliasSuffix 对齐 tool_name_alias.py:33-34:取最后一个 "__" 段(空则
// 全名),非法字符段压成 "_",去首尾 "_-",全空回退 "tool"。
func toolAliasSuffix(name string) string {
	src := name
	if i := strings.LastIndex(name, "__"); i >= 0 {
		src = name[i+2:]
	}
	if src == "" {
		src = name
	}
	s := strings.Trim(toolSuffixUnsafe.ReplaceAllString(src, "_"), "_-")
	if s == "" {
		s = "tool"
	}
	return s
}

// buildToolAlias 对齐 tool_name_alias.py:31-37:后缀按码点从尾部截断到
// 64 前缀剩余长度。
func buildToolAlias(name string, digestLen int) string {
	sum := sha256.Sum256([]byte(name))
	prefix := "t_" + hex.EncodeToString(sum[:])[:digestLen] + "_"
	suffix := []rune(toolAliasSuffix(name))
	if max := 64 - len(prefix); len(suffix) > max {
		suffix = suffix[len(suffix)-max:]
	}
	return prefix + string(suffix)
}

// aliasToolName 返回上行用名:合法名原样,否则取/建别名;冲突时摘要长度
// 按 16/20/24… 递增加长(tool_name_alias.py:48-55)。
func aliasToolName(name string) string {
	if !needsToolAlias(name) {
		return name
	}
	toolNameMu.Lock()
	defer toolNameMu.Unlock()
	if a, ok := toolNameToAlias[name]; ok {
		return a
	}
	alias := buildToolAlias(name, 12)
	for digestLen := 16; ; digestLen += 4 {
		orig, taken := toolNameFromAlias[alias]
		if !toolNameReserved[alias] && (!taken || orig == name) {
			break
		}
		alias = buildToolAlias(name, digestLen)
	}
	toolNameToAlias[name] = alias
	toolNameFromAlias[alias] = name
	return alias
}

// restoreToolName 响应侧恢复客户端原名;未登记的别名原样透传
// (tool_name_alias.py:63-65)。
func restoreToolName(name string) string {
	toolNameMu.Lock()
	defer toolNameMu.Unlock()
	if orig, ok := toolNameFromAlias[name]; ok {
		return orig
	}
	return name
}

// registerToolNames 对齐 _register_tool_names(tool_name_alias.py:68-78):
// 合法名先整体进保留集(防止被长名别名抢占),再逐一取别名。
func registerToolNames(names []string) {
	toolNameMu.Lock()
	for _, n := range names {
		if !needsToolAlias(n) {
			toolNameReserved[n] = true
		}
	}
	toolNameMu.Unlock()
	for _, n := range names {
		aliasToolName(n)
	}
}
