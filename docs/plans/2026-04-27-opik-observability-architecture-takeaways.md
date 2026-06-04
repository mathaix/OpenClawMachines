# Opik Observability Architecture Takeaways

Date: 2026-04-27
Status: First implementation slice in progress

## Context

The current OpenClawMachines Opik work added an account-wide observability page, trace/span detail, payload inspection, feedback scores, and trace triage tags. The immediate production pain is that tag filtering and trace search feel slow and brittle, especially after adding editable tags.

This note captures the architecture takeaways from comparing our implementation with Opik's public product shape and deployment model.

## What Opik Optimizes For

Opik treats trace data as high-volume observability data, not as a small Postgres JSON browser. Its public README describes production monitoring, feedback score dashboards, online evaluation rules, and high-volume trace ingestion at 40M+ traces/day.

Opik's self-hosting chart separates state and analytics concerns. The chart exposes ClickHouse as the analytics database via `ANALYTICS_DB_*` settings and MySQL as the state database via `STATE_DB_*` settings. That is the key design signal for us: trace listing, filtering, and aggregation should use storage shapes that are built for analytical reads.

Opik's trace/span search API is structured. The Python SDK documents filters over fields such as `status`, `start_time`, `end_time`, `input`, `output`, `metadata.<key>`, `feedback_scores.<name>`, `tags contains`, usage tokens, duration, and total estimated cost. That is a different product model from one catch-all text box over JSON text casts.

Opik's online evaluation rules are first-class production workflows. Rules can run LLM-as-judge scoring over production traces, store results as feedback scores, and also score full conversation threads after a cooldown period.

Opik keeps trace query performance as an explicit backend concern. Recent release notes mention optimizing `TraceDAO` and `SpanDAO` ClickHouse queries and adding time range filters to feedback, comments, and guardrail CTEs.

## Takeaways For OpenClawMachines

The short-term fix is not to mimic all of Opik. It is to make our Postgres design less hostile to the queries we already expose.

1. Normalize tags at write time.
   Tags should be canonical before they hit storage: lower-case, trimmed, slug-like, deduped. The UI and filter parser now normalize input, but ingest paths should do the same so old and new traces behave consistently.

2. Make tag search indexable.
   The current case-insensitive `lower(unnest(tags))` pattern is correct functionally, but it prevents efficient array index usage. Once tags are normalized on write, tag filtering can use exact array containment with GIN indexes on `opik_traces.tags` and `opik_spans.tags`.

3. Separate candidate selection from span aggregation.
   Our account trace list currently joins and aggregates spans while also filtering and ordering traces. At real volume, this makes every list request heavier than necessary. The better shape is:
   - first select candidate trace IDs using account, time, status, tags, and query filters
   - apply order and limit
   - aggregate spans only for that small candidate set

4. Stop firing expensive searches on every keystroke.
   The frontend now ignores stale responses, which prevents wrong results from replacing current results. It still sends too many requests. Add debounce or an explicit Apply action for expensive filters.

5. Split source tags from human labels.
   Tags like `ocm` are ingestion provenance, not useful triage labels. The current UI hides low-signal tags while preserving them, but the durable model should separate `source_tags` from reviewer or workflow labels.

6. Treat feedback scores as queryable data.
   Feedback scores should eventually support filtering, sorting, averages, reviewer attribution, and rule-generated scores. The product value is not just attaching a score to a trace; it is finding traces where quality, latency, cost, or error behavior crosses a threshold.

7. Add thread-level views before building a large evaluation system.
   OpenClaw debugging often spans multiple turns. Thread grouping is the bridge between single trace inspection and useful evaluation rules.

8. Keep dashboards derived from indexed, structured fields.
   Useful dashboards should track volume, latency percentiles, token usage, estimated cost, errors, feedback trends, and tag/label cohorts. They should not depend on scanning arbitrary JSON payloads.

## Recommended Implementation Order

1. Fix tag search performance:
   - normalize trace and span tags on all write/update paths
   - add GIN indexes for trace and span tags
   - rewrite tag filters to exact array containment
   - add tests that ensure tag SQL does not use `unnest` for normal filtering

   Status: implemented in migration `083_opik_trace_tag_indexes.sql` plus store-level tag normalization and SQL tests.

2. Reshape account trace listing:
   - use a candidate trace CTE
   - limit before span aggregation
   - aggregate spans only for selected trace IDs

3. Reduce frontend query pressure:
   - debounce search/filter inputs
   - keep explicit loading/error states tied to the active filter request

4. Improve data model semantics:
   - introduce trace labels or tag kind/source separation
   - leave client ingestion tags intact for provenance

5. Expand structured filtering:
   - status
   - latency/duration
   - cost
   - token usage
   - feedback score thresholds
   - metadata key filters
   - thread ID

6. Add higher-level workflows:
   - thread/conversation views
   - feedback score review surfaces
   - annotation queues
   - online evaluation rules
   - dashboards over structured aggregates

## Sources

- Opik README: https://github.com/comet-ml/opik
- Opik Python SDK search/filter documentation: https://www.comet.com/docs/opik/python-sdk-reference/Opik.html
- Opik online evaluation rules documentation: https://www.comet.com/docs/opik/production/rules
- Opik Helm chart values showing ClickHouse analytics DB and MySQL state DB settings: https://comet-ml.github.io/opik/
- Opik release notes mentioning `TraceDAO` and `SpanDAO` query optimization: https://newreleases.io/project/github/comet-ml/opik/release/1.10.11
