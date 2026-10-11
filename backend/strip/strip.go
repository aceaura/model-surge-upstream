// Package strip 按管理员配置的点路径清除上行 JSON 字段。清除在请求体
// 定稿后施加(defaults/客户端参数/effort 映射/overrides/协议整形全部
// 完成之后),优先级最高,是管理员剔除上游不收字段的最终手段。
package strip

import "strings"

// Apply 按 paths 清除 body 里的字段:每条路径是点分隔的对象键
// (如 metadata.user_id),逐层下钻,末段键整键删除;删除后沿路径
// 回收变空的中层对象(与 effort 先清后写的空壳清理同口径)。路径
// 缺失或中段遇非对象值都跳过,不报错。
func Apply(body map[string]any, paths []string) {
	for _, p := range paths {
		segs := strings.Split(p, ".")
		del(body, segs)
	}
}

// del 沿 segs 下钻并删除末段键,返回 true 表示发生了删除(调用方据此
// 决定是否回收变空的父对象)。
func del(obj map[string]any, segs []string) bool {
	if len(segs) == 1 {
		if _, ok := obj[segs[0]]; !ok {
			return false
		}
		delete(obj, segs[0])
		return true
	}
	next, ok := obj[segs[0]].(map[string]any)
	if !ok {
		return false
	}
	if !del(next, segs[1:]) {
		return false
	}
	if len(next) == 0 {
		delete(obj, segs[0])
	}
	return true
}

// Valid 校验路径形态:点分隔、每段非空(不允许空路径、首尾点、连续点)。
// 段内容不限制字符——JSON 键本身可含空格等字符。
func Valid(path string) bool {
	if path == "" {
		return false
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			return false
		}
	}
	return true
}
