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
- [x] Save non-secret resource/UID/spec/restart baselines under `baseline-public` on the builder; retain Helm history and the unchanged old Grafana. Existing production Secret export was rejected and not executed. New Grafana backup/restore is tracked separately below.
- [x] Freeze endpoint contracts: backend metrics `https://backend-metrics.ray-train-platform.svc:9091/metrics`; OTLP `https://otel-gateway.opentelemetry.svc:14318/v1/{traces,logs}`; backend URI `spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry`; Grafana query URI `spiffe://observability.raytrain.local/grafana/grafana-query`.

## 2. Backend privacy and failure-isolation tests

Files: `backend/telemetry/*_test.go`, `backend/config/telemetry*_test.go`, `backend/main.go` and corresponding implementation.

- [x] Write failing tests for bounded HTTP labels, rejected unknown endpoints/certificates, retained business status/body, unsampled user baggage, request metadata redaction, queue overflow, bounded shutdown and Collector outage.
- [x] Commit only the owned tests; bundle to a detached builder checkout and run `go test ./telemetry ./config` to record RED.
- [x] Implement `TELEMETRY_ENABLED=false` default, independently authenticated metrics listener, official SDK exporters, request middleware and bounded counters. Live credentials are Secret references, never constants or command output.
- [x] Run targeted tests, race tests and coverage on the builder. Require new package coverage at least 80%; report whole-repository coverage separately.

## 3. Separate production analytics pipeline

Files: `next/otel/{collector.yaml,gateway.yaml,proxy-otel.yaml}`, `next/clickhouse/*`, `next/tests/test_platform_pipeline.py`.

- [x] RED contracts keep the existing canary pipeline and add exact backend identity on a separate 14318 listener; anonymous, wrong-role and wrong-route requests fail.
- [x] Add production log/trace replicated tables and distributed query tables in `raytrain_observability`. Use insert-only production ingestion and separately bounded read-only Grafana credentials; retain canary data and identities.
- [x] Collector strips unsupported resource/span/log attributes and event/link attributes, enforces bounded bodies/queues and fixed platform source metadata. Test sensitive marker rejection and known safe event round trips.
- [x] Execute real Collector config validation and isolated ClickHouse ingestion/query/negative-permission checks. Verify logical counts once across replicas, not summed replica copies.

## 4. Independent Grafana HA and database

Files: `next/grafana/`, `next/tests/test_grafana_ha.py`.

- [x] Record RED tests for namespace isolation, credentials, exact certificate issuance policies, native database TLS and shared PostgreSQL configuration.
- [x] Implement fixed Grafana/plugin image, two Grafana replicas, dedicated three-instance PG, scoped CNPG operator and persistent/Retain storage. Keep old monitoring release unchanged.
- [x] Provision Prometheus/VM/Loki/ClickHouse data sources and useful infrastructure/backend/log/trace views, with separate role identities and bounded read permissions.
- [x] Validate pinned image, plugin signature, chart/render/post-render output, native TLS and wrong-role rejection. Verify a saved UI object is present across both replicas and survives one controlled new-instance restart.
- [x] Back up new PG and verify isolated restore on the builder. Do not claim backup automation or recovery guarantees without evidence.

## 5. Scraping and query identities

Files: `helm/ray-train-platform/{values.yaml,templates/backend-deployment.yaml}`, `next/platform-telemetry/`, VM resources and tests.

- [x] Add tests before implementation. The public backend Service must remain single-port; enabled telemetry only adds named container port 9091 and an independent ClusterIP Service in the separate chart.
- [x] Add an exact backend Certificate/Policy, limited VM discovery Role/Binding and TLS VMServiceScrape. No training namespace permissions or wildcard Secret reads.
- [x] Add an opt-in Grafana read URI and separately revocable VMUser token with query-only paths; preserve all existing read/write identities. Default renders remain compatible and do not enable new consumers implicitly.

## 6. Candidate gates, rollout and acceptance

- [x] Consolidate source; run all Python infrastructure contracts, real Helm/Kustomize/SDS validation, Go format/vet/full regression and actual PostgreSQL integration tests on isolated builder candidates.
- [x] Independent security/code review, then server-side dry-run and exact diffs. Preserve existing VM `businessScrape.enabled=true`, Nightingale HA and SDS rendering chains.
- [x] Deploy new Grafana/database and production analytics prerequisites, then enable backend telemetry through a minimal backend image/config update only. Keep old ingress and Grafana as rollback until the new UI is verified.
- [x] Verify real HTTP requests appear as finite metrics and correlated traces/logs, anonymous and wrong-role private-metrics reads fail, and Collector failure is isolated in builder tests. Verify retained training resource identities and separately attribute completed/canceled/failed lifecycle changes; do not claim all historical UIDs are unchanged.
- [x] Switch the existing Grafana NodePort to the accepted new Grafana over verified mTLS. Test login, shared UI state, VM queries, Loki metadata-only queries, ClickHouse logs and trace correlation, WebSocket and rollback target. Do not read user log content for synthetic acceptance.
- [x] Record image digests, source SHA, Helm revisions, created resources, new totals, actual limitations and evidence in the existing operations/runbook documentation.

## 2026-10-13 live checkpoint

Backend code `2b2f38e0b20056761d64639bd23f392382cab6e3` passed format/vet, 5351 tests across 37 packages and real PostgreSQL validation; telemetry race and coverage 86.5% passed. Backend Helm revision 274 is Ready 2/2. VM revision 2 and platform-telemetry revision 1 are deployed; real HTTP metrics are queryable in tenant 1001 with both backend targets UP. Production CH/OTel is deployed while retaining the canary and all existing storage identities. Independent Grafana, its scoped CNPG operator and three-instance PG are Ready; seven new scrape endpoints are UP. PG primary 1 → 2 switchover and cross-replica persisted state passed. All four datasources passed on each Grafana Pod; backend traces/logs/lookup were correlated on both CH replicas with privacy allowlist checks and no exporter failures/drops. Isolated logical restore passed against new Grafana data only, with no external network or published ports. UI revision 2 cut over to independent Grafana; both node endpoints passed login, four datasources, production dashboard, actual VM/CH queries, WebSocket/Origin and authentication-negative checks. Old SQLite accounts and UI content are retained separately, not migrated. Final infrastructure candidate 059583287 passed 366 tests. Complete 79-Pod metrics sampling and 32 Bound PVCs were verified after cutover.

Resource and lifecycle details, evidence locations and exact image digests are in `ops/observability/next/DEPLOYMENT_RUNBOOK.md`. Retained training objects are unchanged; two canceled tasks and one application-failed task were cleaned before the new backend rollout. Do not report all historical training UIDs as unchanged.

## Remaining separate gates

Full asynchronous submission → outbox → reconcile → Worker trace propagation needs additional context storage and lifecycle tests; an HTTP trace does not represent an entire multi-day training run. Nightingale recipients/channels are user-configured. Root CA rotation, cross-site recovery, historical data migration, and ALB/SSO integration are separate from this additive business integration and must not be silently declared complete.
