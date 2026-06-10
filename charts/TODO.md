# TODO — Helm chart (`charts/`)

Plan for a single umbrella Helm chart that deploys the **trino-goway gateway** and the
**routing-service** as two independently-scalable workloads, configured from one `values.yaml`
with camelCase `trinoGoway:` (gateway config) and `routingService:` (routing-service config)
sections. Chart lives at `charts/trino-goway/`.

**Grounding (real config surfaces the chart must template):**
- Gateway (`configs/config.example.yaml`): `proxy.port` 8080 (client-facing), `admin.port` 8090
  (Web UI + admin API + `/metrics` + `/trino-gateway/livez|readyz`), `db.{driver,dsn}` (Postgres
  libpq DSN incl. password), `routing.type: EXTERNAL` + `routing.external.grpcAddr`,
  `auth.*`, `cookie.secret` (HMAC — shared + stable across replicas), `clusterStats.monitorType`,
  `metrics.{enabled,path}`. Runs `goose` migrations on startup (`internal/persistence/db.go`).
- routing-service (`routing-service/docs/config.example.yaml`): `addr` `:9001` (gRPC data-plane —
  the gateway connects here), `metricsAddr` `:9091` (Prometheus HTTP), `adminAddr` `:9092` (admin
  gRPC kill-switch), `defaultRoutingGroup`, `sqlParsing.*`, `methods:` (expr/Starlark rules,
  hot-reloaded via fsnotify), `tracingEndpoint`. gRPC health (`grpc.health.v1`). Stateless.

---

## Design rulings (binding on all tasks)

- **R1 — One umbrella chart, two component template dirs** (`templates/gateway/`,
  `templates/routing-service/`), not two separate charts and not Helm subcharts. Matches the
  "one values file, two sections" request and lets the gateway↔routing wiring + shared labels
  live in one place. *(Subcharts would be the alternative only if the two needed independent
  release cadences.)*
- **R2 — Values keyed `trinoGoway:` (gateway) and `routingService:` (routing-service)** —
  camelCase, dot-accessible everywhere (`.Values.trinoGoway.*`, `.Values.routingService.*`),
  under a shared `global:` block. camelCase is chosen specifically so templates use clean
  dot-access with no `index .Values "…"` indirection.
- **R3 — Migration safety is the gating concern for a scalable gateway.** The gateway runs
  `goose.Up` on every startup and `goose` does **not** take a lock, so N replicas booting
  together can race (see the migration best-practice note). Resolve before scaling >1:
  - **Prereq (Go change, recommended): advisory-lock `MigrateUp`** — wrap migrations in a
    `pg_advisory_lock` (Postgres) / `GET_LOCK` (MySQL) so concurrent replicas serialize safely.
    Robust everywhere (not just under Helm). Tracked as **HC-9** (touches `internal/persistence`).
  - **Helm-native complement: a `pre-install`/`pre-upgrade` Job hook** runs migrations once; add
    a gateway `--skip-migrations` flag (or `db.autoMigrate: false`) so replicas don't re-run.
  - Chart default: ship the advisory-lock (HC-9) **and** offer `migrations.strategy: hook|inline|disabled`.
- **R4 — Service topology.** Gateway exposes **two Services**: `*-proxy` (client-facing,
  `type` configurable LoadBalancer/ClusterIP, + optional Ingress) and `*-admin` (internal
  ClusterIP: UI/API/metrics/probes). routing-service exposes a **gRPC Service** (`:9001`,
  ClusterIP; optional `clusterIP: None` headless for client-side gRPC LB) and a **metrics
  Service** (`:9091`). The gateway's `routing.external.grpcAddr` is templated to
  `<release>-routing-service:9001` when unset.
- **R5 — Scalability primitives for both:** configurable `replicaCount` + optional `HPA`
  (CPU/memory, `autoscaling.enabled`) + `PodDisruptionBudget` + `topologySpreadConstraints` /
  pod anti-affinity. `RollingUpdate` strategy with sane `maxSurge`/`maxUnavailable`.
- **R6 — Health probes.** Gateway: HTTP `GET /trino-gateway/livez` (liveness) + `readyz`
  (readiness) on the admin port. routing-service: native k8s **gRPC probe** on `:9001`
  (`grpc:` probe, k8s ≥1.24); fallback `grpc_health_probe` exec for older clusters (toggle).
- **R7 — Secrets vs ConfigMap.** Non-secret config → ConfigMap (templated `config.yaml` per
  service). Secrets (DB password, `cookie.secret`, OIDC client secret, LDAP bind password) →
  Secret, with `existingSecret` BYO support (for external-secrets/Vault). Add a
  `checksum/config` pod annotation so config changes trigger a rolling restart.
- **R8 — Database.** External Postgres by default: build the DSN from structured values
  (`db.host/port/name/user/sslmode` + password from Secret) with a raw `db.dsn` override.
  Optional bundled **Bitnami `postgresql` subchart dependency** (`postgresql.enabled: false`
  default) for dev/demo only.
- **R9 — Config rendering.** Template each service's `config.yaml` from its values block into a
  ConfigMap, mount read-only, pass `--config`. routing-service `methods:` (expr/Starlark rule
  bodies) render into the same ConfigMap and are hot-reloaded.
- **R10 — Validation/CI per task:** `helm lint`, `helm template | kubeconform -strict`
  (or `kubeval`), and `helm unittest` golden tests are the DoD for every task that adds templates.

Chart skeleton:
```
charts/trino-goway/
  Chart.yaml  values.yaml  values.schema.json  README.md  .helmignore
  templates/
    _helpers.tpl  NOTES.txt
    gateway/{configmap,secret,deployment,service-proxy,service-admin,ingress,hpa,pdb,
             serviceaccount,servicemonitor,migrate-job}.yaml
    routing-service/{configmap,secret,deployment,service-grpc,service-metrics,hpa,pdb,
             serviceaccount,servicemonitor}.yaml
    networkpolicy.yaml
  charts/                      # optional postgresql dependency
  tests/                       # helm-unittest specs
```

Critical path: **HC-1 → (HC-2 ∥ HC-5) → HC-8**. HC-9 (Go migration lock) is a prereq for
scaling the gateway and can proceed in parallel. HC-2…HC-4 (gateway) and HC-5…HC-7
(routing-service) are independent once HC-1 lands.

---

## Phase HC-0: Scaffold & conventions

### Task HC-1 — Chart scaffold, values layout, helpers
- [x] `charts/trino-goway/Chart.yaml` — apiVersion v2, name trino-goway, type application, version 0.1.0, appVersion 0.1.0, kubeVersion >=1.24.0-0, maintainers, sources, optional postgresql dep
- [x] `charts/trino-goway/values.yaml` — `global:` + `trinoGoway:` + `routingService:` blocks per R2; full scheduling/security/scaling scaffolding + `config.*` per service
- [x] values.yaml uses camelCase `trinoGoway:` / `routingService:` — dot-accessible throughout, no `index`; layout documented in README
- [x] `charts/trino-goway/templates/_helpers.tpl` — name/fullname/chart, per-component labels+selectorLabels, image builder, imagePullSecrets, serviceAccountName, DSN builder, routingGrpcAddr auto-wire, autoMigrate, podAntiAffinity, secretName
- [x] `charts/trino-goway/values.schema.json` — enums for auth.type, clusterStats.monitorType, db.driver, service.type, migrations.strategy, method.type, image.pullPolicy
- [x] `charts/trino-goway/templates/NOTES.txt` — proxy/admin/UI URLs, routing-service reach, migration-strategy note, cookie-secret warning
- [x] `charts/trino-goway/.helmignore`, `charts/trino-goway/README.md` (stub → helm-qa filled it)
- [x] **DoD:** `helm lint charts/trino-goway` passes; `helm template charts/trino-goway` renders. NOTE: helm v4 here requires the postgresql dep **unpacked** under `charts/postgresql/` (not just the `.tgz`) for `helm template` to resolve — `helm dependency build` downloads the tgz; unpack it (or commit the unpacked dir).

---

## Phase HC-1: Gateway workload (`trino-goway`)

### Task HC-2 — Gateway config (ConfigMap + Secret)
- [x] `templates/gateway/configmap.yaml` — renders the gateway `config.yaml` (non-secret view) from `.Values.trinoGoway.config` via the `trino-goway.gateway.configYaml` helper: proxy/admin/monitor(incl. refreshInterval)/clusterStats/backendState/db(autoMigrate)/routing(EXTERNAL + auto-wired grpcAddr)/auth/ui/metrics. Secret fields blanked here.
- [x] `templates/gateway/secret.yaml` — renders the **full** mounted `config.yaml` (cookie.secret, OIDC client secret, LDAP bind password, backendState password filled in) + discrete `db-password` key; gated on `existingSecret` (skip creation; BYO must provide `config.yaml`+`db-password`). DESIGN NOTE below.
- [x] DSN assembly (R8): `trino-goway.gateway.dsn` builds a password-less libpq DSN from `db.{host,port,name,user,sslmode}` (or raw `db.dsn` override; bundled-PG host auto-filled); password exposed as `db-password` and injected via PGPASSWORD env (lib/pq) — never in the config file.
- [x] **DoD:** `helm template` renders; the rendered `config.yaml` **round-trips through the real `internal/config.Load`** (default + OIDC + LDAP + METRICS cases) with no dropped keys. kubeconform/unittest are helm-qa's.

> **DESIGN NOTE (R7/R9 deviation — flagged to lead).** The gateway binary reads ONE YAML file with **no env expansion** (verified: only the test harness uses `os.Getenv`). So non-DB secrets (cookie.secret, OIDC/LDAP/backendState) cannot be a separate ConfigMap — the mounted `--config` must be a Secret. Hence: ConfigMap = non-secret audit/diff view (not mounted); Secret = the live mounted config. DB password is the exception (PGPASSWORD/lib/pq), kept out of the file as a discrete key. To restore the clean R9 "mount config from ConfigMap + secrets via env" model, the gateway needs a small Go change to read these secrets from env (proposed to lead/go-impl).

### Task HC-3 — Gateway Deployment
- [x] `templates/gateway/deployment.yaml` — Deployment (replicas from `replicaCount`, omitted when `autoscaling.enabled`), two named container ports (proxy 8080, admin 8090), `--config /etc/trino-goway/config.yaml` from the Secret-mounted config (read-only, defaultMode 0440), `PGPASSWORD` env from the `db-password` Secret key (optional), `checksum/config` + `checksum/secret` annotations (R7), `RollingUpdate` strategy
- [x] Probes (R6): `livenessProbe` HTTP `GET /trino-gateway/livez` :admin; `readinessProbe` `GET /trino-gateway/readyz` :admin; `startupProbe` (readyz, 30×5s) for slow DB connect
- [x] securityContext (non-root, read-only rootfs + writable `/tmp` emptyDir), resources, nodeSelector/affinity(default soft anti-affinity)/tolerations/topologySpread, `terminationGracePeriodSeconds: 40` (≥ 30s proxy drain)
- [x] `templates/gateway/serviceaccount.yaml` (automountServiceAccountToken false — no k8s API access)
- [x] **DoD:** `helm template` renders; verified replicas omitted under HPA, probes/ports/checksums present. kubeconform/unittest are helm-qa's.

### Task HC-4 — Gateway Services + Ingress
- [x] `templates/gateway/service-proxy.yaml` — client-facing proxy `:8080`; `type` configurable (LoadBalancer/NodePort/ClusterIP), annotations, `externalTrafficPolicy`, `loadBalancerSourceRanges`, optional `nodePort`
- [x] `templates/gateway/service-admin.yaml` — internal `ClusterIP` `:8090` (UI/API/metrics/probes)
- [x] `templates/gateway/ingress.yaml` — optional, per-host/path `backend: proxy|admin` routing; `className`, hosts, TLS
- [x] **DoD:** `helm template` renders across type=LoadBalancer/NodePort + ingress on/off. kubeconform/unittest are helm-qa's.

---

## Phase HC-2: Routing-service workload (`routing-service`)

### Task HC-5 — Routing-service config (ConfigMap + Secret) + Deployment
- [ ] `templates/routing-service/configmap.yaml` — render routing-service `config.yaml` from `.Values.routingService.config`: `addr :9001`, `metricsAddr :9091`, `adminAddr :9092`, `defaultRoutingGroup`, `sqlParsing.*`, `tracingEndpoint`, and `methods:` (expr/Starlark rule bodies templated in; hot-reloaded)
- [x] `templates/routing-service/secret.yaml` — placeholder Secret (renders only when `routingService.secrets` non-empty; Phase-1 insecure); `existingSecret` support
- [x] `templates/routing-service/deployment.yaml` — three named container ports parsed from config addrs (grpc 9001, metrics 9091, admin-grpc 9092), `--config /etc/routing-service/config.yaml` ConfigMap mount + `checksum/config`, non-root/read-only-rootfs securityContext, resources, scheduling knobs, 30s drain
- [x] Probes (R6): native `grpc:` startup/liveness/readiness on :9001; `grpc_health_probe` exec fallback via `probes.useGrpcProbe=false`
- [x] `templates/routing-service/serviceaccount.yaml`
- [x] **DoD:** `helm template` renders; routing config.yaml **round-trips through the real routing-service `config.Load`** (addr/methods/sqlParsing parse). kubeconform/unittest are helm-qa's.

### Task HC-6 — Routing-service Services
- [x] `templates/routing-service/service-grpc.yaml` — `:9001` ClusterIP; optional `clusterIP: None` headless (`service.grpc.headless`); `appProtocol: grpc`
- [x] `templates/routing-service/service-metrics.yaml` — `:9091` ClusterIP (Prometheus scrape target)
- [x] **DoD:** `helm template` renders headless and clusterIP variants. kubeconform/unittest are helm-qa's.

---

## Phase HC-3: Scalability, cross-cutting & dependencies

### Task HC-7 — Scalability primitives (both components)
- [x] `templates/gateway/hpa.yaml` + `templates/routing-service/hpa.yaml` — HPA (autoscaling/v2), CPU + optional memory metric (target=0 disables); Deployments omit `replicas` when `autoscaling.enabled`
- [x] `templates/gateway/pdb.yaml` + `templates/routing-service/pdb.yaml` — PDB (`minAvailable`/`maxUnavailable`), gated on `podDisruptionBudget.enabled && (replicaCount>1 || autoscaling.enabled)`
- [x] Pod anti-affinity (soft default, `podAntiAffinity: hard` for required) in `_helpers.tpl`, referenced by both Deployments; user `topologySpreadConstraints`/`affinity` override it
- [x] **DoD:** verified via `helm template` — HPA strips `replicas`, PDB absent at replicaCount=1, anti-affinity renders. helm-unittest is helm-qa's.

### Task HC-8 — Cross-service wiring, global, optional Postgres, NetworkPolicy
- [x] Auto-wire `routing.external.grpcAddr` → `<release>-trino-goway-routing-service:9001` when unset & `routingService.enabled` (helper `trino-goway.gateway.routingGrpcAddr`); explicit `grpcAddr`/`url` override; empty when routing disabled
- [x] `global.{imageRegistry,imagePullSecrets,commonLabels,storageClass}` applied to both via `_helpers.tpl` (image/imagePullSecrets/commonLabels helpers; storageClass flows to the postgres subchart)
- [x] `Chart.yaml` Bitnami `postgresql` dep (condition `postgresql.enabled`, default false); when enabled the DSN host defaults to `<fullname>-postgresql` and `db-password` is taken from `postgresql.auth.password`
- [x] `templates/networkpolicy.yaml` (`networkPolicy.enabled`) — per-component policies: clients→proxy `:8080`, scrape/UI→admin `:8090`, gateway→routing gRPC `:9001`, scrape→routing metrics `:9091`; default-deny implicit once selected
- [x] **DoD:** verified — `routingService.enabled=true` points the gateway at the in-cluster Service; `postgresql.enabled=true` resolves the dep (20 resources, kubeconform 0 errors)

### Task HC-9 — [Prereq, Go change] Concurrency-safe migrations
- [x] `internal/persistence/db.go::MigrateUp` — wrap migrations in a DB advisory lock: Postgres `pg_advisory_lock(key)` / `pg_advisory_unlock`; MySQL `GET_LOCK(name, timeout)` / `RELEASE_LOCK` — so N gateway replicas serialize and only one applies (R3)
- [x] Optional `db.autoMigrate` config switch (default true) so a Helm migrate-Job can own migrations and replicas skip them (`persistence.Open` skips `MigrateUp` when false)
- [x] `templates/gateway/migrate-job.yaml` (written by helm-impl) — `pre-install`/`pre-upgrade` hook Job (gated `migrations.strategy=hook`) + its own hook-scoped config Secret (weight -5, the Deployment Secret doesn't exist at pre-install); Job weight 0, `hook-delete-policy: before-hook-creation,hook-succeeded`, `backoffLimit`. **BLOCKED ON go-impl:** the Job runs `--migrate-only`, which must run `MigrateUp` then exit 0 WITHOUT starting the proxy/admin servers — that flag does NOT exist yet in `cmd/trino-goway` (only `--config`). Needs adding for the hook strategy to work.
- [x] Unit + integration test (testcontainers): two concurrent `MigrateUp` against one DB → no error, single apply (`internal/persistence/migrate_test.go`)
- [ ] **DoD:** `go build/vet/test -race/golangci-lint` green; chart renders the migrate Job only under `strategy=hook`

---

## Phase HC-4: Observability, validation, docs, CI

### Task HC-10 — Observability
- [x] `templates/gateway/servicemonitor.yaml` — `ServiceMonitor` scraping admin `:8090{metrics.path}` (gated `serviceMonitor.enabled`); admin Service carries `trino-goway.io/endpoint: admin` so the SM targets it only; pod scrape annotations (`metrics.podAnnotations`) are the non-Operator fallback
- [x] `templates/routing-service/servicemonitor.yaml` — scrape metrics `:9091` (metrics Service labelled `trino-goway.io/endpoint: metrics`)
- [x] Optional `tracingEndpoint` wired in the routing-service config (`routingService.config.tracingEndpoint` → OTLP collector)
- [x] **DoD:** `helm template` renders SM on/off; kubeconform clean (SM CRD schema is helm-qa's to wire up). helm-unittest is helm-qa's.

### Task HC-11 — Chart tests (helm-unittest + kubeconform)
- [x] `charts/trino-goway/tests/*_test.yaml` — `helm unittest` suites: default install, gateway-only (`routingService.enabled=false`), routing-only, OIDC auth, existingSecret, HPA+PDB, bundled Postgres, camelCase value access, gateway→routing `grpcAddr` auto-wiring, `migrations.strategy` matrix (8 suites, 32 tests)
- [x] `helm template … | kubeconform -strict -summary` across a values matrix (default / scaled / ingress / hook-migrations / servicemonitor / networkpolicy / postgres / gateway-only / routing-only)
- [x] **DoD:** `helm unittest charts/trino-goway` green (32/32); kubeconform 0 errors across the matrix (CRD schemas via datree CRDs-catalog for ServiceMonitor)

### Task HC-12 — Docs & CI
- [x] `charts/trino-goway/README.md` (generated from `README.md.gotmpl`) — purpose, architecture (gateway + routing-service), install/upgrade, the camelCase `trinoGoway:`/`routingService:` values layout, a generated values table (`helm-docs`), the migration-strategy note (R3), secrets model, production checklist (external DB, secrets, HPA, PDB, NetworkPolicy)
- [x] Root `README.md` — "Kubernetes (Helm)" section linking the chart + a quickstart (`helm install`) + TOC entry
- [x] CI workflow `.github/workflows/helm-chart.yaml` — `helm lint`, `helm unittest`, `kubeconform` matrix, plus a best-effort `kind` install smoke (build gateway+routing images → install → wait Ready → curl `/trino-gateway/livez` → gRPC health on routing-service)
- [x] Chart versioning policy: bump `version` per chart change, keep `appVersion` synced to the app release (documented in the chart README "Versioning" section)
- [x] **DoD:** local lint+unittest+kubeconform green; `helm install` on kind brings both workloads to Ready (validated: gateway → plain Postgres + inline migrations, `/trino-gateway/livez`+`readyz` → 200, routing-service gRPC health → SERVING). NOTE: bundled-PG subchart path has a DSN host-mismatch bug — see HC-8 follow-up below.

---

## Backlog / future

- mTLS between gateway↔routing-service (routing-service Phase-1 is insecure gRPC) — certs via cert-manager + Secret.
- External-secrets / Vault integration examples.
- OpenShift `Route` + SecurityContextConstraints variant.
- Separately-published charts (split umbrella into `gateway` + `routing-service` subcharts) if release cadences diverge.
- KEDA / custom-metrics autoscaling (e.g. scale gateway on in-flight proxy requests, routing-service on RPC rate).
- **DB-less gateway mode (Go).** `config.example.yaml` implies a blank `db.driver` runs without persistence, but `Validate()` fatals ("db.driver must be configured"). Either support a real db-less mode or fix the comment; until then the chart requires a DB (external by default, optional bundled PG).
- **R7/R9 env-secret model (Go).** The gateway has no env-expansion in config loading, so the live mounted `--config` is a Secret containing all secrets (cookie/OIDC/LDAP/backendState); the ConfigMap is a non-secret audit view and the DB password is the one discrete `PGPASSWORD` key. A small Go change to read those 4 secrets from env vars would restore the cleaner "mount config from ConfigMap + secrets via env" model.
- **helm-docs `# --` value annotations.** `values.yaml` has no helm-docs key annotations, so the generated values table has empty descriptions. Adding `# -- ` annotations would enrich it (README prose already documents the layout).
- **Bundled Postgres image.** The optional bundled PG defaults to Bitnami's `bitnamilegacy/postgresql` (frozen archive, per Bitnami's 2025 community-image migration) so `--set postgresql.enabled=true` works out-of-box; it is dev/demo only — production should use a managed/external Postgres (chart default `postgresql.enabled=false`).
