---
title: Config loading — Go-implementer addendum (YAML→struct mapping, custom unmarshalers, validators)
author: go-implementer
role: Go Implementer
component: trino-gateway
topics:
  - config
  - cross-cutting
date: 2026-05-24
status: draft
risk: medium
version_pins:
  trino: 481-150-g93e020bf9df
  trino-gateway: 19-21-g334ba12
related-to:
  - trino-gateway/config-coupling-depth.architect.md
  - trino-gateway/library-landscape-go-mapping.md
  - trino-gateway/jvm-dependencies-inventory.go-implementer.md
---

# Config loading — Go-implementer addendum (YAML→struct mapping, custom unmarshalers, validators)

## Summary

This study is the **Implementer-side addendum** to `[[config-coupling-depth.architect.md]]` (the source-of-truth catalog of the ~20 sub-configs, the three sharp edges, and the Airlift `serverConfig` translation story). Read that first. This file lands four concrete decisions the Implementer needs before writing `internal/config`: (1) `gopkg.in/yaml.v3` + plain typed structs, not `koanf` or `viper` — confirms architect's pick with a why; (2) env-var substitution as a pre-parse text rewrite using the exact regex from `ConfigurationUtils.java:24`, not via `os.ExpandEnv` (different syntax); (3) a single `Config.Validate() error` that aggregates errors with `errors.Join` rather than fail-fast, so operators see every issue per launch; (4) the `modules:`/`managedApps:` reflective extension hook drops with a typed `Extension` interface for compile-time plugins. Plus three Go-side gotchas neither prior study surfaces: `yaml.v3`'s default zero-value behavior loses Java's "field is set vs. field is default" distinction (matters for booleans like `runMigrationsEnabled`), enum unmarshaling needs explicit `UnmarshalYAML` methods (not `iota`), and the cookie-config singleton dance becomes a plain `*GatewayCookieSigner` passed to the auth filter — no global state needed.

## Key Findings

### Concrete picks for the config stack

| Concern | Pick | Rationale |
|---|---|---|
| YAML parser | `gopkg.in/yaml.v3` | YAML 1.2 support, line-numbered error messages, struct-tag-driven binding (`yaml:"field,omitempty"`). Matches what the architect proposed in `[[config-coupling-depth.architect.md]]` § "Implications for Go Rewrite". Avoid `ghodss/yaml` (YAML 1.1, no longer maintained). |
| Config tree shape | Single typed `Config struct` at package root mirroring `HaGatewayConfiguration.java:26-299` | Avoid `spf13/viper` — its hot-reload/env-magic/multi-source features are unwanted complexity for a single-file boot-time load. Avoid `koanf` — same reason. The Java side loads exactly one YAML file once at startup (`HaGatewayLauncher.java:115`); a typed struct is the simplest shape that fits. |
| Env-var preprocessor | Hand-rolled `replaceEnvVars(yaml []byte) ([]byte, error)` with `regexp.MustCompile(\`\\$\\{ENV:([a-zA-Z][a-zA-Z0-9_-]*)\\}\`)` | Exact regex from `ConfigurationUtils.java:24` for syntax parity. `os.ExpandEnv` is the wrong tool — its syntax is `$VAR` or `${VAR}`, not `${ENV:NAME}`. |
| Validation framework | Custom `Config.Validate() error` + `errors.Join` for aggregation. Optionally `go-playground/validator/v10` for tag-driven per-field rules. | JSR-380 (`@NotNull`, `@Min`, `@URL`) is the Java equivalent. Go's `validator/v10` covers most of this with struct tags but the cross-field rules (e.g. statement-path prefix check at `HaGatewayConfiguration.java:280-289`) need imperative code anyway. Recommend `validator/v10` for per-field tags + a `Validate() error` method for cross-field rules. |
| `DataSize` / `Duration` unmarshaling | One small internal package `internal/config/units` providing `Duration` (delegates to `time.ParseDuration`) and `DataSize` (`int64` bytes) with `yaml.Unmarshaler` implementations | Java's Airlift `Duration` and `DataSize` types accept strings like `"30s"`, `"5m"`, `"32MB"`, `"1GB"` (see `[[jvm-dependencies-inventory.go-implementer.md]]` gotcha #1). Go's stdlib parses time but not bytes. `dustin/go-humanize.ParseBytes` is close but uses MB=1000^2 vs Airlift's MB=1024^2 convention — verify per-field before adopting. |
| Enum handling | Per-enum custom `UnmarshalYAML` method | Go has no native enum. Implementing `(*RulesType).UnmarshalYAML(node *yaml.Node) error` keeps the typed-enum-from-string story explicit and lets us produce useful error messages ("unknown rules type 'extrnal' — did you mean 'EXTERNAL'?"). |
| Schema documentation | Generated from struct tags via a tiny tool emitting Markdown | Java's typed config classes are self-documenting via field names and annotations. Go's equivalent is reflection-based doc-gen. Phase-2 concern; flag for `@architect` to decide. |

### Three divergences from `[[config-coupling-depth.architect.md]]`

Small divergences worth flagging before either of us writes config-loading code.

- **Validation strategy.** Architect says "Validation as a method `Config.Validate() error` that runs after unmarshalling — replacing JSR-380 annotations." I agree on the shape but recommend `errors.Join` aggregation: collect *all* validation errors and surface them in one shot, not the first one. Operators with a misconfigured YAML want to see every problem per launch, not fix-launch-fix-launch. Small implementation detail, big operator-ergonomics win.
- **Tag-driven per-field validation.** Architect's draft doesn't take a position on `validator/v10` vs. fully imperative validation. My recommendation: `validator/v10` for the per-field rules (`required`, `url`, `min`, `max`, `gte`, `oneof=FILE EXTERNAL`) and imperative code for cross-field rules (statement-path prefix, OIDC issuer requires client-id, etc.). Splits the boring stuff from the interesting stuff.
- **Cookie-config singleton replacement.** Architect's study calls out the `GatewayCookieConfigurationPropertiesProvider` singleton dance (`HaGatewayProviderModule.java:105-109`) but doesn't propose a Go replacement. My recommendation: **plain dependency injection — the auth filter's constructor takes `*GatewayCookieSigner` directly**, no global. The singleton exists in Java only because `GatewayCookie` (a non-Guice POJO deserialized from request bodies) can't receive injected dependencies. In Go, cookies are deserialized in handler functions which already hold a reference to `*GatewayCookieSigner` from their constructor. Zero global state required. **This is a place where Go is structurally cleaner than Java**, not just a port.

### Three Go-side gotchas neither prior study surfaces

These are trap-detection findings — not picks.

- **Zero-value collision: `bool false` is indistinguishable from "unset" in `yaml.v3`.** `DataStoreConfiguration.java:22-24` declares `private boolean queryHistoryEnabled = true`. A Go field `QueryHistoryEnabled bool` defaults to `false` — operators who omit the field get the opposite of the Java default. **Two fixes:**
  1. **Initialize defaults before unmarshal:** `cfg := DefaultConfig(); yaml.Unmarshal(data, &cfg)`. `yaml.v3` overlays present fields onto the existing struct value. Simplest, my pick.
  2. **Use `*bool` for fields with non-`false` defaults** and check for `nil`. Ugly, makes the rest of the code pay the tax.
  Pick (1) and document `DefaultConfig()` as the contract for "what you get when the YAML is empty". Same trick covers `runMigrationsEnabled = true` (`DataStoreConfiguration.java:24`) and `queryHistoryHoursRetention = 4` (`:23`).
- **`yaml.v3` is strict about unknown fields only with `yaml.KnownFields(true)` — and we want it on.** Default: unknown fields silently ignored. Java/Jackson's behavior is configurable but the gateway uses default (also silent). **My recommendation: turn on `KnownFields(true)` for the Go version** — silent ignoring of unknown fields is how operators end up with broken configs from typos like `data_store:` (should be `dataStore:`). This is a deliberate behavior tightening; flag in migration docs.
- **The statement-path prefix validator (`HaGatewayConfiguration.java:280-289`) runs on `setAdditionalStatementPaths`, not at config-load time.** Java's per-setter validation fires as Jackson unmarshals each field. In Go, with `yaml.v3` unmarshaling directly to struct fields (no setter), the equivalent check has to live in `Config.Validate()`. Subtle but important: if the Java side throws at unmarshal time and the Go side throws at validate time, error messages and stack frames differ. Same error class name (`HaGatewayConfigurationException` → `ConfigError`) and same human-readable message — operators may grep logs for "Statement paths cannot be prefixes of other statement paths" verbatim.

### What the Java code tells us about Go shape

(Not duplicating `[[config-coupling-depth.architect.md]]`'s catalog; calling out only the Implementer-relevant specifics.)

- **One YAML file, one struct, one `Unmarshal` call.** `HaGatewayLauncher.java:115`:
  ```java
  objectMapper.readValue(replaceEnvironmentVariables(config), HaGatewayConfiguration.class);
  ```
  Go shape:
  ```go
  raw, err := os.ReadFile(path)              // 1
  subst, err := replaceEnvVars(raw)          // 2 — exact ConfigurationUtils.java parity
  var cfg = DefaultConfig()                  // 3 — non-zero defaults pre-loaded
  err = yaml.UnmarshalStrict(subst, &cfg)    // 4 — KnownFields(true) enabled
  err = cfg.Validate()                       // 5 — per-field + cross-field, errors.Join
  ```
  Five steps, ~50 LOC in `internal/config/load.go`. Tiny.
- **`modules:` and `managedApps:` reflective loading drops cleanly.** Architect's study correctly classifies these as `drop`. Go replacement: a typed `Extension` interface (`Register(router chi.Router, lifecycle *Lifecycle) error`) implemented by compile-time-included packages, listed in `main` as `[]Extension{routingrules.New(cfg), authplugin.New(cfg)}`. Operators with custom Java modules need a documented "fork-and-rebuild" migration story (architect's recommendation in `[[config-coupling-depth.architect.md]]`, which I support).
- **`serverConfig:` map needs explicit per-key translation.** Architect proposes a typed translation table. My Implementer addition: **don't accept unknown `serverConfig:` keys silently.** The Java side passes them through to Airlift, which ignores unknown keys. The Go side should *reject* unknown keys with `unknown serverConfig key '<key>' — see migration guide for typed equivalents`. Forces operators to confront the rename instead of silently losing tuning.
- **Cross-field validations to port:** statement-path prefix check (`HaGatewayConfiguration.java:280-289`); OIDC issuer-and-client-id co-presence (in `OAuthConfiguration` — not yet read); LDAP URL + bind-DN co-presence; ranged numerics like `queryHistoryHoursRetention >= 1`. All belong in `Config.Validate()`, all use `errors.Join`.

## Behavior vs. Implementation Artifact

### `${ENV:NAME}` substitution syntax
- **Observed behavior:** `ConfigurationUtils.replaceEnvironmentVariables` rewrites the YAML text *before* parsing, replacing exact-match `${ENV:NAME}` substrings with `System.getenv("NAME")`. Missing env var → `IllegalArgumentException` halts startup (`ConfigurationUtils.java:38-42`). Regex is case-sensitive (`${env:NAME}` lowercase does NOT match).
- **Source of behavior:** `gateway-design-intent` — minimal, explicit, fails loud.
- **Go obligation:** `replicate-exactly`. Use the same regex string verbatim. Same hard-fail on missing env var. Do NOT use `os.ExpandEnv` (different syntax: `$VAR`/`${VAR}`). Operators have YAML files in source control depending on this exact syntax.
- **Notes:** the regex `\$\{ENV:([a-zA-Z][a-zA-Z0-9_-]*)\}` requires the var name start with a letter, allows letters/digits/underscores/hyphens. Most env-var conventions allow leading underscore — this regex doesn't. Faithful port should preserve the restriction for behavioral parity.

### Per-setter validation
- **Observed behavior:** `setAdditionalStatementPaths` (`HaGatewayConfiguration.java:242-248`) calls `validateStatementPath` per element during set; on violation throws `HaGatewayConfigurationException`. Other setters are pure assignment.
- **Source of behavior:** `gateway-design-intent` — early failure on bad config.
- **Go obligation:** `replicate-intent`, not `replicate-exactly`. Go has no setters; validation moves to `Config.Validate()` post-unmarshal. Same checks, same error messages, different invocation point. Documented in migration guide so operators searching for the error string in logs still find it.

### Singleton-bootstrap dance for cookie configs
- **Observed behavior:** `GatewayCookieConfigurationPropertiesProvider.initialize(cookieConfig)` is called at the top of `HaGatewayProviderModule`'s constructor (`HaGatewayProviderModule.java:105-109`); afterward, non-Guice classes call `GatewayCookieConfigurationPropertiesProvider.getInstance()` to read cookie-signing config.
- **Source of behavior:** `jvm-artifact` — Guice doesn't reach all classes, so a singleton is the escape hatch.
- **Go obligation:** `drop`. In Go, the cookie-handling functions are plain methods on the auth handler/middleware, which holds `*GatewayCookieSigner` from its constructor. No global. This eliminates a class of subtle initialization-order bugs.
- **Notes:** the OAuth2 cookie equivalent (`OAuth2GatewayCookieConfigurationPropertiesProvider`) gets the same treatment.

### `databaseCache:` knob mapping
- **Observed behavior:** `DatabaseCacheConfiguration` exposes Caffeine cache size/TTL in YAML; Java code wires them to Caffeine builders.
- **Source of behavior:** `gateway-design-intent` — ops-tunable cache pressure.
- **Go obligation:** `replicate-intent`. Same YAML knobs (`maximumSize`, `expireAfterWriteSeconds`); apply to whichever Go cache library wins (see `[[persistence-and-db-schema.go-implementer.md]]` and `[[concurrency-and-lifecycle-model.go-implementer.md]]` divergences — currently `hashicorp/golang-lru/v2` + `singleflight`). `expireAfterWriteSeconds` maps to `golang-lru/v2/expirable`'s TTL. If the chosen lib doesn't expose `maximumSize` as expected (e.g. plain `golang-lru/v2` is size-bounded but eviction policy differs from W-TinyLFU), document the operator-visible difference per architect's note.

## Implications for Go Rewrite

- **Package layout:** `internal/config` with sub-packages `units/` (`Duration`, `DataSize` types and YAML marshalers), `validate/` (cross-field rules), and the root package holding the `Config struct` and `Load(path string) (*Config, error)` function.
- **`Load` is the only entry point.** Operators get `cfg, err := config.Load(path)` and `cfg.Validate()` is called inside `Load`. No two-step usage at call sites.
- **`DefaultConfig()` is the single source of truth for default values.** All Java-side `private foo = X` defaults are mirrored as `DefaultConfig` field assignments. **Code-review rule:** new config field requires adding a default to `DefaultConfig()` even if the zero value is intentional — forces deliberateness.
- **One small `units` package** (~80 LOC) covers `Duration` (delegates to `time.ParseDuration`) and `DataSize` with `yaml.Unmarshaler` implementations. Unit tests with golden cases: `"30s"`, `"5m"`, `"32MB"`, `"1GB"`. Architect's `[[jvm-dependencies-inventory.go-implementer.md]]` suggested `internal/airliftish`; on reflection `internal/config/units` is more discoverable.
- **Enum unmarshaling:** every enum type implements `UnmarshalYAML(*yaml.Node) error` returning a useful error including the field path and the offending value. ~10 LOC per enum; tedious but pays off in operator support.
- **Strict-unknown-fields on by default.** `yaml.KnownFields(true)`. Document the tightening in migration notes.
- **No hot reload of the root config.** Mirrors Java behavior (architect's note). The one hot-reloaded thing — `routingRules.rulesConfigPath` — is owned by the routing-engine component, not the config loader.
- **`internal/config` has zero dependencies on other internal packages.** It produces a `*Config`; the composition root in `main` consumes it. Keeps test scope tight.

## Test Strategy Hooks

- **Test level:** mostly unit (golden YAML fixtures); one integration test that loads the canonical `trino-gateway/gateway-ha/config.yaml` and verifies all sub-configs populate.
- **Fixtures required:**
  - **Golden YAML corpus:** copy fixture YAML files from `trino-gateway/gateway-ha/src/test/resources/` (a known-good set) and assert `Load` succeeds + produces expected struct values. Same files the Java tests use — gives differential coverage for free.
  - **Env-var substitution cases:** set/unset, mixed-case-not-substituted, multiple substitutions in one file, escape-character handling (`\${ENV:X}` — Java behavior unclear, verify).
  - **Default-value cases:** empty YAML `{}` should yield `DefaultConfig()`. Per-field omission should preserve default. Explicit `false` should override `true` default.
  - **Validation aggregation:** YAML with 3 simultaneous errors (bad statement path, negative retention, malformed URL) should fail with all 3 errors visible via `errors.Unwrap`-walking the joined error.
  - **Unknown-fields rejection:** typo (`dataStor:`) fails fast with a useful error message including the line number.
- **Observable signals:** struct field values, error message text (for grep-from-logs parity with Java), specific error types (`*ConfigError`, joinable).
- **Non-determinism risks:** none for config loading. Env-var values captured at preprocess time. **One subtle risk:** if any test sets a process env var, it leaks to parallel tests — use `t.Setenv` (Go 1.17+) which auto-restores.
- See paired QA study (none yet — flagging `@go-qa` for paired coverage of config loading and validation).

## Open Questions

- `@architect`: confirm the `errors.Join` aggregation pattern for `Config.Validate()` — small UX improvement over fail-fast, worth a one-line note in `[[config-coupling-depth.architect.md]]`?
- `@architect`: confirm strict-unknown-fields (`yaml.KnownFields(true)`) as a deliberate behavior tightening from the Java side?
- `@architect`: confirm `serverConfig:` should reject unknown keys (vs. Java which silently passes them to Airlift)?
- `@architect`: where should the typed `Extension` interface live? `internal/extension`? Or per-feature (`internal/routing.Extension`, `internal/auth.Extension`)? Affects how operators bundle their plugins.
- `@trino-expert`: does Airlift's `DataSize` use SI (MB=1000^2) or IEC (MiB=1024^2) for unsuffixed units? Affects the `DataSize` unmarshaler default. `dustin/go-humanize` uses IEC by default (`MB=1000^2`, `MiB=1024^2`) — mismatch could silently halve or double byte counts.
- `@java-analyst`: are there config-validation rules I haven't traced beyond `setAdditionalStatementPaths`? JSR-380 annotations on sub-config fields would be the place to look.
- `@qa-tech-lead`: are operator-visible error message strings part of the contract? If yes, the Go validator must emit byte-identical messages for the cases the Java side already throws.

## Cross-references

- `[[config-coupling-depth.architect.md]]` — sub-config catalog, three sharp edges, behavior-vs-artifact for env-vars / `modules:` / `serverConfig:`. This addendum does not re-catalog.
- `[[jvm-dependencies-inventory.go-implementer.md]]` — `Duration`/`DataSize` parsing rationale, `airliftish` package origin (now superseded here by `internal/config/units`).
- `[[library-landscape-go-mapping.md]]` — architect's library recommendations.
- `[[persistence-and-db-schema.go-implementer.md]]` — `DataStoreConfiguration` is one of the config sub-structs; pool fields added there.
- `[[concurrency-and-lifecycle-model.go-implementer.md]]` — `internal/lifecycle` owns the start order; config is the first component constructed.
