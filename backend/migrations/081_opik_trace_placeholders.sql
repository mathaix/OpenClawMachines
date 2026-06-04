-- Allow spans to arrive before their parent trace batch.
--
-- Placeholder traces preserve the opik_spans.trace_id foreign key and claim the
-- trace UUID for the first account that references it. The real trace upsert
-- replaces the placeholder metadata once it arrives.
ALTER TABLE opik_traces
    ADD COLUMN IF NOT EXISTS is_placeholder BOOLEAN NOT NULL DEFAULT false;
