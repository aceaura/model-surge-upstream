CREATE TABLE IF NOT EXISTS accounts (
    name        TEXT PRIMARY KEY,
    provider_id TEXT        NOT NULL,
    credential  JSONB       NOT NULL,
    base_url    TEXT        NOT NULL DEFAULT '',
    headers     JSONB       NOT NULL DEFAULT '{}',
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS models (
    id             TEXT PRIMARY KEY,
    account        TEXT        NOT NULL REFERENCES accounts(name) ON DELETE CASCADE,
    native_model   TEXT        NOT NULL,
    protocol       TEXT        NOT NULL,
    context_window INTEGER     NOT NULL DEFAULT 0,
    defaults       JSONB       NOT NULL DEFAULT '{}',
    overrides      JSONB       NOT NULL DEFAULT '{}',
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
