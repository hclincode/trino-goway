# trino-goway

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square)

Umbrella chart for the trino-goway Trino gateway and its companion
routing-service. Deploys the two as independently-scalable workloads from a
single values file (camelCase `trinoGoway:` and `routingService:` sections).

## What this chart deploys

`trino-goway` is an umbrella chart that installs **two independently-scalable
workloads** from a single values file:

| Component | Values key | Role | Ports |
|-----------|------------|------|-------|
| **Gateway** (`trino-goway`) | `trinoGoway.*` | Trino-protocol reverse proxy + load balancer; serves the web UI, admin API, and metrics | proxy `8080` (client-facing), admin `8090` (UI / API / `/metrics` / `/trino-gateway/livez` / `readyz`) |
| **routing-service** | `routingService.*` | gRPC routing engine that the gateway consults to pick a backend cluster | gRPC `9001` (data-plane), metrics `9091`, admin gRPC `9092` (kill-switch) |

```
                 clients
                    │  :8080 (proxy Service, LB/ClusterIP)
                    ▼
            ┌───────────────┐   routing.external.grpcAddr
            │  trino-goway  │ ─────────────► ┌──────────────────┐
            │   (gateway)   │   :9001 gRPC   │ routing-service  │
            └───────┬───────┘ ◄───────────── └──────────────────┘
        admin :8090 │ (UI/API/metrics/probes)        :9091 metrics
                    ▼
              PostgreSQL  (external by default; bundled subchart for dev)
```

The gateway's `routing.external.grpcAddr` is **auto-wired** to the in-cluster
routing-service (`<release>-routing-service:9001`) when left unset and
`routingService.enabled=true`. Set it explicitly to point at an external router.

## Prerequisites

- Kubernetes **>=1.24.0-0** (the routing-service uses
  the native `grpc:` probe, k8s ≥ 1.24; an exec `grpc_health_probe` fallback is
  available for older clusters).
- Helm 3.8+ (or Helm 4).
- A reachable PostgreSQL for the gateway (or enable the bundled `postgresql`
  subchart for dev/demo — **not** for production).

## Install

```bash
# Resolve the optional bundled-Postgres dependency (no-op unless enabled).
helm dependency build charts/trino-goway

# Dev/demo: bundled Postgres, single replica of each component.
helm install goway charts/trino-goway \
  --namespace trino-goway --create-namespace \
  --set postgresql.enabled=true

# Production: external DB, secrets out of band, scaled + protected.
helm install goway charts/trino-goway \
  --namespace trino-goway --create-namespace \
  --values my-values.yaml
```

> **Bundled Postgres is dev/demo only.** `postgresql.enabled=true` pulls in the
> Bitnami `postgresql` subchart, which the chart points at Bitnami's
> **`bitnamilegacy`** image archive — the frozen community images Bitnami moved
> off their primary Docker Hub repos in their 2025 image migration (the original
> `bitnami/postgresql` tags are no longer published). It is fine for a quick
> local spin-up but is **not** maintained for production. When the gateway's
> `db.host` is left empty, the chart auto-wires the DSN to the bundled
> `<release>-postgresql` Service. For anything beyond dev/demo, set
> `postgresql.enabled=false` (the default) and point `trinoGoway.config.db.*` at
> a managed/external Postgres.

A minimal production `my-values.yaml`:

```yaml
global:
  imageRegistry: ghcr.io/your-org

trinoGoway:
  replicaCount: 3
  # BYO Secret holding the DB password, cookie.secret, and any OIDC/LDAP
  # secrets (keys documented in the secret template). Skips chart-managed Secret.
  existingSecret: trino-goway-secrets
  config:
    db:
      host: postgres.db.svc
      port: 5432
      name: trino_gateway
      user: gateway
      sslmode: require
    auth:
      type: OIDC
  autoscaling:
    enabled: true
    minReplicas: 3
    maxReplicas: 10

routingService:
  replicaCount: 2
```

## Upgrade

```bash
helm upgrade goway charts/trino-goway --values my-values.yaml
```

A `checksum/config` (and `checksum/secret`) pod annotation rolls the
Deployments automatically when the rendered config or secret changes. The
chart uses a `RollingUpdate` strategy for both components.

## Values layout (camelCase)

Values are keyed in **camelCase** so templates use clean dot-access
(`.Values.trinoGoway.config.proxy.port`) with no `index .Values "…"` indirection:

- `global.*` — cross-cutting: `imageRegistry`, `imagePullSecrets`,
  `commonLabels`, `storageClass`, applied to both components.
- `trinoGoway.*` — the gateway: scheduling/scaling knobs plus `config.*`, which
  renders the gateway `config.yaml` (`proxy`, `admin`, `monitor`, `clusterStats`,
  `backendState`, `db`, `routing`, `auth`, `ui`, `metrics`).
- `routingService.*` — the routing-service: scheduling/scaling knobs plus
  `config.*` (`addr`, `metricsAddr`, `adminAddr`, `defaultRoutingGroup`,
  `sqlParsing`, `tracingEndpoint`, and the `methods:` expr/Starlark rule bodies).

Each component block carries the same scaffolding: `enabled`, `replicaCount`,
`image.{registry,repository,tag,pullPolicy}`, `resources`, `nodeSelector`,
`tolerations`, `affinity`, `topologySpreadConstraints`, `podSecurityContext`,
`securityContext`, `serviceAccount.{create,name,annotations}`, `podAnnotations`,
`extraEnv`, `autoscaling.*`, and `pdb.*`.

## Database migrations (R3)

The gateway runs `goose.Up` on startup. `goose` does not take a lock, so N
replicas booting together could otherwise race. The chart resolves this with
`trinoGoway.migrations.strategy`:

| Strategy | Behavior | Use when |
|----------|----------|----------|
| `inline` *(default)* | Each gateway runs migrations on boot; the Go layer wraps them in a PostgreSQL advisory lock (`pg_advisory_lock`) / MySQL `GET_LOCK`, so concurrent replicas serialize and only one applies. | The standard path once the advisory-lock migration is in place. |
| `hook` | A `pre-install`/`pre-upgrade` Helm Job runs migrations once; the gateway Deployment skips them. | You prefer migrations gated to a single Job (e.g. strict change-control). |
| `disabled` | No migrations are run by the chart. | Schema is managed entirely out of band. |

## Secrets (R7)

Non-secret config goes to a ConfigMap; the DB password, `cookie.secret`, OIDC
client secret, and LDAP bind password go to a Secret. Set
`trinoGoway.existingSecret` to point at a BYO Secret (for External Secrets /
Vault) and the chart skips creating its own. `cookie.secret` must be **stable
and shared across all gateway replicas** (it signs the session cookie).

## Production checklist

- [ ] **External database** — `postgresql.enabled=false` (default); point
      `trinoGoway.config.db.*` at managed Postgres with `sslmode: require`.
- [ ] **Secrets out of band** — `trinoGoway.existingSecret` from a sealed/Vault
      source; never commit `cookie.secret` or DB passwords.
- [ ] **Stable `cookie.secret`** shared across replicas (`openssl rand -hex 32`).
- [ ] **Migrations** — confirm the advisory-lock path (`strategy=inline`) or use
      `strategy=hook` (see above) before scaling the gateway past one replica.
- [ ] **Scale + protect** — `autoscaling.enabled=true` (or `replicaCount>1`),
      `pdb` enabled, and pod anti-affinity / `topologySpreadConstraints`.
- [ ] **Probes** — keep the default HTTP probes (gateway) and gRPC probe
      (routing-service); raise `startupProbe` budget for slow DB connects.
- [ ] **NetworkPolicy** — `networkPolicy.enabled=true` to restrict traffic to
      gateway→routing `:9001`, scrape→`:9091`/`:8090`, clients→proxy `:8080`.
- [ ] **Observability** — `trinoGoway.serviceMonitor.enabled=true` /
      `routingService.serviceMonitor.enabled=true` with Prometheus Operator, or
      rely on the pod scrape annotations.
- [ ] **Resources** — set requests/limits for both components; size the gateway
      `terminationGracePeriodSeconds` ≥ the proxy drain deadline (30s).

## Versioning

- **`version`** (chart semver) is bumped on every chart change, independently of
  the application.
- **`appVersion`** tracks the trino-goway / routing-service application release
  the chart targets; keep it in sync when bumping the deployed image tags
  (the image `tag` defaults to `appVersion` when unset).

## Requirements

Kubernetes: `>=1.24.0-0`

| Repository | Name | Version |
|------------|------|---------|
| https://charts.bitnami.com/bitnami | postgresql | >=16.0.0 <17.0.0 |

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| global | object | `{"commonLabels":{},"imagePullSecrets":[],"imageRegistry":"","storageClass":""}` | ------------------------------------------------------------------------- |
| networkPolicy.enabled | bool | `false` |  |
| networkPolicy.metricsFrom | list | `[]` |  |
| networkPolicy.proxyFrom | list | `[]` |  |
| postgresql.architecture | string | `"standalone"` |  |
| postgresql.auth.database | string | `"trino_gateway"` |  |
| postgresql.auth.password | string | `""` |  |
| postgresql.auth.username | string | `"gw"` |  |
| postgresql.enabled | bool | `false` |  |
| postgresql.image.repository | string | `"bitnamilegacy/postgresql"` |  |
| routingService.affinity | object | `{}` |  |
| routingService.autoscaling.enabled | bool | `false` |  |
| routingService.autoscaling.maxReplicas | int | `10` |  |
| routingService.autoscaling.minReplicas | int | `2` |  |
| routingService.autoscaling.targetCPUUtilizationPercentage | int | `80` |  |
| routingService.autoscaling.targetMemoryUtilizationPercentage | int | `0` |  |
| routingService.config.addr | string | `":9001"` |  |
| routingService.config.adminAddr | string | `":9092"` |  |
| routingService.config.defaultRoutingGroup | string | `"default"` |  |
| routingService.config.methods[0].program | string | `"request.source == \"airflow\" ? \"etl\"\n  : request.parse_ok && request.query_category == \"WRITE\" ? \"etl\"\n  : request.parse_ok && \"hive\" in request.catalogs ? \"warehouse\"\n  : \"tier=premium\" in request.client_tags ? \"premium\"\n  : \"\"\n"` |  |
| routingService.config.methods[0].type | string | `"expr"` |  |
| routingService.config.metricsAddr | string | `":9091"` |  |
| routingService.config.sqlParsing.enabled | bool | `true` |  |
| routingService.config.sqlParsing.maxBodyBytes | int | `262144` |  |
| routingService.config.tracingEndpoint | string | `""` |  |
| routingService.enabled | bool | `true` |  |
| routingService.existingSecret | string | `""` |  |
| routingService.extraEnv | list | `[]` |  |
| routingService.image.pullPolicy | string | `"IfNotPresent"` |  |
| routingService.image.registry | string | `"ghcr.io"` |  |
| routingService.image.repository | string | `"hclincode/trino-goway-routing-service"` |  |
| routingService.image.tag | string | `""` |  |
| routingService.imagePullSecrets | list | `[]` |  |
| routingService.metrics.podAnnotations | bool | `true` |  |
| routingService.nodeSelector | object | `{}` |  |
| routingService.podAnnotations | object | `{}` |  |
| routingService.podAntiAffinity | string | `"soft"` |  |
| routingService.podDisruptionBudget.enabled | bool | `true` |  |
| routingService.podDisruptionBudget.maxUnavailable | string | `""` |  |
| routingService.podDisruptionBudget.minAvailable | int | `1` |  |
| routingService.podLabels | object | `{}` |  |
| routingService.podSecurityContext.fsGroup | int | `65532` |  |
| routingService.podSecurityContext.runAsGroup | int | `65532` |  |
| routingService.podSecurityContext.runAsNonRoot | bool | `true` |  |
| routingService.podSecurityContext.runAsUser | int | `65532` |  |
| routingService.podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| routingService.probes.grpcHealthProbePath | string | `"/bin/grpc_health_probe"` |  |
| routingService.probes.useGrpcProbe | bool | `true` |  |
| routingService.replicaCount | int | `2` |  |
| routingService.resources.limits.cpu | string | `"500m"` |  |
| routingService.resources.limits.memory | string | `"256Mi"` |  |
| routingService.resources.requests.cpu | string | `"100m"` |  |
| routingService.resources.requests.memory | string | `"128Mi"` |  |
| routingService.secrets | object | `{}` |  |
| routingService.securityContext.allowPrivilegeEscalation | bool | `false` |  |
| routingService.securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| routingService.securityContext.readOnlyRootFilesystem | bool | `true` |  |
| routingService.securityContext.runAsNonRoot | bool | `true` |  |
| routingService.service.grpc.annotations | object | `{}` |  |
| routingService.service.grpc.headless | bool | `false` |  |
| routingService.service.grpc.port | int | `9001` |  |
| routingService.service.metrics.annotations | object | `{}` |  |
| routingService.service.metrics.port | int | `9091` |  |
| routingService.serviceAccount.annotations | object | `{}` |  |
| routingService.serviceAccount.create | bool | `true` |  |
| routingService.serviceAccount.name | string | `""` |  |
| routingService.serviceMonitor.enabled | bool | `false` |  |
| routingService.serviceMonitor.interval | string | `"30s"` |  |
| routingService.serviceMonitor.labels | object | `{}` |  |
| routingService.serviceMonitor.metricRelabelings | list | `[]` |  |
| routingService.serviceMonitor.relabelings | list | `[]` |  |
| routingService.serviceMonitor.scrapeTimeout | string | `"10s"` |  |
| routingService.terminationGracePeriodSeconds | int | `30` |  |
| routingService.tolerations | list | `[]` |  |
| routingService.topologySpreadConstraints | list | `[]` |  |
| routingService.updateStrategy.rollingUpdate.maxSurge | int | `1` |  |
| routingService.updateStrategy.rollingUpdate.maxUnavailable | int | `0` |  |
| routingService.updateStrategy.type | string | `"RollingUpdate"` |  |
| trinoGoway.affinity | object | `{}` |  |
| trinoGoway.autoscaling.enabled | bool | `false` |  |
| trinoGoway.autoscaling.maxReplicas | int | `10` |  |
| trinoGoway.autoscaling.minReplicas | int | `2` |  |
| trinoGoway.autoscaling.targetCPUUtilizationPercentage | int | `80` |  |
| trinoGoway.autoscaling.targetMemoryUtilizationPercentage | int | `0` |  |
| trinoGoway.config.admin.port | int | `8090` |  |
| trinoGoway.config.auth.authorization.admin | string | `""` |  |
| trinoGoway.config.auth.authorization.api | string | `""` |  |
| trinoGoway.config.auth.authorization.pagePermissions | object | `{}` |  |
| trinoGoway.config.auth.authorization.user | string | `""` |  |
| trinoGoway.config.auth.ldap.bindDn | string | `""` |  |
| trinoGoway.config.auth.ldap.url | string | `""` |  |
| trinoGoway.config.auth.ldap.userAttr | string | `"uid"` |  |
| trinoGoway.config.auth.ldap.userBase | string | `""` |  |
| trinoGoway.config.auth.oidc.authorizationEndpoint | string | `""` |  |
| trinoGoway.config.auth.oidc.clientId | string | `""` |  |
| trinoGoway.config.auth.oidc.issuerUrl | string | `""` |  |
| trinoGoway.config.auth.oidc.jwksTtlSecs | int | `300` |  |
| trinoGoway.config.auth.oidc.jwksUrl | string | `""` |  |
| trinoGoway.config.auth.oidc.redirectUrl | string | `""` |  |
| trinoGoway.config.auth.oidc.scopes[0] | string | `"openid"` |  |
| trinoGoway.config.auth.oidc.scopes[1] | string | `"profile"` |  |
| trinoGoway.config.auth.oidc.scopes[2] | string | `"email"` |  |
| trinoGoway.config.auth.oidc.tokenEndpoint | string | `""` |  |
| trinoGoway.config.auth.type | string | `"NOOP"` |  |
| trinoGoway.config.backendState.ssl | bool | `false` |  |
| trinoGoway.config.backendState.username | string | `""` |  |
| trinoGoway.config.backendState.xForwardedProtoHeader | bool | `false` |  |
| trinoGoway.config.clusterStats.monitorType | string | `"INFO_API"` |  |
| trinoGoway.config.cookie.ttl | string | `"10m"` |  |
| trinoGoway.config.cookie.wireCompat | bool | `true` |  |
| trinoGoway.config.db.driver | string | `"postgres"` |  |
| trinoGoway.config.db.dsn | string | `""` |  |
| trinoGoway.config.db.host | string | `""` |  |
| trinoGoway.config.db.name | string | `"trino_gateway"` |  |
| trinoGoway.config.db.port | int | `5432` |  |
| trinoGoway.config.db.sslmode | string | `"disable"` |  |
| trinoGoway.config.db.user | string | `"gw"` |  |
| trinoGoway.config.metrics.enabled | bool | `true` |  |
| trinoGoway.config.metrics.path | string | `"/metrics"` |  |
| trinoGoway.config.monitor.checkTimeout | string | `"5s"` |  |
| trinoGoway.config.monitor.interval | string | `"30s"` |  |
| trinoGoway.config.monitor.metricMaximumValues | object | `{}` |  |
| trinoGoway.config.monitor.metricMinimumValues.trino_metadata_name_DiscoveryNodeManager_ActiveNodeCount | int | `1` |  |
| trinoGoway.config.monitor.metricsEndpoint | string | `"/metrics"` |  |
| trinoGoway.config.monitor.queuedQueriesMetricName | string | `"trino_execution_name_QueryManager_QueuedQueries"` |  |
| trinoGoway.config.monitor.refreshInterval | string | `"15s"` |  |
| trinoGoway.config.monitor.retries | int | `0` |  |
| trinoGoway.config.monitor.runningQueriesMetricName | string | `"trino_execution_name_QueryManager_RunningQueries"` |  |
| trinoGoway.config.monitor.statsTimeout | string | `"10s"` |  |
| trinoGoway.config.proxy.port | int | `8080` |  |
| trinoGoway.config.proxy.propagateErrors | bool | `false` |  |
| trinoGoway.config.proxy.requestTimeout | string | `"30s"` |  |
| trinoGoway.config.proxy.responseSize | string | `"1MiB"` |  |
| trinoGoway.config.routing.defaultGroup | string | `"default"` |  |
| trinoGoway.config.routing.external.excludeHeaders[0] | string | `"Authorization"` |  |
| trinoGoway.config.routing.external.excludeHeaders[1] | string | `"X-Trino-Client-Secret"` |  |
| trinoGoway.config.routing.external.grpcAddr | string | `""` |  |
| trinoGoway.config.routing.external.timeout | string | `"1s"` |  |
| trinoGoway.config.routing.external.url | string | `""` |  |
| trinoGoway.config.routing.type | string | `"EXTERNAL"` |  |
| trinoGoway.config.ui.disablePages | list | `[]` |  |
| trinoGoway.enabled | bool | `true` |  |
| trinoGoway.existingSecret | string | `""` |  |
| trinoGoway.extraEnv | list | `[]` |  |
| trinoGoway.image.pullPolicy | string | `"IfNotPresent"` |  |
| trinoGoway.image.registry | string | `"ghcr.io"` |  |
| trinoGoway.image.repository | string | `"hclincode/trino-goway"` |  |
| trinoGoway.image.tag | string | `""` |  |
| trinoGoway.imagePullSecrets | list | `[]` |  |
| trinoGoway.ingress.annotations | object | `{}` |  |
| trinoGoway.ingress.className | string | `""` |  |
| trinoGoway.ingress.enabled | bool | `false` |  |
| trinoGoway.ingress.hosts[0].host | string | `"trino-goway.example.com"` |  |
| trinoGoway.ingress.hosts[0].paths[0].backend | string | `"proxy"` |  |
| trinoGoway.ingress.hosts[0].paths[0].path | string | `"/"` |  |
| trinoGoway.ingress.hosts[0].paths[0].pathType | string | `"Prefix"` |  |
| trinoGoway.ingress.tls | list | `[]` |  |
| trinoGoway.metrics.podAnnotations | bool | `true` |  |
| trinoGoway.migrations.backoffLimit | int | `3` |  |
| trinoGoway.migrations.podAnnotations | object | `{}` |  |
| trinoGoway.migrations.resources | object | `{}` |  |
| trinoGoway.migrations.strategy | string | `"inline"` |  |
| trinoGoway.nodeSelector | object | `{}` |  |
| trinoGoway.podAnnotations | object | `{}` |  |
| trinoGoway.podAntiAffinity | string | `"soft"` |  |
| trinoGoway.podDisruptionBudget.enabled | bool | `true` |  |
| trinoGoway.podDisruptionBudget.maxUnavailable | string | `""` |  |
| trinoGoway.podDisruptionBudget.minAvailable | int | `1` |  |
| trinoGoway.podLabels | object | `{}` |  |
| trinoGoway.podSecurityContext.fsGroup | int | `65532` |  |
| trinoGoway.podSecurityContext.runAsGroup | int | `65532` |  |
| trinoGoway.podSecurityContext.runAsNonRoot | bool | `true` |  |
| trinoGoway.podSecurityContext.runAsUser | int | `65532` |  |
| trinoGoway.podSecurityContext.seccompProfile.type | string | `"RuntimeDefault"` |  |
| trinoGoway.replicaCount | int | `2` |  |
| trinoGoway.resources.limits.cpu | string | `"1"` |  |
| trinoGoway.resources.limits.memory | string | `"512Mi"` |  |
| trinoGoway.resources.requests.cpu | string | `"250m"` |  |
| trinoGoway.resources.requests.memory | string | `"256Mi"` |  |
| trinoGoway.secrets.backendStatePassword | string | `""` |  |
| trinoGoway.secrets.cookieSecret | string | `""` |  |
| trinoGoway.secrets.dbPassword | string | `""` |  |
| trinoGoway.secrets.ldapBindPassword | string | `""` |  |
| trinoGoway.secrets.oidcClientSecret | string | `""` |  |
| trinoGoway.securityContext.allowPrivilegeEscalation | bool | `false` |  |
| trinoGoway.securityContext.capabilities.drop[0] | string | `"ALL"` |  |
| trinoGoway.securityContext.readOnlyRootFilesystem | bool | `true` |  |
| trinoGoway.securityContext.runAsNonRoot | bool | `true` |  |
| trinoGoway.service.admin.annotations | object | `{}` |  |
| trinoGoway.service.admin.port | int | `8090` |  |
| trinoGoway.service.admin.type | string | `"ClusterIP"` |  |
| trinoGoway.service.proxy.annotations | object | `{}` |  |
| trinoGoway.service.proxy.externalTrafficPolicy | string | `""` |  |
| trinoGoway.service.proxy.loadBalancerSourceRanges | list | `[]` |  |
| trinoGoway.service.proxy.nodePort | string | `""` |  |
| trinoGoway.service.proxy.port | int | `8080` |  |
| trinoGoway.service.proxy.type | string | `"ClusterIP"` |  |
| trinoGoway.serviceAccount.annotations | object | `{}` |  |
| trinoGoway.serviceAccount.create | bool | `true` |  |
| trinoGoway.serviceAccount.name | string | `""` |  |
| trinoGoway.serviceMonitor.enabled | bool | `false` |  |
| trinoGoway.serviceMonitor.interval | string | `"30s"` |  |
| trinoGoway.serviceMonitor.labels | object | `{}` |  |
| trinoGoway.serviceMonitor.metricRelabelings | list | `[]` |  |
| trinoGoway.serviceMonitor.relabelings | list | `[]` |  |
| trinoGoway.serviceMonitor.scrapeTimeout | string | `"10s"` |  |
| trinoGoway.terminationGracePeriodSeconds | int | `40` |  |
| trinoGoway.tolerations | list | `[]` |  |
| trinoGoway.topologySpreadConstraints | list | `[]` |  |
| trinoGoway.updateStrategy.rollingUpdate.maxSurge | int | `1` |  |
| trinoGoway.updateStrategy.rollingUpdate.maxUnavailable | int | `0` |  |
| trinoGoway.updateStrategy.type | string | `"RollingUpdate"` |  |

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| trino-goway maintainers |  | <https://github.com/hclincode/trino-goway> |

## Source Code

* <https://github.com/hclincode/trino-goway>
* <https://github.com/hclincode/trino-goway/tree/main/routing-service>
