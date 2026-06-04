-- Normalize trace/span tags before making tag filters index-friendly.
--
-- The dashboard filter parser canonicalizes tags to lower-case slug-like
-- values. Applying the same shape to existing rows lets normal array
-- containment queries use GIN indexes instead of lower(unnest(...)) scans.
WITH normalized_traces AS (
    SELECT t.id,
           COALESCE(
               array_agg(DISTINCT tag ORDER BY tag) FILTER (WHERE tag <> ''),
               ARRAY[]::text[]
           ) AS tags
    FROM opik_traces t
    CROSS JOIN LATERAL (
        SELECT regexp_replace(lower(btrim(raw_tag)), '[[:space:]]+', '-', 'g') AS tag
        FROM unnest(COALESCE(t.tags, ARRAY[]::text[])) AS raw(raw_tag)
    ) normalized
    WHERE t.tags IS NOT NULL
    GROUP BY t.id
)
UPDATE opik_traces t
SET tags = normalized_traces.tags
FROM normalized_traces
WHERE t.id = normalized_traces.id
  AND t.tags IS DISTINCT FROM normalized_traces.tags;

WITH normalized_spans AS (
    SELECT s.id,
           COALESCE(
               array_agg(DISTINCT tag ORDER BY tag) FILTER (WHERE tag <> ''),
               ARRAY[]::text[]
           ) AS tags
    FROM opik_spans s
    CROSS JOIN LATERAL (
        SELECT regexp_replace(lower(btrim(raw_tag)), '[[:space:]]+', '-', 'g') AS tag
        FROM unnest(COALESCE(s.tags, ARRAY[]::text[])) AS raw(raw_tag)
    ) normalized
    WHERE s.tags IS NOT NULL
    GROUP BY s.id
)
UPDATE opik_spans s
SET tags = normalized_spans.tags
FROM normalized_spans
WHERE s.id = normalized_spans.id
  AND s.tags IS DISTINCT FROM normalized_spans.tags;

CREATE INDEX IF NOT EXISTS idx_opik_traces_tags_gin
    ON opik_traces USING GIN(tags);

CREATE INDEX IF NOT EXISTS idx_opik_spans_tags_gin
    ON opik_spans USING GIN(tags);
