-- VM resource instrumentation: per-second CPU + memory metrics for every VM.
--
-- Two tables, both partitioned by day on `ts`:
--   - vm_metrics_1s — 1-second resolution, ~24 h retention
--   - vm_metrics_1m — 1-minute aggregates, 7 d retention
--
-- account_id and host_id are denormalized at ingest so user-facing reads
-- can authorize against deleted-VM history without joining the live VM
-- row (machines / browser_vms use hard delete).
--
-- No FK to machines.id / browser_vms.id — metrics outlive the VM row.
-- The (vm_id, kind, ts) PK and the (account_id, vm_id, kind, ts) index
-- match the access path: cursor reads (?since=lastTs) and ranged history
-- both filter by account + vm + kind + ts.

CREATE TABLE vm_metrics_1s (
    account_id INT          NOT NULL,
    host_id    INT          NOT NULL,
    vm_id      UUID         NOT NULL,
    kind       TEXT         NOT NULL CHECK (kind IN ('machine', 'browser')),
    ts         TIMESTAMPTZ  NOT NULL,
    cpu_pct    REAL,
    mem_bytes  BIGINT       NOT NULL CHECK (mem_bytes >= 0),
    PRIMARY KEY (vm_id, kind, ts)
) PARTITION BY RANGE (ts);

CREATE INDEX vm_metrics_1s_account_vm_ts ON vm_metrics_1s (account_id, vm_id, kind, ts);

CREATE TABLE vm_metrics_1m (
    account_id   INT          NOT NULL,
    host_id      INT          NOT NULL,
    vm_id        UUID         NOT NULL,
    kind         TEXT         NOT NULL CHECK (kind IN ('machine', 'browser')),
    ts           TIMESTAMPTZ  NOT NULL,
    cpu_pct      REAL,
    mem_bytes    BIGINT       NOT NULL CHECK (mem_bytes >= 0),
    sample_count INT          NOT NULL,
    finalized    BOOLEAN      NOT NULL DEFAULT FALSE,
    PRIMARY KEY (vm_id, kind, ts)
) PARTITION BY RANGE (ts);

CREATE INDEX vm_metrics_1m_account_vm_ts ON vm_metrics_1m (account_id, vm_id, kind, ts);

-- Bootstrap partitions: create CURRENT_DATE + 3 future days so the
-- ingest path has somewhere to write before the maintenance cron
-- (Step 6) takes over. EnsurePartitions will keep the runway extended.
DO $$
DECLARE
    d date;
    part_name text;
    start_ts timestamptz;
    end_ts   timestamptz;
    i        int;
BEGIN
    FOR i IN 0..3 LOOP
        d := CURRENT_DATE + i;
        start_ts := d::timestamptz;
        end_ts   := (d + 1)::timestamptz;

        part_name := format('vm_metrics_1s_y%sm%sd%s',
            to_char(d, 'YYYY'), to_char(d, 'MM'), to_char(d, 'DD'));
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF vm_metrics_1s FOR VALUES FROM (%L) TO (%L)',
            part_name, start_ts, end_ts);

        part_name := format('vm_metrics_1m_y%sm%sd%s',
            to_char(d, 'YYYY'), to_char(d, 'MM'), to_char(d, 'DD'));
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF vm_metrics_1m FOR VALUES FROM (%L) TO (%L)',
            part_name, start_ts, end_ts);
    END LOOP;
END $$;
