# Observability business integration implementation plan

> **For agentic workers:** Use subagent-driven development with disjoint file ownership and independent review. Run all executable validation on the existing builder, never on the workstation.

**Goal:** Deliver a persistent Grafana HA UI with VM/Loki/ClickHouse query sources and real, privacy-bounded backend HTTP metrics, traces and request events, while preserving existing training and log collection.

**Architecture:** Keep old Grafana and all training workloads running. Add a dedicated Grafana namespace, two Grafana instances and three PostgreSQL instances under a separately scoped CNPG operator. Backend telemetry uses a separate native mTLS metrics listener and a bounded asynchronous OTLP path to a separate production Collector pipeline and replicated ClickHouse tables. No user code or submission protocol changes.

**Tech Stack:** Existing pinned VictoriaMetrics Operator, cert-manager, Envoy, CNPG, Grafana and ClickHouse/OTel releases; Go Prometheus and OTel SDKs.

## Ownership and baseline

- Main `39b09582cc865d6197dbe1a432c02292d8540397` merged non-destructively into `codex/observability-additive`. Preserve unrelated changes in the primary checkout.
- Backend worker owns `backend/telemetry/`, telemetry configuration, `backend/main.go`, and Go module files.
- Pipeline worker owns `ops/observability/next/otel/`, necessary ClickHouse additions and `tests/test_platform_pipeline.py`.
- Grafana worker owns `ops/observability/next/grafana/` and `tests/test_grafana_ha.py`.
- Coordinator owns platform Helm wiring, `next/platform-telemetry/`, VM read-role additions, compatibility tests, UI cutover, rollout and evidence.
- User explicitly selected independent Grafana plus dedicated HA PostgreSQL on 2026-10-13. Alert channels remain user-configured in Nightingale; do not send notifications or enable unattended native certificate evictions without the notification acceptance gate.

## 1. Protected baseline and concrete configuration

- [x] Read release rules, existing deployment records and source state; verify production availability read-only. The original seven namespaces currently have 72 Ready Pods and 28 Ready Certificates.
- [ ] Save private, time-stamped Helm values and non-secret resource/UID/spec/restart baselines on `root@14.103.49.106`. Include existing Grafana configurations and private recoverable database snapshots before any UI cutover; do not delete old instances.
- [ ] Freeze endpoint contracts: backend metrics `https://backend-metrics.ray-train-platform.svc:9091/metrics`; OTLP `https://otel-gateway.opentelemetry.svc:14318/v1/{traces,logs}`; backend URI `spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry`; Grafana query URI `spiffe://observability.raytrain.local/grafana/grafana-query`.

## 2. Backend privacy and failure-isolation tests

Files: `backend/telemetry/*_test.go`, `backend/config/telemetry*_test.go`, `backend/main.go` and corresponding implementation.

- [ ] Write failing tests for bounded HTTP labels, rejected unknown endpoints/certificates, retained business status/body, unsampled user baggage, request metadata redaction, queue overflow, bounded shutdown and Collector outage.
- [ ] Commit only the owned tests; bundle to a detached builder checkout and run `go test ./telemetry ./config` to record RED.
- [ ] Implement `TELEMETRY_ENABLED=false` default, independently authenticated metrics listener, official SDK exporters, request middleware and bounded counters. Live credentials are Secret references, never constants or command output.
- [ ] Run targeted tests, race tests and coverage on the builder. Require new package coverage at least 80%; report whole-repository coverage separately.

## 3. Separate production analytics pipeline

Files: `next/otel/{collector.yaml,gateway.yaml,proxy-otel.yaml}`, `next/clickhouse/*`, `next/tests/test_platform_pipeline.py`.

- [ ] RED contracts keep the existing canary pipeline and add exact backend identity on a separate 14318 listener; anonymous, wrong-role and wrong-route requests fail.
- [ ] Add production log/trace replicated tables and distributed query tables in `raytrain_observability`. Use insert-only production ingestion and separately bounded read-only Grafana credentials; retain canary data and identities.
- [ ] Collector strips unsupported resource/span/log attributes and event/link attributes, enforces bounded bodies/queues and fixed platform source metadata. Test sensitive marker rejection and known safe event round trips.
- [ ] Execute real Collector config validation and isolated ClickHouse ingestion/query/negative-permission checks. Verify logical counts once across replicas, not summed replica copies.

## 4. Independent Grafana HA and database

Files: `next/grafana/`, `next/tests/test_grafana_ha.py`.

- [ ] Record RED tests for namespace isolation, credentials, exact certificate issuance policies, native database TLS and shared PostgreSQL configuration.
- [ ] Implement fixed Grafana/plugin image, two Grafana replicas, dedicated three-instance PG, scoped CNPG operator and persistent/Retain storage. Keep old monitoring release unchanged.
- [ ] Provision Prometheus/VM/Loki/ClickHouse data sources and useful infrastructure/backend/log/trace views, with separate role identities and bounded read permissions.
- [ ] Validate pinned image, plugin signature, chart/render/post-render output, native TLS and wrong-role rejection. Verify a saved UI object is present across both replicas and survives one controlled new-instance restart.
- [ ] Back up new PG and verify isolated restore on the builder. Do not claim backup automation or recovery guarantees without evidence.

## 5. Scraping and query identities

Files: `helm/ray-train-platform/{values.yaml,templates/backend-deployment.yaml}`, `next/platform-telemetry/`, VM resources and tests.

- [ ] Add tests before implementation. The public backend Service must remain single-port; enabled telemetry only adds named container port 9091 and an independent ClusterIP Service in the separate chart.
- [ ] Add an exact backend Certificate/Policy, limited VM discovery Role/Binding and TLS VMServiceScrape. No training namespace permissions or wildcard Secret reads.
- [ ] Add an opt-in Grafana read URI and separately revocable VMUser token with query-only paths; preserve all existing read/write identities. Default renders remain compatible and do not enable new consumers implicitly.

## 6. Candidate gates, rollout and acceptance

- [ ] Consolidate source; run all Python infrastructure contracts, real Helm/Kustomize/SDS validation, Go format/vet/full regression and actual PostgreSQL integration tests on isolated builder candidates.
- [ ] Independent security/code review, then server-side dry-run and exact diffs. Preserve existing VM `businessScrape.enabled=true`, Nightingale HA and SDS rendering chains.
- [ ] Deploy new Grafana/database and production analytics prerequisites, then enable backend telemetry through a minimal backend image/config update only. Keep old ingress and Grafana as rollback until the new UI is verified.
- [ ] Verify real authorized API calls appear as finite metrics and correlated traces/logs, unauthorized metrics reads fail, Collector failure does not break request processing, and training resource identities are unchanged.
- [ ] Switch the existing Grafana NodePort to the accepted new Grafana over verified mTLS. Test login, shared UI state, VM queries, Loki metadata-only queries, ClickHouse logs and trace correlation, WebSocket and rollback target. Do not read user log content for synthetic acceptance.
- [ ] Record image digests, source SHA, Helm revisions, created resources, new totals, actual limitations and evidence in the existing operations/runbook documentation.

## Remaining separate gates

Full asynchronous submission → outbox → reconcile → Worker trace propagation needs additional context storage and lifecycle tests; an HTTP trace does not represent an entire multi-day training run. Nightingale recipients/channels are user-configured. Root CA rotation, cross-site recovery, historical data migration, and ALB/SSO integration are separate from this additive business integration and must not be silently declared complete.
