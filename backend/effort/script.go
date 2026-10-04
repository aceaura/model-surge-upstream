// 档位映射脚本的执行器,账号额度脚本同款 goja 机制:
// 脚本是一个 JS 对象字面量 ({ apply: function(ctx) {...} }),apply 按
// ctx.level 自行决定上行值与写入位置(映射表写在脚本体内,元数据只作
// 档位清单与界面展示),返回完整的新请求体。模型的协议内置映射(Apply)
// 只覆盖四协议官方字段,「anthropic 外壳+自家字段」这类厂商差异(kimi
// 顶层 reasoning_effort)由脚本承接:配了脚本即接管,没配走内置映射。
package effort

import (
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/aceaura/model-surge-upstream/backend/apperr"
)

// scriptTimeout 是纯映射脚本的执行上限:无网络交互,超过即中断视为失控。
// 不像额度脚本那样可配——映射在请求热路径上,必须快且确定。
const scriptTimeout = 2 * time.Second

// ScriptContext 是传给 apply 的 ctx 形参。元数据(数字档声明)只作档位
// 清单与界面展示,不参与映射——上行值由脚本按 level 自行决定。
type ScriptContext struct {
	// Level 是命中的数字档号(如 "2");对话页按值选档时反查档号,
	// 查不到(值不在声明里)传空串。
	Level string
	// Protocol 是出站协议(anthropic/chat_completions/responses/gemini)。
	Protocol string
	// Efforts 是模型的有效数字档声明 [{name,value}],仅供脚本参考
	// (如按档号取展示名),映射表应写在脚本体内。
	Efforts []Entry
	// Request 是当前请求体。转发面在 defaults/overrides 合并前调用
	// (脚本结果仍可被 overrides 压盖);对话页在 defaults 合并后、
	// overrides 合并前调用,与内置映射的落点完全一致。
	Request map[string]any
}

// ValidateScript 保存期预检:脚本必须能求值为带 apply 函数的对象。
// 语法错误在此挡住,不放到请求路径上炸。
func ValidateScript(code string) error {
	vm := goja.New()
	timer := time.AfterFunc(scriptTimeout, func() { vm.Interrupt("effort script timeout") })
	defer timer.Stop()
	_, err := loadApply(vm, code, apperr.InvalidRequest)
	return err
}

// RunScript 执行映射脚本并返回新请求体。apply 必须返回对象(完整请求体,
// 通常在 ctx.request 上改完原样返回);返回非对象、抛异常或超时都算失败。
func RunScript(code string, sc ScriptContext) (map[string]any, error) {
	vm := goja.New()
	timer := time.AfterFunc(scriptTimeout, func() { vm.Interrupt("effort script timeout") })
	defer timer.Stop()
	apply, err := loadApply(vm, code, apperr.EffortScriptFailed)
	if err != nil {
		return nil, err
	}
	ctx := vm.ToValue(map[string]any{
		"level":    sc.Level,
		"protocol": sc.Protocol,
		"efforts":  sc.Efforts,
		"request":  sc.Request,
	})
	res, err := apply(goja.Undefined(), ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.EffortScriptFailed, "effort script apply", err)
	}
	exported, ok := res.Export().(map[string]any)
	if !ok {
		return nil, apperr.New(apperr.EffortScriptFailed,
			"effort script apply must return the request object")
	}
	// 脚本在 ctx.request 上原地改完原样返回时,Export 回的是同一个 Go map;
	// 调用方要「清空旧体再写入新体」,共用引用会把数据先删没——顶层拷一份
	// 再交出,别名在边界上断开。
	body := make(map[string]any, len(exported))
	for k, v := range exported {
		body[k] = v
	}
	return body, nil
}

// loadApply 求值脚本并取出 apply 函数,错误码由调用场景定(保存期 400、
// 运行期 502)。
func loadApply(vm *goja.Runtime, code string, errCode apperr.Code) (goja.Callable, error) {
	if strings.TrimSpace(code) == "" {
		return nil, apperr.New(errCode, "effort script code is empty")
	}
	v, err := vm.RunString(code)
	if err != nil {
		return nil, apperr.Wrap(errCode, "effort script eval", err)
	}
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, apperr.New(errCode,
			"effort script must evaluate to an object with an apply function")
	}
	obj := v.ToObject(vm)
	if obj == nil {
		return nil, apperr.New(errCode,
			"effort script must evaluate to an object with an apply function")
	}
	apply, ok := goja.AssertFunction(obj.Get("apply"))
	if !ok {
		return nil, apperr.New(errCode, "effort script missing apply function")
	}
	return apply, nil
}
