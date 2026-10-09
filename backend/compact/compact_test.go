package compact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aceaura/model-surge-upstream/backend/resolve"
)

func modeTarget(mode string, window int) resolve.ResolvedTarget {
	return resolve.ResolvedTarget{
		ModelID: "m", Account: "acc", Protocol: "anthropic",
		ContextWindow: window,
		Compact:       json.RawMessage(`{"mode":"` + mode + `"}`),
	}
}

// bigBody 构造估算必超小窗口的请求体（10 轮长中文历史）。
func bigBody() map[string]any {
	pad := strings.Repeat("很长的中文内容用来撑估算。", 20)
	msgs := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": pad})
	}
	return map[string]any{"model": "m", "max_tokens": 100, "messages": msgs}
}

func TestRunPassiveNoop(t *testing.T) {
	r := NewRunner(DefaultConfig())
	if _, reject := r.Run(modeTarget("passive", 100), bigBody()); reject {
		t.Fatal("passive 不应 reject")
	}
}

func TestRunBelowThreshold(t *testing.T) {
	r := NewRunner(DefaultConfig())
	est, reject := r.Run(modeTarget("error", 1000000), bigBody())
	if reject {
		t.Fatal("未超阈值不应 reject")
	}
	if est <= 0 {
		t.Fatalf("estimated = %d, want > 0", est)
	}
}

func TestRunErrorModeRejects(t *testing.T) {
	r := NewRunner(DefaultConfig())
	est, reject := r.Run(modeTarget("error", 100), bigBody())
	if !reject {
		t.Fatal("error 模式超限应 reject")
	}
	if est <= 100 {
		t.Fatalf("estimated = %d, want > window", est)
	}
}

func TestRunLegacyAutoRejects(t *testing.T) {
	r := NewRunner(DefaultConfig())
	if _, reject := r.Run(modeTarget("auto", 100), bigBody()); !reject {
		t.Fatal("存量 auto 应归一为 error 并 reject")
	}
}

func TestRunZeroWindow(t *testing.T) {
	r := NewRunner(DefaultConfig())
	if _, reject := r.Run(modeTarget("error", 0), bigBody()); reject {
		t.Fatal("未声明窗口不应 reject")
	}
}

func TestRunGlobalDefaultApplies(t *testing.T) {
	r := NewRunner(Defaults{Mode: ModeError, Threshold: 0.85})
	// 模型级 compact 为空时继承全局模式。
	empty := resolve.ResolvedTarget{ModelID: "m", Account: "acc", ContextWindow: 100}
	if _, reject := r.Run(empty, bigBody()); !reject {
		t.Fatal("全局 error 应对空模型级配置生效")
	}
}
