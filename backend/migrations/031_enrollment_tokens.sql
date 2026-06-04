CREATE TABLE IF NOT EXISTS host_enrollment_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL DEFAULT 'ovhcloud',
    provider_class TEXT NOT NULL DEFAULT 'registered',
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    used_by_host_id INT REFERENCES hosts(id),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
