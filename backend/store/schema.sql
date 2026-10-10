CREATE TABLE IF NOT EXISTS accounts (
    name           TEXT PRIMARY KEY,
    provider_id    TEXT        NOT NULL,
    credential     JSONB       NOT NULL,
    base_url       TEXT        NOT NULL DEFAULT '',
    headers        JSONB       NOT NULL DEFAULT '{}',
    quota_settings JSONB       NOT NULL DEFAULT '{}',
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 已存在的库补列:CREATE TABLE IF NOT EXISTS 不会改旧表结构。
-- quota_settings 承载账号级额度查询节奏(auto_interval_minutes/
-- stop_interval_minutes);查询本身走 provider 的 Go 内置实现,'{}' 即未配置。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS quota_settings JSONB NOT NULL DEFAULT '{}';

-- sort_order 承载账号页拖拽排序:小者在前。老库与新建账号同为 0 起,
-- 并列时列表回落 name 序,即拖拽功能存在之前的显示顺序。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS sort_order INTEGER NOT NULL DEFAULT 0;

-- quota_script(账号级 JS 额度脚本)已废除:旧库把两个调度间隔迁入
-- quota_settings 后整列删除,脚本代码/超时/自定义变量一并随列销毁。
DO $$
BEGIN
    -- 必须限定 table_schema:测试用临时 schema 的同名旧表会让这里误判,
    -- 随后 UPDATE 在当前 schema 上找不到该列而报错。
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema()
                 AND table_name = 'accounts' AND column_name = 'quota_script') THEN
        UPDATE accounts SET quota_settings = jsonb_build_object(
            'auto_interval_minutes', COALESCE((quota_script->>'auto_interval_minutes')::int, 0),
            'stop_interval_minutes', COALESCE((quota_script->>'stop_interval_minutes')::int, 0))
        WHERE quota_script <> '{}'::jsonb;
        ALTER TABLE accounts DROP COLUMN quota_script;
    END IF;
END $$;

-- provider id 路径式改名(2026-10-06):厂商[-区域]/服务,Global 省略区域,
-- 按量计费标 /api,订阅标套餐 slug;kiro 无可命名套餐保持裸 id。存量账号
-- 的 provider_id 逐个改写,幂等可重复执行。
UPDATE accounts SET provider_id = 'anthropic/api' WHERE provider_id = 'anthropic';
UPDATE accounts SET provider_id = 'openai/api' WHERE provider_id = 'openai';
UPDATE accounts SET provider_id = 'openai/codex' WHERE provider_id = 'openai-codex';
UPDATE accounts SET provider_id = 'gemini/api' WHERE provider_id = 'gemini';
UPDATE accounts SET provider_id = 'kimi/coding' WHERE provider_id = 'kimi';
UPDATE accounts SET provider_id = 'ark/api' WHERE provider_id = 'ark';
UPDATE accounts SET provider_id = 'deepseek/api' WHERE provider_id = 'deepseek';
UPDATE accounts SET provider_id = 'bailian-cn/token-plan' WHERE provider_id = 'bailian';
UPDATE accounts SET provider_id = 'bailian-cn/coding-plan' WHERE provider_id = 'bailian-coding';

-- provider id 点式改名(2026-10-06):厂商.区域.计费.服务 四段恒在——区域
-- cn/global 不省略,计费 api=按量、subscribe=订阅,服务段 standard=默认服务、
-- 订阅套餐用 slug。承接上面的路径式块(任意年代库链式迁移),幂等可重复执行。
UPDATE accounts SET provider_id = 'anthropic.global.api.standard' WHERE provider_id = 'anthropic/api';
UPDATE accounts SET provider_id = 'openai.global.api.standard' WHERE provider_id = 'openai/api';
UPDATE accounts SET provider_id = 'openai.global.subscribe.codex' WHERE provider_id = 'openai/codex';
UPDATE accounts SET provider_id = 'gemini.global.api.standard' WHERE provider_id = 'gemini/api';
UPDATE accounts SET provider_id = 'kimi.global.subscribe.coding' WHERE provider_id = 'kimi/coding';
UPDATE accounts SET provider_id = 'ark.global.api.standard' WHERE provider_id = 'ark/api';
UPDATE accounts SET provider_id = 'deepseek.global.api.standard' WHERE provider_id = 'deepseek/api';
UPDATE accounts SET provider_id = 'kiro.global.subscribe.standard' WHERE provider_id = 'kiro';
UPDATE accounts SET provider_id = 'bailian.cn.subscribe.token-plan' WHERE provider_id = 'bailian-cn/token-plan';
UPDATE accounts SET provider_id = 'bailian.cn.subscribe.coding-plan' WHERE provider_id = 'bailian-cn/coding-plan';

CREATE TABLE IF NOT EXISTS models (
    id             TEXT PRIMARY KEY,
    account        TEXT        NOT NULL REFERENCES accounts(name) ON DELETE CASCADE,
    native_model   TEXT        NOT NULL,
    protocol       TEXT        NOT NULL,
    context_window INTEGER     NOT NULL DEFAULT 0,
    defaults       JSONB       NOT NULL DEFAULT '{}',
    overrides      JSONB       NOT NULL DEFAULT '{}',
    compact        JSONB       NOT NULL DEFAULT '{}',
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 已存在的库补列：CREATE TABLE IF NOT EXISTS 不会改旧表结构。
-- compact 承载上下文压缩配置（mode/threshold）。
ALTER TABLE models ADD COLUMN IF NOT EXISTS compact JSONB NOT NULL DEFAULT '{}';

-- efforts 为推理档支持列表：JSON null=自动（按协议+模型名规则推导），
-- 数组=管理员显式声明（空数组即该模型不支持 effort）。
ALTER TABLE models ADD COLUMN IF NOT EXISTS efforts JSONB NOT NULL DEFAULT 'null';

-- effort_format 为 effort 写入格式(effort.FormatXxx 枚举):空串=协议内置
-- 映射(按出站协议选字段);非空=显式格式压过协议外形,按目标协议命名
-- (chat_completions/responses/anthropic/gemini),承接「协议外壳+自家字段」
-- 的厂商差异(如 kimi 顶层 reasoning_effort)。none 是否上行由 efforts 是否
-- 声明 0 档决定,格式不做 none 特判(2026-10-06 起不再设 skip_none 变体)。
ALTER TABLE models ADD COLUMN IF NOT EXISTS effort_format TEXT NOT NULL DEFAULT '';

-- 存量 effort_script 迁移(2026-10-05 脚本配置收进通用底层):含顶层
-- reasoning_effort 写法的脚本统一归入 chat_completions;旧脚本对 none 的
-- delete/不落字段特判随之消失——上游思考不可关的模型靠不声明 0 档表达。
-- 迁移后删列。
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'models' AND column_name = 'effort_script') THEN
        UPDATE models SET effort_format = 'chat_completions'
        WHERE effort_script LIKE '%reasoning_effort%' AND effort_format = '';
        ALTER TABLE models DROP COLUMN effort_script;
    END IF;
END $$;

-- 格式枚举协议化改名(2026-10-06):旧字段式命名→协议式命名;skip_none
-- 变体同日废除(降级为 chat_completions,none 特判改由 0 档声明承担)。
UPDATE models SET effort_format = 'chat_completions' WHERE effort_format IN ('reasoning_effort', 'reasoning_effort_skip_none', 'chat_completions_skip_none');
UPDATE models SET effort_format = 'responses' WHERE effort_format = 'reasoning_object';
UPDATE models SET effort_format = 'anthropic' WHERE effort_format = 'output_config';
UPDATE models SET effort_format = 'gemini' WHERE effort_format = 'thinking_level';

-- sort_order 承载模型页拖拽排序,语义同 accounts.sort_order:小者在前,
-- 并列回落 id 序(即拖拽功能存在之前的显示顺序)。
ALTER TABLE models ADD COLUMN IF NOT EXISTS sort_order INTEGER NOT NULL DEFAULT 0;

-- effort 双端转换(2026-10-10):effort_in=入口 effort 格式(空=auto=现状:
-- 对话页选档+reasoning_level 数字档,透传体不解析不剥离);非空=显式声明
-- harness 送进来的形态,转发面按它读规范档并剥离残留键。
-- effort_off=0 档在 anthropic 族上游的关思考落定(空=disabled /
-- between_tools=Sonnet 5.5 顶替 / omit=不写即上游不可关)。
-- effort_budgets=档位值→预算 token 覆盖映射(空对象=全内置映射),
-- 预算类上游格式(anthropic_budget/gemini_budget)生效。
ALTER TABLE models ADD COLUMN IF NOT EXISTS effort_in TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN IF NOT EXISTS effort_off TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN IF NOT EXISTS effort_budgets JSONB NOT NULL DEFAULT '{}';

-- 推理档转换总开关(2026-10-10):false=转发面不读不写不剥离(reasoning_level
-- 仍消费即删,网关扩展字段不泄漏),对话页选档不落笔;默认 true 保旧行为。
ALTER TABLE models ADD COLUMN IF NOT EXISTS effort_enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE INDEX IF NOT EXISTS models_account_idx ON models(account);

-- 代理转发面配置：单行表（id 恒为 1）。api_key 为空表示转发面关闭。
-- 端口与监听范围运行期可改（管理面应用后立即重绑监听），故落库而非走环境变量。
CREATE TABLE IF NOT EXISTS proxy_settings (
    id         INTEGER     PRIMARY KEY CHECK (id = 1),
    api_key    TEXT        NOT NULL DEFAULT '',
    port       INTEGER     NOT NULL DEFAULT 12344,
    lan_open   BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO proxy_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- 对话页：会话与消息。消息按自增 id 定序（即时间序），
-- 会话删时消息级联删。model_id 记最近一次发送所用模型，供选择器回显。
CREATE TABLE IF NOT EXISTS chat_sessions (
    id         TEXT PRIMARY KEY,
    title      TEXT        NOT NULL DEFAULT '',
    model_id   TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS chat_messages (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id TEXT        NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    role       TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS chat_messages_session_idx ON chat_messages(session_id);

-- 图片附件：base64 内嵌 JSONB（[{mime,data}]），空数组表示纯文本消息。
-- 已存在的库补列：CREATE TABLE IF NOT EXISTS 不会改旧表结构。
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS attachments JSONB NOT NULL DEFAULT '[]';

-- 用量统计：每次上游请求的四桶 token 明细（输入/输出/缓存读取/缓存写入）。
-- 转发面与对话面共用，source 区分来源。input_semantics 记录该行的输入语义：
-- 1=输入含缓存（OpenAI 两族/Gemini），2=输入已是净输入（Anthropic）；
-- 聚合时统一归一为净输入，否则跨协议的「新增输入」会重复计缓存。
CREATE TABLE IF NOT EXISTS usage_logs (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id        TEXT        NOT NULL DEFAULT '',
    source            TEXT        NOT NULL,
    protocol          TEXT        NOT NULL,
    model_id          TEXT        NOT NULL DEFAULT '',
    account           TEXT        NOT NULL DEFAULT '',
    native_model      TEXT        NOT NULL DEFAULT '',
    input_tokens      BIGINT      NOT NULL DEFAULT 0,
    output_tokens     BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT      NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT     NOT NULL DEFAULT 0,
    input_semantics   SMALLINT    NOT NULL DEFAULT 2,
    status_code       INTEGER     NOT NULL DEFAULT 0,
    is_streaming      BOOLEAN     NOT NULL DEFAULT FALSE,
    latency_ms        INTEGER,
    duration_ms       INTEGER,
    error_message     TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS usage_logs_created_idx ON usage_logs(created_at);
CREATE INDEX IF NOT EXISTS usage_logs_model_idx ON usage_logs(model_id);
CREATE INDEX IF NOT EXISTS usage_logs_account_idx ON usage_logs(account);
