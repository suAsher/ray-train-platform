-- Collector Contrib v0.162.0 ClickHouse exporter Map schema.
-- Source: exporter/clickhouseexporter/internal/sqltemplates (Apache-2.0).
-- Additive-only: no DROP, no ALTER, no historical backfill.
-- The three local tables are replicated; the MV is installed on every replica.
CREATE DATABASE IF NOT EXISTS observability_canary ON CLUSTER telemetry ENGINE = Atomic;

CREATE TABLE IF NOT EXISTS observability_canary.otel_logs ON CLUSTER telemetry
(
    Timestamp DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    TraceId String CODEC(ZSTD(1)),
    SpanId String CODEC(ZSTD(1)),
    TraceFlags UInt8,
    SeverityText LowCardinality(String) CODEC(ZSTD(1)),
    SeverityNumber UInt8,
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    Body String CODEC(ZSTD(1)),
    ResourceSchemaUrl LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeSchemaUrl LowCardinality(String) CODEC(ZSTD(1)),
    ScopeName String CODEC(ZSTD(1)),
    ScopeVersion LowCardinality(String) CODEC(ZSTD(1)),
    ScopeAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    LogAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    EventName String CODEC(ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_body lower(Body) TYPE tokenbf_v1(32768, 3, 0) GRANULARITY 8
)
ENGINE = ReplicatedMergeTree('/clickhouse/observability-analytics/{installation}/{cluster}/tables/{shard}/observability_canary/otel_logs', '{replica}')
PARTITION BY toDate(Timestamp)
ORDER BY (toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)
TTL toDateTime(Timestamp) + INTERVAL 7 DAY DELETE
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS observability_canary.otel_traces ON CLUSTER telemetry
(
    Timestamp DateTime64(9) CODEC(Delta, ZSTD(1)),
    TraceId String CODEC(ZSTD(1)),
    SpanId String CODEC(ZSTD(1)),
    ParentSpanId String CODEC(ZSTD(1)),
    TraceState String CODEC(ZSTD(1)),
    SpanName LowCardinality(String) CODEC(ZSTD(1)),
    SpanKind LowCardinality(String) CODEC(ZSTD(1)),
    ServiceName LowCardinality(String) CODEC(ZSTD(1)),
    ResourceAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    ScopeName String CODEC(ZSTD(1)),
    ScopeVersion String CODEC(ZSTD(1)),
    SpanAttributes Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    Duration UInt64 CODEC(ZSTD(1)),
    StatusCode LowCardinality(String) CODEC(ZSTD(1)),
    StatusMessage String CODEC(ZSTD(1)),
    Events Nested (
        Timestamp DateTime64(9),
        Name LowCardinality(String),
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    Links Nested (
        TraceId String,
        SpanId String,
        TraceState String,
        Attributes Map(LowCardinality(String), String)
    ) CODEC(ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_duration Duration TYPE minmax GRANULARITY 1
)
ENGINE = ReplicatedMergeTree('/clickhouse/observability-analytics/{installation}/{cluster}/tables/{shard}/observability_canary/otel_traces', '{replica}')
PARTITION BY toDate(Timestamp)
ORDER BY (ServiceName, SpanName, toDateTime(Timestamp))
TTL toDateTime(Timestamp) + INTERVAL 7 DAY DELETE
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS observability_canary.otel_traces_trace_id_ts ON CLUSTER telemetry
(
    TraceId String CODEC(ZSTD(1)),
    Start DateTime CODEC(Delta, ZSTD(1)),
    End DateTime CODEC(Delta, ZSTD(1)),
    INDEX idx_trace_id TraceId TYPE bloom_filter(0.01) GRANULARITY 1
)
ENGINE = ReplicatedMergeTree('/clickhouse/observability-analytics/{installation}/{cluster}/tables/{shard}/observability_canary/otel_traces_trace_id_ts', '{replica}')
PARTITION BY toDate(Start)
ORDER BY (TraceId, Start)
TTL Start + INTERVAL 7 DAY DELETE
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;

CREATE MATERIALIZED VIEW IF NOT EXISTS observability_canary.otel_traces_trace_id_ts_mv ON CLUSTER telemetry
TO observability_canary.otel_traces_trace_id_ts
DEFINER = schema_admin SQL SECURITY DEFINER
AS SELECT TraceId, min(Timestamp) AS Start, max(Timestamp) AS End
FROM observability_canary.otel_traces
WHERE TraceId != ''
GROUP BY TraceId;

-- Query through one logical shard; never UNION the two replica copies.
CREATE TABLE IF NOT EXISTS observability_canary.otel_logs_all ON CLUSTER telemetry
AS observability_canary.otel_logs
ENGINE = Distributed(telemetry, observability_canary, otel_logs, cityHash64(TraceId));

CREATE TABLE IF NOT EXISTS observability_canary.otel_traces_all ON CLUSTER telemetry
AS observability_canary.otel_traces
ENGINE = Distributed(telemetry, observability_canary, otel_traces, cityHash64(TraceId));

CREATE TABLE IF NOT EXISTS observability_canary.otel_traces_trace_id_ts_all ON CLUSTER telemetry
AS observability_canary.otel_traces_trace_id_ts
ENGINE = Distributed(telemetry, observability_canary, otel_traces_trace_id_ts, cityHash64(TraceId));
