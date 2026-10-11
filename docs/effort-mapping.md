# 推理档映射规则(effort mapping)

面向两类读者:**配置模型的管理员**(怎么填档位表、选双端格式)与**接入下游的开发者**(harness 怎么送档位、怎么拿声明)。

## 1. 总览:规范档桥接模型

网关不直接把下游的思考参数原样透传给上游,而是以**规范档**(模型档位表里声明的 value,含 `none`)为桥:

```
下游请求体 ──Read(下游格式)──▶ 规范档 ──Coerce(档位表矫正)──▶ 声明档
            ──StripKeys(剥入口残留键)──▶ ──Write(上游格式)──▶ 上行请求体
```

- **Read**:按模型声明的下游格式(`effort_in`)从请求体里读出档位原始值。
- **Coerce**:把原始值矫正进档位表(就低不就高,见 §6);每次矫正写运行日志。
- **StripKeys**:剥掉入口格式读过的键(先剥后写,入口与上游共用键时才不会吃掉刚写入的字段)。
- **Write**:按模型声明的上游格式(`effort_format`)把声明档写成上游认识的字段结构。

自有扩展字段 `reasoning_level`(数字档)不入词表:消费即删,未命中也不泄漏上游;它是 Read 未命中时的回退链(§3)。

```mermaid
sequenceDiagram
    autonumber
    participant C as 下游客户端/harness
    participant P as proxyplane (转发面)
    participant E as effort (读/矫正/剥/写)
    participant R as ringlog
    participant U as 上游厂商
    participant M as 下发面 /v1/models

    Note over M,C: 声明通道:模型列表携带 efforts(档号+上行值),下游按它渲染菜单/校验配置
    M-->>C: efforts=[0·none,1·low,2·high,3·max]

    C->>P: 请求体(自带档位字段 或 reasoning_level 数字档)
    P->>P: 总开关闸:关=不读不写不剥,仅删 reasoning_level
    Note over P,E: 双端格式恒与声明协议同族(配置面强制),读写剥必在同族字段集内
    P->>E: Read(下游格式):体里读规范档
    E-->>P: 原始值(budget 反查亦走矫正)
    P->>E: Coerce(声明表):别名归一→精确命中→向下钳→medium 兜底
    E-->>P: 矫正后档位值
    opt 发生矫正
        P->>R: effort coerced: 原值→矫正值
    end
    P->>E: StripKeys(下游格式):剥入口残留键(先剥后写)
    P->>E: Write(上游格式):effort 类直写档位 / budget 类写预算 / 无档位类恒落关思考
    Note over E: none 随格式落定:anthropic 载档族走 effort_off(disabled/between_tools/omit),<br/>gemini 族不动体,openai 族原样写;anthropic_off 命中任何档都落关思考
    P->>U: 上行请求(映射恒压 defaults;overrides 最后合并仍可压盖)
```

## 2. 协议同族约束

模型的出站协议(`protocol`)决定双端格式的合法集合;**跨族组合写出的字段上游根本不认识**,配置面只渲染同族选项,后端保存时同族校验(空串=auto 放行):

| 声明协议 | 下游格式(effort_in) | 上游格式(effort_format) |
|---|---|---|
| chat_completions | openai_chat | openai_chat |
| responses | openai_responses | openai_responses |
| anthropic | anthropic_effort / anthropic_budget / anthropic_adaptive / anthropic_off | 同左四种 |
| gemini | 无(词表不收 gemini 下游)→ 表单隐藏该字段,恒存 `''` | gemini_level / gemini_budget |

细则:

- 单选项族(chat_completions/responses)下拉只有一项,等同落定,仍渲染以明示形态。
- gemini 协议下游走 `''`(auto=不读不剥,体里自带字段透传;`reasoning_level` 数字档照常消费)。
- 表单切协议时,双端格式重置为新协议族首项(族内值保留);存量跨族值读时回落族首项,下次保存即迁移,落库原值不动。
- 旧四值(`chat_completions`/`responses`/`anthropic`/`gemini`)读时归一到新词表(`openai_chat`/`openai_responses`/`anthropic_effort`/`gemini_level`)。

## 3. 档位表语义

- 档位表是**显式数字映射**:每行一个档位值,**行号即档号**(1、2、3…),0 档固定为「关闭思考」(上行值 `none`),由表单开关决定是否声明。
- value 就是发给上游的档位字符串,不设固定词表:各家私有档(`ultra` 等)也能声明,上行按声明原样发送。
- 上游思考恒开不可关的模型(kimi、codex)**不声明 0 档**即可,`none` 永远不会出现。
- 下游请求顶层 `reasoning_level: "N"` 按档号 N 查表取值(网关扩展字段,消费即删);**越界档号钳到末行最高档**并记日志。

## 4. 下游格式词表(Read/StripKeys)

| 格式 | 认什么字段 | 剥什么键 |
|---|---|---|
| openai_chat | 顶层 `reasoning_effort`(字符串) | `reasoning_effort` |
| openai_responses | `reasoning.effort` | 只删 `reasoning.effort`,兄弟键(summary 等)保留 |
| anthropic_effort | `output_config.effort` | `output_config.effort`(删空则清理空壳) |
| anthropic_budget | `thinking.type==enabled` 时的 `thinking.budget_tokens`,按预算区间反查档位(§6 规则 7) | `thinking.type` + `thinking.budget_tokens` |
| anthropic_adaptive | `thinking.type==adaptive` 时的 `output_config.effort` | `thinking.type` 与 `output_config.effort` |
| anthropic_off | `thinking.type` 为 `disabled`/`between_tools` → 读成 `none` | `thinking.type` |

读不到(字段缺失/形态不符)不算错误:回退 `reasoning_level` 数字档,再未命中则本次请求不动思考字段。`''`(auto)不读不剥——体里自带字段原样透传。

## 5. 上游格式词表(Write)与三分类

上游格式分三类,分类决定映射行为与表单填写框(代码:`effort.CategoryOf`):

| 分类 | 格式 | 映射行为 | 表单填写框 |
|---|---|---|---|
| effort 类(档位直写) | openai_chat, openai_responses, anthropic_effort, anthropic_adaptive, gemini_level | 档位值原样写入协议字段 | 档位表(0 档开关 + 1..N 档行);关思考落定仅 anthropic 两格式 |
| budget 类(预算映射) | anthropic_budget, gemini_budget | 档位值→预算 token(BudgetOf+钳制) | 档位表 + 预算列;关思考落定仅 anthropic_budget |
| 无档位类 | anthropic_off | 不载档;**命中任何档都落关思考形态**(模型思考恒关) | 无档位表/0 档开关/关思考落定,efforts 落 `[]` |

下游格式(effort_in)对三类都有意义:不管上游是哪类,都按下游格式读档并剥掉原键。effort 类与 budget 类按档位数字映射(档号→声明表行→值);无档位类无需映射,直接改格式。

| 格式 | 怎么写 | `none` 怎么落定 |
|---|---|---|
| openai_chat | 顶层 `reasoning_effort` | 原样写 `none`(OpenAI 值域含 none) |
| openai_responses | `reasoning.effort`(不动其他 reasoning 键) | 原样写 |
| anthropic_effort | `output_config.effort` | 按关思考落定(§7) |
| anthropic_budget | `thinking:{type:enabled,budget_tokens:N}`,N 按预算表并钳到 < max_tokens | 按关思考落定 |
| anthropic_adaptive | `thinking:{type:adaptive}` + `output_config.effort` | 按关思考落定 |
| anthropic_off | 无档位:命中任何档都按关思考落定(off 恒为 disabled 标准写法) | 同左 |
| gemini_level | `generationConfig.thinkingConfig.thinkingLevel`(大写枚举) | 不动体(gemini 3.x 不可关思考) |
| gemini_budget | `generationConfig.thinkingConfig.thinkingBudget`(钳到 < maxOutputTokens) | 不动体 |

`''`(auto)=协议内置映射:按出站协议选字段(responses→`reasoning.effort`、chat_completions→`reasoning_effort`、anthropic→`output_config.effort` 且 none→`thinking:{type:disabled}`、gemini→`thinkingLevel`)。

## 6. 矫正算法(就低不就高)

统一原则:**请求档超出声明表时,钳到「强度不超过请求值」的最高档;低于最低档时取最低档**。保守不超档,避免悄悄放大思考预算。

内置强度序:`none < minimal < low < medium < high < xhigh < max`。

管线(对 Read / reasoning_level 读出的原始值依次执行):

1. **空声明表**(模型未声明任何档)→ 恒等透传,不矫正不拦截。
2. **别名归一**:`off`/`disabled`→`none`,`extra-high`/`extra_high`→`xhigh`;大小写与空白归一。
3. **精确命中**声明表 → 用声明原值(仅归一命中也算矫正,值被改写了)。
4. **在强度序但未声明** → 向下钳到声明表内强度不超过请求值的最高档;请求强度低于全部声明档 → 取声明表最低档(声明了 `none` 它就是最低档)。
5. **完全未知值**(不在强度序,如 `ultra`、`hihg`)→ 回落声明表中的 `medium`;无 medium 取首个非 none 档;表只有 none 回 none。
6. **数字档越界**(`reasoning_level: "9"` 但只有 3 行)→ 钳到末行最高档。
7. **budget 反查矫正**(下游 anthropic_budget)→ 不再要求精确命中:钳到预算不超过请求数的最高档,低于最小档取最小档。

每次矫正写运行日志(日志页可见):`model effort coerced: 原值→矫正值 model=模型标识`。

示例矩阵(声明表 `0·none / 1·low / 2·medium / 3·high`):

| 请求值 | 实际档 | 规则 |
|---|---|---|
| `high` | high | 精确命中 |
| `HIGH` | high | 归一命中 |
| `off` | none | 别名归一 |
| `xhigh` | high | 向下钳 |
| `max` | high | 向下钳 |
| `minimal` | none | 低于最低档取最低 |
| `ultra` | medium | 未知值 medium 兜底 |
| `reasoning_level: "9"` | high | 数字档越界钳末行 |

声明表 `1·low / 2·high / 3·max`(无 0 档,如 kimi):请求 `none` → low(表内最低档);请求 `medium` → low(向下钳)。

## 7. 附属规则

| 规则 | 字段 | 何时生效 | 作用 |
|---|---|---|---|
| 总开关 | `effort_enabled` | 恒判 | false=不读不写不剥离,对话页选档不落笔;`reasoning_level` 仍消费即删 |
| 关思考落定 | `effort_off` | 上游格式属 anthropic 载档族且档位为 `none`;anthropic_off(无档位类)命中任何档都落定 | `''`=disabled(标准,`thinking:{type:disabled}`);`between_tools`=Sonnet 5.5 顶替(整对象替换);`omit`=不写(上游思考恒开,0 档仅靠不声明承担) |
| 预算覆盖 | `effort_budgets` | 预算类格式(anthropic_budget/gemini_budget)取数,及 anthropic_budget 入口反查 | 档位值→预算 token 的模型级覆盖;内置表 low 1024 / medium 4000 / high 10000 / xhigh 20000 / max 32000,表外档回 4000;写上行时钳到 < max_tokens(体里 max_tokens ≤1024 或未声明则不钳) |
| 数字档 | `reasoning_level`(请求字段) | 下游格式 Read 未命中时回退 | 顶层扩展字段,档号即档位表行号;消费即删,越界钳末行 |

## 8. 声明契约:下游怎么配

**拿声明**:网关下发面 `/v1/models` 各协议形态的每条模型携带 `efforts` 字段(`[{name,value}]`,name=档号/显示名,value=上行值)。下游应当:

- 用它渲染档位菜单(name 展示、value 上行);
- 用它做本地校验——送表外值会被网关矫正(就低),不保证原样上行;
- `reasoning_level` 数字档的合法档号就是 name 列(通常 `"0"`…`"N"`);
- 列表里没有 `none` 条目 = 该模型不支持关闭思考,别送 0 档。

**送档位**:按模型的下游格式(effort_in)在请求体里携带对应字段(§4 表),或直接送顶层 `reasoning_level`。

**每协议最小配置示例**(管理员视角):

- chat_completions 模型:协议选 chat_completions,双端格式自动落定 openai_chat,只需填档位表。
- responses 模型:同上,双端 openai_responses。
- anthropic 模型(下游 harness 送预算):下游格式 anthropic_budget、上游格式按厂商选(如 anthropic_effort);需要关 0 档思考时声明 0 档并按厂商选关思考落定。
- anthropic 无档位模型(思考恒关):上游格式 anthropic_off,无需档位表;下游格式按 harness 形态选(读档剥键仍生效),送什么档都按关思考发上游。
- gemini 模型:下游格式隐藏(透传),上游格式 gemini_level(3.x)或 gemini_budget(2.5 预算制);不声明 0 档。

## 9. 优先级与冲突

- **参数合并顺序**:模型 defaults ← 请求体参数 ← 档位映射写入 ← 模型 overrides。映射恒压 defaults;**overrides 最后合并,仍可压盖映射结果**(留给「强制某档」的运维口径)。
- **先剥后写**:入口与上游共用键时(anthropic 族 `output_config.effort`、`thinking.type`),写后剥离会吃掉刚写入的字段;Read 已取到规范档,剥离不依赖入口键,顺序固定为先 StripKeys 后 Write。
- **入口==上游同格式**:写入即剥离会自吃?不会——先剥后写的顺序保证剥离发生在写入之前,同格式时字段最终按映射值落定。
- **总开关优先于一切**:关闭时双端格式、档位表、附属规则全部旁路。
