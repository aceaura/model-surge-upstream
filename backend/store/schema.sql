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
