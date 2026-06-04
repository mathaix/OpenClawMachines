CREATE TABLE token_usage (
  id BIGSERIAL PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id),
  machine_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'byok',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_token_usage_account ON token_usage(account_id, created_at);
CREATE INDEX idx_token_usage_machine ON token_usage(machine_id, created_at);
