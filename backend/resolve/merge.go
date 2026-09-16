package resolve

import (
	"encoding/json"
	"fmt"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// MergeParams 按「默认参数 ← 调用方参数 ← 覆盖参数」的顺序合并。
// object 递归合并，其余类型（含数组）整体替换——数组做元素级合并没有
// 可预测语义，替换更容易被调用方理解。
func MergeParams(defaults, client, overrides json.RawMessage) (json.RawMessage, error) {
	acc := map[string]any{}
	for _, layer := range []struct {
		raw   json.RawMessage
		field string
	}{
		{defaults, "defaults"},
		{client, "params"},
		{overrides, "overrides"},
	} {
		obj, err := decodeObject(layer.raw, layer.field)
		if err != nil {
			return nil, err
		}
		acc = mergeObjects(acc, obj)
	}
	out, err := json.Marshal(acc)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidJSON, "encode merged params", err)
	}
	return out, nil
}

func decodeObject(raw json.RawMessage, field string) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, apperr.New(apperr.InvalidJSON, fmt.Sprintf("%s must be a json object: %v", field, err))
	}
	return obj, nil
}

// mergeObjects 把 override 叠加到 base 上，返回新 map，不改动入参。
func mergeObjects(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		existing, ok := out[k]
		if !ok {
			out[k] = v
			continue
		}
		baseChild, baseIsObj := existing.(map[string]any)
		overChild, overIsObj := v.(map[string]any)
		if baseIsObj && overIsObj {
			out[k] = mergeObjects(baseChild, overChild)
			continue
		}
		out[k] = v
	}
	return out
}
