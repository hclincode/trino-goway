{{/*
================================================================================
Common helpers
================================================================================
*/}}

{{/* Base chart name (overridable via nameOverride). */}}
{{- define "trino-goway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Release fullname. Honors fullnameOverride; otherwise <release>-<chart> (deduped
when the release name already contains the chart name).
*/}}
{{- define "trino-goway.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/* chart label "name-version". */}}
{{- define "trino-goway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Component fullnames. */}}
{{- define "trino-goway.gateway.fullname" -}}
{{- printf "%s-gateway" (include "trino-goway.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}
{{- define "trino-goway.routingService.fullname" -}}
{{- printf "%s-routing-service" (include "trino-goway.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Common labels shared by all objects. Merges global.commonLabels.
*/}}
{{- define "trino-goway.commonLabels" -}}
helm.sh/chart: {{ include "trino-goway.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: trino-goway
{{- with .Values.global.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Per-component label/selector sets.
Usage: include "trino-goway.gateway.labels" . / "...selectorLabels" .
*/}}
{{- define "trino-goway.gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "trino-goway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: gateway
{{- end }}
{{- define "trino-goway.gateway.labels" -}}
{{ include "trino-goway.commonLabels" . }}
{{ include "trino-goway.gateway.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{- define "trino-goway.routingService.selectorLabels" -}}
app.kubernetes.io/name: {{ include "trino-goway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: routing-service
{{- end }}
{{- define "trino-goway.routingService.labels" -}}
{{ include "trino-goway.commonLabels" . }}
{{ include "trino-goway.routingService.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{/*
Image reference builder. Arg: dict "image" <imageBlock> "global" .Values.global
"chart" .Chart. Honors global.imageRegistry override and AppVersion tag default.
*/}}
{{- define "trino-goway.image" -}}
{{- $registry := .image.registry -}}
{{- if .global.imageRegistry -}}
{{- $registry = .global.imageRegistry -}}
{{- end -}}
{{- $tag := .image.tag | default .chart.AppVersion -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry .image.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" .image.repository $tag -}}
{{- end -}}
{{- end }}

{{/* Merged image pull secrets (global + component). */}}
{{- define "trino-goway.imagePullSecrets" -}}
{{- $secrets := concat (.global.imagePullSecrets | default list) (.component.imagePullSecrets | default list) -}}
{{- if $secrets }}
imagePullSecrets:
{{- range ($secrets | uniq) }}
  - name: {{ . }}
{{- end }}
{{- end }}
{{- end }}

{{/* ServiceAccount names. */}}
{{- define "trino-goway.gateway.serviceAccountName" -}}
{{- if .Values.trinoGoway.serviceAccount.create -}}
{{- default (include "trino-goway.gateway.fullname" .) .Values.trinoGoway.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.trinoGoway.serviceAccount.name -}}
{{- end -}}
{{- end }}
{{- define "trino-goway.routingService.serviceAccountName" -}}
{{- if .Values.routingService.serviceAccount.create -}}
{{- default (include "trino-goway.routingService.fullname" .) .Values.routingService.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.routingService.serviceAccount.name -}}
{{- end -}}
{{- end }}

{{/*
routing-service in-cluster gRPC address used to auto-wire the gateway's
routing.external.grpcAddr when it is left empty (R4).
*/}}
{{- define "trino-goway.routingService.grpcAddr" -}}
{{- printf "%s:%d" (include "trino-goway.routingService.fullname" .) (int .Values.routingService.service.grpc.port) -}}
{{- end }}

{{/*
Resolve the gateway's effective routing.external.grpcAddr:
explicit value wins; else auto-wire to the in-cluster routing-service when it is
enabled; else empty (gateway routes everything to defaultGroup).
*/}}
{{- define "trino-goway.gateway.routingGrpcAddr" -}}
{{- $ext := .Values.trinoGoway.config.routing.external -}}
{{- if $ext.grpcAddr -}}
{{- $ext.grpcAddr -}}
{{- else if and .Values.routingService.enabled (not $ext.url) -}}
{{- include "trino-goway.routingService.grpcAddr" . -}}
{{- end -}}
{{- end }}

{{/*
Effective replica count for the gateway: omitted by callers when autoscaling is
enabled. Returns the number to set on the Deployment.
*/}}
{{- define "trino-goway.gateway.replicas" -}}
{{- .Values.trinoGoway.replicaCount -}}
{{- end }}

{{/*
Default soft/hard pod anti-affinity for a component. Arg: dict "selectorLabels"
<labels> "mode" <soft|hard|"">. Empty mode renders nothing.
*/}}
{{- define "trino-goway.podAntiAffinity" -}}
{{- if eq .mode "soft" -}}
podAntiAffinity:
  preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      podAffinityTerm:
        topologyKey: kubernetes.io/hostname
        labelSelector:
          matchLabels:
{{ .selectorLabels | indent 12 }}
{{- else if eq .mode "hard" -}}
podAntiAffinity:
  requiredDuringSchedulingIgnoredDuringExecution:
    - topologyKey: kubernetes.io/hostname
      labelSelector:
        matchLabels:
{{ .selectorLabels | indent 10 }}
{{- end -}}
{{- end }}

{{/*
Gateway database DSN (R8). Explicit config.db.dsn always wins. Otherwise the DSN
is assembled from the structured db fields, per driver:
  - postgres: a password-less libpq string ("host=... user=..."); the password is
    injected at runtime via the PGPASSWORD env (lib/pq), so it never appears here.
  - mysql (also MariaDB, which is MySQL wire-compatible): a go-sql-driver DSN
    "user:password@tcp(host:port)/db?parseTime=true". That driver reads the
    password ONLY from the DSN (no PGPASSWORD equivalent), so it is embedded —
    but ONLY when withSecrets=true (the Secret-mounted config); the ConfigMap
    audit view renders it blank.
Bundled-DB host auto-derivation: postgres -> the postgresql subchart Service,
mysql -> the mariadb subchart Service (with the port defaulted to 3306 when it is
left at the postgres default). An explicit db.host / db.dsn always wins.
Arg: dict "ctx" $ "withSecrets" <bool>.
*/}}
{{- define "trino-goway.gateway.dsn" -}}
{{- $ctx := .ctx -}}
{{- $with := .withSecrets -}}
{{- $db := $ctx.Values.trinoGoway.config.db -}}
{{- if $db.dsn -}}
{{- $db.dsn -}}
{{- else if eq $db.driver "mysql" -}}
{{- $host := $db.host -}}
{{- $port := int $db.port -}}
{{- if and (not $host) ($ctx.Values.mariadb | default dict).enabled -}}
{{- $host = include "trino-goway.mariadb.primaryHost" $ctx -}}
{{- if eq $port 5432 -}}{{- $port = 3306 -}}{{- end -}}
{{- end -}}
{{- $pw := "" -}}
{{- if $with -}}{{- $pw = include "trino-goway.gateway.dbPassword" $ctx -}}{{- end -}}
{{- printf "%s:%s@tcp(%s:%d)/%s?parseTime=true" $db.user $pw $host $port $db.name -}}
{{- else -}}
{{- $host := $db.host -}}
{{- if and (not $host) $ctx.Values.postgresql.enabled -}}
{{- $host = include "trino-goway.postgresql.primaryHost" $ctx -}}
{{- end -}}
{{- printf "host=%s port=%d dbname=%s user=%s sslmode=%s" $host (int $db.port) $db.name $db.user $db.sslmode -}}
{{- end -}}
{{- end }}

{{/*
Hostname of the bundled Bitnami postgresql primary Service, mirroring the
subchart's own naming (common.names.fullname) so the auto-derived DSN host
resolves: default <release>-postgresql, honoring postgresql.fullnameOverride /
postgresql.nameOverride, with a "-primary" suffix under the replication
architecture. Only used when trinoGoway.config.db.host is unset and
postgresql.enabled (an explicit db.host / db.dsn always wins).
*/}}
{{- define "trino-goway.postgresql.fullname" -}}
{{- $pg := .Values.postgresql | default dict -}}
{{- if $pg.fullnameOverride -}}
{{- $pg.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "postgresql" $pg.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end }}
{{- define "trino-goway.postgresql.primaryHost" -}}
{{- $pg := .Values.postgresql | default dict -}}
{{- $fullname := include "trino-goway.postgresql.fullname" . -}}
{{- if eq ($pg.architecture | default "standalone") "replication" -}}
{{- printf "%s-primary" $fullname | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $fullname -}}
{{- end -}}
{{- end }}

{{/*
Hostname of the bundled Bitnami mariadb primary Service, mirroring the subchart's
common.names.fullname so the auto-derived mysql DSN host resolves: default
<release>-mariadb, honoring mariadb.fullnameOverride / mariadb.nameOverride, with
a "-primary" suffix under the replication architecture. Only used when
trinoGoway.config.db.host is unset, db.driver=mysql, and mariadb.enabled (an
explicit db.host / db.dsn always wins).
*/}}
{{- define "trino-goway.mariadb.fullname" -}}
{{- $m := .Values.mariadb | default dict -}}
{{- if $m.fullnameOverride -}}
{{- $m.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "mariadb" $m.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end }}
{{- define "trino-goway.mariadb.primaryHost" -}}
{{- $m := .Values.mariadb | default dict -}}
{{- $fullname := include "trino-goway.mariadb.fullname" . -}}
{{- if eq ($m.architecture | default "standalone") "replication" -}}
{{- printf "%s-primary" $fullname | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $fullname -}}
{{- end -}}
{{- end }}

{{/*
Whether the gateway should run migrations on boot (db.autoMigrate), derived from
migrations.strategy: inline=true, hook/disabled=false.
*/}}
{{- define "trino-goway.gateway.autoMigrate" -}}
{{- if eq .Values.trinoGoway.migrations.strategy "inline" -}}
true
{{- else -}}
false
{{- end -}}
{{- end }}

{{/* Name of the gateway Secret (created or BYO existingSecret). */}}
{{- define "trino-goway.gateway.secretName" -}}
{{- default (printf "%s-secret" (include "trino-goway.gateway.fullname" .)) .Values.trinoGoway.existingSecret -}}
{{- end }}

{{/*
Key name for the discrete DB password within the gateway Secret. Configurable via
trinoGoway.secretKeys.dbPassword so a BYO existingSecret (external-secrets/Vault,
or a Bitnami subchart Secret) can expose the password under its own key. Default
"db-password". Used for both the chart-managed Secret's key and the PGPASSWORD
secretKeyRef (postgres driver).
*/}}
{{- define "trino-goway.gateway.dbPasswordKey" -}}
{{- (.Values.trinoGoway.secretKeys | default dict).dbPassword | default "db-password" -}}
{{- end }}

{{/*
Resolved DB password: explicit trinoGoway.secrets.dbPassword wins; otherwise it
falls back to the bundled subchart's auth password (postgresql or mariadb,
whichever is enabled). Empty string when none is set. Consumed by the gateway
Secret / migrate-job (the db-password key + PGPASSWORD for postgres) and by the
mysql DSN builder (where the password is embedded in the DSN).
*/}}
{{- define "trino-goway.gateway.dbPassword" -}}
{{- $pw := .Values.trinoGoway.secrets.dbPassword -}}
{{- if and (not $pw) .Values.postgresql.enabled -}}
{{- $pw = .Values.postgresql.auth.password -}}
{{- else if and (not $pw) (.Values.mariadb | default dict).enabled -}}
{{- $pw = .Values.mariadb.auth.password -}}
{{- end -}}
{{- $pw -}}
{{- end }}

{{/*
================================================================================
Gateway config.yaml body
--------------------------------------------------------------------------------
Renders the full gateway config.yaml from .Values.trinoGoway.config. The gateway
binary reads ONE YAML file (no env expansion), so the complete rendering — incl.
secret fields — must be mounted from a Secret. The ConfigMap mounts the same body
with secret fields blanked (audit/diff view only).

Arg: dict "ctx" $ "withSecrets" <bool>.
  withSecrets=false → cookie.secret / oidc.clientSecret / ldap.bindPassword /
                      backendState.password render empty (ConfigMap-safe).
The DB password is NEVER rendered here — db.dsn is password-less and the password
is injected at runtime via PGPASSWORD (lib/pq) from the Secret.
================================================================================
*/}}
{{- define "trino-goway.gateway.configYaml" -}}
{{- $ctx := .ctx -}}
{{- $with := .withSecrets -}}
{{- $c := $ctx.Values.trinoGoway.config -}}
{{- $secrets := $ctx.Values.trinoGoway.secrets -}}
proxy:
  port: {{ $c.proxy.port }}
  responseSize: {{ $c.proxy.responseSize | quote }}
  requestTimeout: {{ $c.proxy.requestTimeout | quote }}
  propagateErrors: {{ $c.proxy.propagateErrors }}
admin:
  port: {{ $c.admin.port }}
monitor:
  interval: {{ $c.monitor.interval | quote }}
  checkTimeout: {{ $c.monitor.checkTimeout | quote }}
  refreshInterval: {{ $c.monitor.refreshInterval | quote }}
  statsTimeout: {{ $c.monitor.statsTimeout | quote }}
  retries: {{ $c.monitor.retries }}
  metricsEndpoint: {{ $c.monitor.metricsEndpoint | quote }}
  runningQueriesMetricName: {{ $c.monitor.runningQueriesMetricName | quote }}
  queuedQueriesMetricName: {{ $c.monitor.queuedQueriesMetricName | quote }}
  metricMinimumValues:
{{- if $c.monitor.metricMinimumValues }}
{{ toYaml $c.monitor.metricMinimumValues | indent 4 }}
{{- else }} {}
{{- end }}
  metricMaximumValues:
{{- if $c.monitor.metricMaximumValues }}
{{ toYaml $c.monitor.metricMaximumValues | indent 4 }}
{{- else }} {}
{{- end }}
clusterStats:
  monitorType: {{ $c.clusterStats.monitorType | quote }}
backendState:
  username: {{ $c.backendState.username | quote }}
  password: {{ if $with }}{{ $secrets.backendStatePassword | quote }}{{ else }}""{{ end }}
  ssl: {{ $c.backendState.ssl }}
  xForwardedProtoHeader: {{ $c.backendState.xForwardedProtoHeader }}
db:
  driver: {{ $c.db.driver | quote }}
  dsn: {{ include "trino-goway.gateway.dsn" (dict "ctx" $ctx "withSecrets" $with) | quote }}
  autoMigrate: {{ include "trino-goway.gateway.autoMigrate" $ctx }}
routing:
  defaultGroup: {{ $c.routing.defaultGroup | quote }}
  type: {{ $c.routing.type | quote }}
  external:
{{- $grpcAddr := include "trino-goway.gateway.routingGrpcAddr" $ctx }}
{{- if $c.routing.external.url }}
    url: {{ $c.routing.external.url | quote }}
{{- else if $grpcAddr }}
    grpcAddr: {{ $grpcAddr | quote }}
{{- end }}
    timeout: {{ $c.routing.external.timeout | quote }}
    excludeHeaders:
{{- range $c.routing.external.excludeHeaders }}
      - {{ . | quote }}
{{- end }}
auth:
  type: {{ $c.auth.type | quote }}
{{- if eq $c.auth.type "OIDC" }}
  oidc:
    issuerUrl: {{ $c.auth.oidc.issuerUrl | quote }}
    clientId: {{ $c.auth.oidc.clientId | quote }}
    clientSecret: {{ if $with }}{{ $secrets.oidcClientSecret | quote }}{{ else }}""{{ end }}
    jwksUrl: {{ $c.auth.oidc.jwksUrl | quote }}
    jwksTtlSecs: {{ $c.auth.oidc.jwksTtlSecs }}
    scopes:
{{- range $c.auth.oidc.scopes }}
      - {{ . | quote }}
{{- end }}
    redirectUrl: {{ $c.auth.oidc.redirectUrl | quote }}
{{- with $c.auth.oidc.authorizationEndpoint }}
    authorizationEndpoint: {{ . | quote }}
{{- end }}
{{- with $c.auth.oidc.tokenEndpoint }}
    tokenEndpoint: {{ . | quote }}
{{- end }}
{{- else if eq $c.auth.type "LDAP" }}
  ldap:
    url: {{ $c.auth.ldap.url | quote }}
    bindDn: {{ $c.auth.ldap.bindDn | quote }}
    bindPassword: {{ if $with }}{{ $secrets.ldapBindPassword | quote }}{{ else }}""{{ end }}
    userBase: {{ $c.auth.ldap.userBase | quote }}
    userAttr: {{ $c.auth.ldap.userAttr | quote }}
{{- end }}
  authorization:
    admin: {{ $c.auth.authorization.admin | quote }}
    user: {{ $c.auth.authorization.user | quote }}
    api: {{ $c.auth.authorization.api | quote }}
{{- with $c.auth.authorization.pagePermissions }}
    pagePermissions:
{{ toYaml . | indent 6 }}
{{- end }}
ui:
  disablePages:
{{- range $c.ui.disablePages }}
    - {{ . | quote }}
{{- end }}
cookie:
  secret: {{ if $with }}{{ $secrets.cookieSecret | quote }}{{ else }}""{{ end }}
  ttl: {{ $c.cookie.ttl | quote }}
  wireCompat: {{ $c.cookie.wireCompat }}
metrics:
  enabled: {{ $c.metrics.enabled }}
  path: {{ $c.metrics.path | quote }}
{{- end }}

{{/*
================================================================================
routing-service config.yaml body
--------------------------------------------------------------------------------
Renders the routing-service config.yaml from .Values.routingService.config. No
secrets in Phase 1 (insecure gRPC), so this is mounted from a ConfigMap. The
`methods:` expr/Starlark rule bodies are rendered verbatim (hot-reloaded by the
service via fsnotify). Arg: the root context ($).
================================================================================
*/}}
{{- define "trino-goway.routingService.configYaml" -}}
{{- $c := .Values.routingService.config -}}
addr: {{ $c.addr | quote }}
metricsAddr: {{ $c.metricsAddr | quote }}
adminAddr: {{ $c.adminAddr | quote }}
tracingEndpoint: {{ $c.tracingEndpoint | quote }}
defaultRoutingGroup: {{ $c.defaultRoutingGroup | quote }}
sqlParsing:
  enabled: {{ $c.sqlParsing.enabled }}
  maxBodyBytes: {{ $c.sqlParsing.maxBodyBytes }}
methods:
{{- if $c.methods }}
{{ toYaml $c.methods }}
{{- else }} []
{{- end }}
{{- end }}
