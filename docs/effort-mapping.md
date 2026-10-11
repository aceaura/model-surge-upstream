# 推理档映射规则(effort mapping)

面向两类读者:**配置模型的管理员**(怎么填映射表、选双端格式)与**接入下游的开发者**(harness 怎么送档位、怎么拿声明)。

## 1. 总览:规范档桥接与先清后写

网关不直接把下游的思考参数原样透传给上游,而是以**规范档**(映射对表行的上游值,含 `none`)为桥:

```
下游请求体 ──Read(下游格式)──▶ 原始档 ──Coerce(映射表矫正)──▶ 上行值
           ──StripAll(全键清除)──▶ ──Write(上游格式)──▶ 上行请求体
```

- **Read**:按模型声明的下游格式(`effort_in`)从请求体里读出档位原始值。
- **Coerce**:把原始值矫正进映射对表(就低不就高,未知值取中,见 §6);产出是命中行的**上行值**;每次矫正写运行日志。
- **StripAll**:映射命中后**无条件清除全部已知档位键**(八格式并集,不限入口格式)——客户端残留的旧形态字段不得与新写入字段并存污染上行。兄弟键保留,删空的中层对象一并清理。
- **Write**:按模型声明的上游格式(`effort_format`)把上行值写成上游认识的字段结构。

自有扩展字段 `reasoning_level`(数字档)不入词表:消费即删,未命中也不泄漏上游;它是 Read 未命中时的回退链(§3)。

```mermaid
sequenceDiagram
    autonumber
    participant C as 下游客户端/harness
    participant P as proxyplane (转发面)
    participant E as effort (读/矫正/清/写)
    participant R as ringlog
    participant U as 上游厂商
    participant M as 下发面 /v1/models

    Note over M,C: 声明通道:模型列表携带 efforts(下游档+上行值映射对),下游按它渲染菜单/校验配置
    M-->>C: efforts=[{none,none},{low,low},{high,max}]

    C->>P: 请求体(自带档位字段 或 reasoning_level 数字档)
    P->>P: 总开关闸:关=不读不写不清,仅删 reasoning_level
    Note over P,E: 双端格式词表全量可用(跨族组合合法),读写清按各自声明的形态执行
    P->>E: Read(下游格式):体里读原始档
    E-->>P: 原始值(budget 反查亦走矫正)
    P->>E: Coerce(映射表):别名归一→Name 精确命中回 Value→就低钳→取中兜底
    E-->>P: 上行值(命中行的 Value)
    opt 发生矫正
        P->>R: effort coerced: 原值→矫正值
    end
    P->>E: StripAll:清全部已知档位键(先清后写,无条件)
    P->>E: Write(上游格式):effort 类直写档名 / budget 类写数字预算 / 无档位类恒落关思考
    Note over E: none 随格式落定:anthropic 载档族走 effort_off(disabled/between_tools/omit),<br/>gemini 族不动体,openai 族原样写;anthropic_off 命中任何档都落关思考
    P->>U: 上行请求(映射恒压 defaults;overrides 最后合并仍可压盖)
```

## 2. 双端词表全量可用

双端格式与出站协议(`protocol`)**解耦**:配置面恒罗列全部词表,跨族组合合法——承接「协议外壳 + 自家字段」的厂商差异(如 anthropic 外壳的端点却收 OpenAI 式顶层 `reasoning_effort`)。

- 下游格式(`effort_in`)词表 6 种:`openai_chat` / `openai_responses` / `anthropic_effort` / `anthropic_budget` / `anthropic_adaptive` / `anthropic_off`(不收 gemini 下游形态——gemini 无下游适配)。
- 上游格式(`effort_format`)词表 8 种:下游 6 种 + `gemini_level` / `gemini_budget`。
- 后端保存只校验值在词表内(`ValidFormat` / `ValidEntryFormat`),不做协议同族校验。
- 表单缺省落词表首项 `openai_chat`;切协议不重置双端格式;存量值读时归一后原样保留。
- 旧四值(`chat_completions`/`responses`/`anthropic`/`gemini`)读时归一到新词表(`openai_chat`/`openai_responses`/`anthropic_effort`/`gemini_level`)。
- 组合是否合理由配置者负责:跨族写出的字段上游可能不认识,按厂商实际文档选。

### 档词表(LevelsFor)

档名类格式的官方档词表,驱动表单下拉与保存校验(代码:`effort.LevelsFor`):

| 格式族 | 档词表 |
|---|---|
| openai_chat / openai_responses | none, minimal, low, medium, high, xhigh, max(7 值,GPT-6.1 起含 max) |
| anthropic_effort / anthropic_adaptive | none, low, medium, high, xhigh, max(none + 官方 5 档;关思考不是 effort 值,由映射行表达,写上游时按关思考落定) |
| gemini_level | none, LOW, HIGH |
| budget 类 / anthropic_off / auto | 无词表(右列是数字/none 或无档) |

`none` 进两端词表,作为「无思考」档显式声明;不再设独立的 0 档开关。

## 3. 映射对表语义

- 档位表是**行式映射对**:每行 `{name: 下游档, value: 上行值}`——name 是客户端/对话页选档的标识,value 是发给上游的档位字符串。
- **匹配按 name,产出取 value**:请求档精确命中某行 name → 上行发该行 value;非恒等映射(如 `low→medium`)由此表达。
- **下游 budget 格式**(`effort_in=anthropic_budget`):左列 name 填**预算阈值数字串**(如 `1024`/`4000`/`10000`),请求预算按区间反查命中行(§6 规则 7)。
- **上游 budget 格式**(`effort_format=anthropic_budget`/`gemini_budget`):右列 value 填 **token 数字串或 none**,写上行时数字直用(不再走内置预算表);none 按关思考落定/不动体。
- **数字档**(`reasoning_level: "N"`)按**行序**查表:`"1"`=第 1 行、`"2"`=第 2 行……(含 none 行在内数);`"0"` 按名命中 none 行(声明了 none 才命中);**越界档号钳到末行**并记日志。
- 上游思考恒开不可关的模型(kimi、codex)**不声明 none 行**即可,`none` 永远不会出现。
- 校验(保存时):name 须在下游格式档词表内(下游 budget 时为正整数串);value 须在上游档词表内(上游 budget 时为正整数串或 none);大小写不敏感;词表外拒绝并列出合法集。anthropic_off 上游时映射表必须为空。
- **存量迁移**(读时兼容,下次保存即改写):旧「行号即档号」的纯数字 name——值 none → `none→none`,其余 → 恒等映射 `value→value`;name 留空取 value;自动模式(null)生成恒等映射对。

## 4. 下游格式词表(Read)

| 格式 | 认什么字段 |
|---|---|
| openai_chat | 顶层 `reasoning_effort`(字符串) |
| openai_responses | `reasoning.effort` |
| anthropic_effort | `output_config.effort` |
| anthropic_budget | `thinking.type==enabled` 时的 `thinking.budget_tokens`,按预算区间反查档位(§6 规则 7) |
| anthropic_adaptive | `thinking.type==adaptive` 时的 `output_config.effort` |
| anthropic_off | `thinking.type` 为 `disabled`/`between_tools` → 读成 `none` |

读不到(字段缺失/形态不符)不算错误:回退 `reasoning_level` 数字档,再未命中则本次请求不动思考字段(也不清场)。`''`(auto)不读——但命中数字档回退时同样走「先清后写」。

## 5. 上游格式词表(Write)与三分类

上游格式分三类,分类决定映射行为与表单填写框(代码:`effort.CategoryOf`):

| 分类 | 格式 | 映射行为 | 表单右列 |
|---|---|---|---|
| effort 类(档位直写) | openai_chat, openai_responses, anthropic_effort, anthropic_adaptive, gemini_level | 上行值(档名)原样写入协议字段 | 档词表下拉 |
| budget 类(预算直写) | anthropic_budget, gemini_budget | 上行值数字串直写预算 token(钳到 < max_tokens);none 按落定/不动体 | 数字编辑框(或 none) |
| 无档位类 | anthropic_off | 不载档;**命中任何档都落关思考形态**(模型思考恒关) | 无映射表,efforts 落 `[]` |

下游格式(effort_in)对三类都有意义:不管上游是哪类,都按下游格式读档。

| 格式 | 怎么写 | `none` 怎么落定 |
|---|---|---|
| openai_chat | 顶层 `reasoning_effort` | 原样写 `none`(OpenAI 值域含 none) |
| openai_responses | `reasoning.effort`(不动其他 reasoning 键) | 原样写 |
| anthropic_effort | `output_config.effort` | 按关思考落定(§7) |
| anthropic_budget | `thinking:{type:enabled,budget_tokens:N}`,N 取映射行 value 数字串并钳到 < max_tokens | 按关思考落定 |
| anthropic_adaptive | `thinking:{type:adaptive}` + `output_config.effort` | 按关思考落定 |
| anthropic_off | 无档位:命中任何档都按关思考落定(off 恒为 disabled 标准写法) | 同左 |
| gemini_level | `generationConfig.thinkingConfig.thinkingLevel`(大写枚举) | 不动体(gemini 3.x 不可关思考) |
| gemini_budget | `generationConfig.thinkingConfig.thinkingBudget`(钳到 < maxOutputTokens) | 不动体 |

`''`(auto)=协议内置映射:按出站协议选字段(responses→`reasoning.effort`、chat_completions→`reasoning_effort`、anthropic→`output_config.effort` 且 none→`thinking:{type:disabled}`、gemini→`thinkingLevel`)。

## 6. 矫正算法(就低不就高,未知取中)

统一原则:**请求档超出映射表时,钳到「强度不超过请求值」的最高档;低于最低档时取最低档;完全未知的值按位置取中**。保守不超档,避免悄悄放大思考预算;匹配与强度比较都按行 name,**产出一律是命中行的 value**。

内置强度序:`none < minimal < low < medium < high < xhigh < max`。

管线(对 Read / reasoning_level 读出的原始值依次执行):

1. **空映射表**(模型未声明任何行)→ 恒等透传,不矫正不拦截。
2. **别名归一**:`off`/`disabled`→`none`,`extra-high`/`extra_high`→`xhigh`;大小写与空白归一。
3. **精确命中**某行 name → 回该行 value(仅归一命中也算矫正,值被改写了)。
4. **在强度序但未命中** → 向下钳到表内 name 强度不超过请求值的最高档行,回其 value;请求强度低于全部声明档 → 取最低档行的 value(声明了 `none` 它就是最低档)。
5. **完全未知值**(不在强度序,如 `ultra`、`hihg`)→ **排除 none 行后按位置取中**:序号 `(n-1)/2` 下取整(4 行选 2、3 行选 2、5 行选 3、2 行选 1、1 行选 1),回该行 value;表只有 none 行(n=0)回首行 value(即 none)。
6. **数字档越界**(`reasoning_level: "9"` 但只有 3 行)→ 钳到末行。
7. **budget 反查矫正**(下游 anthropic_budget)→ 行 name 为数字串时直接作阈值,否则回退内置预算表;钳到预算不超过请求数的最高档,低于最小档取最小档。

每次矫正写运行日志(日志页可见):`model effort coerced: 原值→矫正值 model=模型标识`。

示例矩阵(映射表 `{none,none} / {low,low} / {medium,medium} / {high,max}`):

| 请求值 | 实际档 | 规则 |
|---|---|---|
| `high` | max | 精确命中第 4 行,上行发该行 value |
| `HIGH` | max | 归一命中 |
| `off` | none | 别名归一 |
| `xhigh` | max | 向下钳到 high 行 |
| `minimal` | none | 低于最低档取最低 |
| `ultra` | medium | 未知值排除 none 后 3 行取中=第 2 行 |
| `reasoning_level: "9"` | max | 数字档越界钳末行 |

映射表 `{low,low} / {high,max} / {max,max}`(无 none 行,如 kimi):请求 `none` → low(表内最低档);请求 `medium` → low(向下钳)。

## 7. 附属规则

| 规则 | 字段 | 何时生效 | 作用 |
|---|---|---|---|
| 总开关 | `effort_enabled` | 恒判 | false=不读不写不清场,对话页选档不落笔;`reasoning_level` 仍消费即删 |
| 关思考落定 | `effort_off` | 上游格式属 anthropic 载档族且档位为 `none`;anthropic_off(无档位类)命中任何档都落定 | `''`=disabled(标准,`thinking:{type:disabled}`);`between_tools`=Sonnet 5.5 顶替(整对象替换);`omit`=不写(上游思考恒开,不声明 none 行承担) |
| 预算覆盖 | `effort_budgets` | budget 反查/写上行时,行 name/value 为档名(非数字串)的兼容回退 | 档位值→预算 token 的模型级覆盖;内置表 low 1024 / medium 4000 / high 10000 / xhigh 20000 / max 32000,表外档回 4000;映射行直接填数字串时不经过此表 |
| 数字档 | `reasoning_level`(请求字段) | 下游格式 Read 未命中时回退 | 顶层扩展字段,档号按映射表行序(§3);消费即删,越界钳末行 |

## 8. 声明契约:下游怎么配

**拿声明**:网关下发面 `/v1/models` 各协议形态的每条模型携带 `efforts` 字段(`[{name,value}]`,name=下游档,value=上行值)。下游应当:

- 用它渲染档位菜单(name 展示、选择后按模型下游格式送档或直接送 `reasoning_level`);
- 用它做本地校验——送表外值会被网关矫正(就低/取中),不保证原样上行;
- `reasoning_level` 数字档的合法档号是 `"1"`…`"N"`(行序,含 none 行),`"0"` 按名命中 none 行;
- 列表里没有 `none` 行 = 该模型不支持关闭思考,别送 none/0 档。

**送档位**:按模型的下游格式(effort_in)在请求体里携带对应字段(§4 表),或直接送顶层 `reasoning_level`。

**每协议最小配置示例**(管理员视角):

- chat_completions 模型:双端格式按缺省 openai_chat 即可(词表首项),只需填映射表(恒等映射:左档右同值)。
- responses 模型:双端格式改选 openai_responses。
- anthropic 模型(下游 harness 送预算):下游格式 anthropic_budget、上游格式按厂商选(如 anthropic_effort);左列填阈值数字串;需要支持关思考时加一行 `none→none` 并按厂商选关思考落定。
- anthropic 无档位模型(思考恒关):上游格式 anthropic_off,无需映射表;下游格式按 harness 形态选(读档仍生效),送什么档都按关思考发上游。
- gemini 模型:上游格式 gemini_level(3.x)或 gemini_budget(2.5 预算制);下游格式按 harness 形态选(harness 不送档位字段时任选——读不到即回退 `reasoning_level`,再未命中不动体);不声明 none 行。

## 9. 优先级与冲突

- **参数合并顺序**:模型 defaults ← 请求体参数 ← 档位映射写入 ← 模型 overrides。映射恒压 defaults;**overrides 最后合并,仍可压盖映射结果**(留给「强制某档」的运维口径)。
- **先清后写**:映射命中后 StripAll **无条件**清全部已知档位键(不限入口格式,auto 入口命中数字档也清),再由 Write 按上游格式落笔——残留的旧形态字段不与新写入字段并存。未命中(Read 与数字档回退都落空)则完全不触碰请求体。
- **同格式入=出**:清除发生在写入之前,同名字段清掉后由 Write 按映射值重写,不会自吃。
- **总开关优先于一切**:关闭时双端格式、映射表、附属规则全部旁路。
