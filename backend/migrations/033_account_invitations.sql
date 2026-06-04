-- Migration 033: Account invitations for team membership
CREATE TABLE IF NOT EXISTS account_invitations (
    id              SERIAL PRIMARY KEY,
    account_id      INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'member',
    token           UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    status          TEXT NOT NULL DEFAULT 'pending',
    invited_by      INT NOT NULL REFERENCES users(id),
    accepted_by     INT REFERENCES users(id),
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    responded_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_invitations_token ON account_invitations(token);
CREATE INDEX IF NOT EXISTS idx_invitations_email ON account_invitations(email);
CREATE INDEX IF NOT EXISTS idx_invitations_account ON account_invitations(account_id);
