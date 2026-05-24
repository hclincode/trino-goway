---
title: Config coupling depth — how tightly YAML binds to Java types
author: architect
role: Architect / Tech Lead
component: trino-gateway
topics:
  - config
  - cross-cutting
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 93e020bf9df756cae935c395c23f67dd9432a527
  trino-gateway: 334ba1226c3073af1eb4d0000fbd2a17f80088a9
related-to:
  - trino-gateway/architecture-overview.architect.md
  - trino-gateway/jvm-idioms-not-to-port.md
---

# Config coupling depth — how tightly YAML binds to Java types

## Summary

The gateway loads exactly one YAML file at startup and deserializes it into `HaGatewayConfiguration` and its ~20 sub-config POJOs. The coupling between YAML and Java is shallow for most fields (plain Jackson getter/setter binding) but has three sharp edges that need conscious design choices in the Go port: (1) `modules:` and `managedApps:` are lists of Java fully-qualified class names reflectively loaded at boot; (2) the `serverConfig:` map is passed straight to Airlift's `Bootstrap.setRequiredConfigurationProperties` as untyped key/value, and Airlift property names are not natural Go config field names; (3) two cookie-config classes use a singleton-bootstrap dance via `*PropertiesProvider` because they need to be visible to classes Guice doesn't construct. Everything else is straightforward to mirror in a typed Go struct tree.

## Key Findings

- **Single root, ~20 sub-configs.** `HaGatewayConfiguration` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/config/HaGatewayConfiguration.java:26-299`) is the YAML root. Sub-configs live in `io.trino.gateway.ha.config.*` and include: `DataStoreConfiguration`, `MonitorConfiguration`, `RoutingConfiguration`, `RoutingRulesConfiguration`, `AuthenticationConfiguration` (with nested `OAuthConfiguration`, `FormAuthConfiguration`, `LdapConfiguration`, `OidcConfiguration`), `AuthorizationConfiguration`, `BackendStateConfiguration`, `ClusterStatsConfiguration`, `RequestAnalyzerConfig`, `UIConfiguration`, `ProxyResponseConfiguration`, `DatabaseCacheConfiguration`, `GatewayCookieConfiguration`, `OAuth2GatewayCookieConfiguration`. Each is a plain POJO with field+getter+setter.
- **YAML root is a single `HaGatewayConfiguration` instance.** Loaded by a one-line Jackson call (`HaGatewayLauncher.java:115`):
  ```
  objectMapper.readValue(replaceEnvironmentVariables(config), HaGatewayConfiguration.class);
  ```
- **Env-var substitution is preprocessor-style.** Before YAML parsing, the file text is run through `ConfigurationUtils.replaceEnvironmentVariables` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/util/ConfigurationUtils.java:24-44`). Pattern: `${ENV:NAME}` is replaced with `System.getenv("NAME")`; missing env var → hard fail. Only env vars; no file substitution, no command substitution, no defaulting syntax.
- **`serverConfig:` is a `Map<String, String>` of Airlift properties.** Sample (`trino-gateway/gateway-ha/config.yaml:1-6`):
  ```
  serverConfig:
    node.environment: test
    http-server.http.port: 8080
    tracing.enabled: true
    otel.exporter.endpoint: http://localhost:4318/v1/traces
  ```
  These are not typed in `HaGatewayConfiguration` — they're passed verbatim to Airlift's `Bootstrap.setRequiredConfigurationProperties(...)` (`HaGatewayLauncher.java:73`). Airlift internally has its own typed config classes that consume keys like `http-server.http.port`, but the gateway code never sees them.
- **`modules:` and `managedApps:` are reflective class-name lists.** `HaGatewayConfiguration` has `private List<String> modules` and `private List<String> managedApps` (`HaGatewayConfiguration.java:50-53`). At boot, `BaseApp.addModules` loops over `modules`, does `Class.forName(clazz).getConstructors()[0].newInstance(configuration)`, and binds the result as a Guice `Module` (`BaseApp.java:69-106`). `managedApps` are similarly classloaded and bound (`BaseApp.java:145-166`). Both are the official extension hook: deployers drop their own JAR on the classpath and add the FQCN to the YAML.
- **Cookie configs use a singleton-bootstrap dance.** `GatewayCookieConfigurationPropertiesProvider` (`trino-gateway/gateway-ha/src/main/java/io/trino/gateway/ha/config/GatewayCookieConfigurationPropertiesProvider.java`) is a singleton initialized at the *start* of `HaGatewayProviderModule`'s constructor (`HaGatewayProviderModule.java:105-109`). This is so classes like `GatewayCookie` that are *not* Guice-managed (they're plain data objects deserialized from cookie bodies) can still get the cookie-signing key. Same trick for `OAuth2GatewayCookieConfigurationPropertiesProvider`.
- **Statement paths have validation on set.** `setAdditionalStatementPaths` validates that no path is a prefix of another and that paths are absolute (`HaGatewayConfiguration.java:242-289`). On violation, throws `HaGatewayConfigurationException`. This is the only setter with non-trivial logic.
- **Switch-on-enum fields drive module wiring.** Several config enums drive runtime-implementation selection: `ClusterStatsMonitorType` (INFO_API / UI_API / JDBC / JMX / METRICS / NOOP) → `HaGatewayProviderModule.getClusterStatsMonitor` `switch` (`HaGatewayProviderModule.java:189-197`); `RulesType` (FILE / EXTERNAL) → `getRoutingGroupSelector` `switch` (`HaGatewayProviderModule.java:159-168`). Add a new monitor type → add a new config enum value AND a new switch arm.

## Behavior vs. Implementation Artifact

### Env-var substitution is `${ENV:NAME}` only
- **Observed behavior:** `${ENV:NAME}` is the only substitution syntax. No `${NAME}`, no `${NAME:-default}`, no `${file:path}`. Unknown env var hard-fails the launcher (`ConfigurationUtils.java:38`).
- **Source of behavior:** `gateway-design-intent`. Minimal, explicit, fails loud.
- **Go obligation:** `replicate-exactly`. Use the same syntax in the Go port; same hard-fail on missing env var. Operators may have YAML files committed to source control that depend on this syntax — preserve it for compatibility.

### `modules:` and `managedApps:` reflective loading
- **Observed behavior:** YAML lists of FQCN strings are reflectively `Class.forName`-ed at boot and bound into the Guice container (`BaseApp.java:69-106, 145-166`). Constructor must be `(HaGatewayConfiguration)`. Default modules are disallowed (a guard catches and exits — `BaseApp.java:108-119`).
- **Source of behavior:** `gateway-design-intent`. The advertised extension mechanism: third parties can compile a JAR with their own Guice module / managed app and add it via YAML.
- **Go obligation:** `drop` (no clean Go equivalent). Document a different extension mechanism for the Go port. Options:
  - **Plugin system via gRPC sidecars.** Heavy but real and decoupled.
  - **Build-time inclusion.** Operators who need extensions fork the build and add their code; document a "stable extension interface" they implement.
  - **Configuration-driven HTTP webhooks.** For things like routing or auth, this is already supported via `byRoutingExternal`. Generalize.
  - Most likely answer: take the build-time approach for v1 and document it.
- **Notes:** This is a behavioural break with the Java gateway. Call it out in the migration guide. Operators with custom modules need an explicit migration path before they can adopt the Go port.

### `serverConfig:` is opaque Airlift properties
- **Observed behavior:** The `serverConfig` map is passed straight to Airlift; the gateway code never inspects it (`HaGatewayLauncher.java:73`). Operators tune things like JVM TLS, HTTP/2, threads, JMX endpoints via these property keys.
- **Source of behavior:** `jvm-artifact`. Airlift's bootstrap convention.
- **Go obligation:** `replicate-intent` with a *typed translation table*. In Go, server tuning becomes typed config fields (e.g. `httpServer.port`, `httpServer.readTimeout`). Provide a migration table: `http-server.http.port` (Airlift) → `httpServer.port` (Go). Some Airlift properties (e.g. JMX-related) will have no Go equivalent and are dropped.

### `databaseCache:` — Caffeine config exposed as YAML
- **Observed behavior:** `DatabaseCacheConfiguration` exposes Caffeine cache size/TTL knobs in YAML. These knobs map to in-memory caches in the Java impl.
- **Source of behavior:** `gateway-design-intent` (ops knob).
- **Go obligation:** `replicate-intent`. Cache eviction policy specifics (Caffeine W-TinyLFU vs `ristretto` TinyLFU vs `lru/expirable` plain LRU) are not behaviourally identical. Pick the closest match; document operator-visible differences.

## Implications for Go Rewrite

- **Library:** `gopkg.in/yaml.v3` is the cleanest pick — supports YAML 1.2, has decent error messages with line numbers, and works with struct tags identical to encoding/json conventions. Avoid `spf13/viper` for the root config: it imposes opinions (auto-watch, env-magic) we don't need and adds dependency weight.
- **Interface:**
  - Single `type Config struct { ... }` at the package root, mirroring `HaGatewayConfiguration`.
  - Sub-configs as nested struct types in the same package.
  - Validation as a method `Config.Validate() error` that runs after unmarshalling — replacing JSR-380 annotations and the per-setter validation. Validation is centralized rather than scattered across setters.
  - Env-var substitution as a `preprocessYAML([]byte) []byte` function called before `yaml.Unmarshal`. Mirror the `${ENV:NAME}` syntax and the hard-fail semantics.
  - Enum-driven dispatch (`RulesType`, `ClusterStatsMonitorType`) becomes typed enums in Go with a `String() string` method and a `switch` in the composition root — exactly mirroring Java but without Guice.
- **Concurrency:**
  - The config is loaded once at startup, never mutated. No locking.
  - File-watch / hot-reload is NOT a feature of the Java config root (only `routingRules.rulesConfigPath` is hot-reloaded by `Suppliers.memoizeWithExpiration` in `FileBasedRoutingGroupSelector.java:55`). Do not add hot-reload to the root config in v1.

## Test Strategy Hooks

- See paired QA studies: [[test-infrastructure-inventory]].
- Config-specific test concerns:
  - **Golden config files:** maintain a corpus of `(yaml input, expected Config{})` cases. Include the canonical example (`trino-gateway/gateway-ha/config.yaml`) and the test-fixture YAML files under `trino-gateway/gateway-ha/src/test/resources/`. Both Go config loader and any Go-side validation must produce the same accept/reject decisions as the Java loader on these inputs.
  - **Env-var substitution:** golden cases for unset env var (must fail), set env var (must substitute), and edge syntax (`${env:NAME}` lowercase — must NOT substitute, regex is case-sensitive).
  - **Migration table:** for each Airlift `serverConfig` key in use, test that the Go-side typed equivalent produces the same observable HTTP server behaviour.
- **Non-determinism risks:** none for config loading; env-var values are captured at preprocess time and not re-read.

## Open Questions

- @architect (self): for `modules:` / `managedApps:`, which extension scenarios do real operators actually use? If the answer is "only in-house custom routing rules", we may be able to cover them with the external rules HTTP transport (`byRoutingExternal`) + a documented Go API for compile-time additions. Worth asking the trino-gateway maintainers.
- @qa-tech-lead: what's our standard for operator-facing config compat? Strict — same YAML works unchanged? Loose — same YAML with a migration tool? Affects how much work goes into preserving every Airlift `serverConfig` key.
- @trino-expert: is `databaseCache:` widely used? If most operators leave it disabled, an exact cache-policy match doesn't matter much.

## Cross-references

- [[architecture-overview.architect.md]] — how config drives module wiring
- [[jvm-idioms-not-to-port.md]] — `@PostConstruct`/`@PreDestroy`, Guice `Multibinder`, `@Inject` patterns the config currently feeds
- [[library-landscape-go-mapping.md]] — YAML library selection rationale
