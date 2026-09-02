# Portability, Updates, And Packaging

## Feature ID

`FR-PORT`

## Behavior Summary

PicoClaw builds and updates across supported desktop, server, embedded, and
container targets while keeping startup, binary size, and resource expectations
compatible with low-cost hardware. Supported runtime targets also provide the
single-owner database broker through an owner-only local transport; application
code keeps the same typed `StoreID` contract across operating systems and never
falls back to opening a provider file when that transport is unavailable.

## Reconstruction Notes

- Similarity target: recreate cross-platform build targets, launcher packaging,
  release/update asset selection, retrying downloads, and benchmark tooling.
- Core types/functions: Makefile build targets, release workflow matrix,
  launcher build scripts, updater package, update API handler, Docker build
  workflow, cross-ref coverage isolation, memory benchmark command, and
  cross-platform database supervisor/IPC build coverage.
- Runtime ordering: select platform and architecture, build or locate the
  matching artifact, validate names and prerequisites, retry transient update
  downloads, then report explicit status.
- Non-obvious constraints: unsupported targets must fail rather than select a
  neighboring binary, frontend assets are part of launcher packaging, and
  benchmark code must stay outside runtime packages.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-PORT-001` | MUST | Makefile and release builds produce core binaries for supported Linux, Darwin, Windows, FreeBSD, Android ARM64, ARM, RISC-V, and LoongArch targets, and pull-request CI executes the complete Makefile `build-all` matrix before merge. Linux/MIPSLE, every NetBSD target, FreeBSD/ARM (32-bit), and FreeBSD/RISC-V are unsupported and absent from applicable build and release matrices; FreeBSD AMD64 and ARM64 remain supported release targets. | Portability is a project-level promise, architecture-specific type errors must fail before reaching `main`, and SQLite-backed runtime storage must not silently fall back to legacy persistence on targets unsupported by its dependency stack. |
| `FR-PORT-002` | MUST | Launcher builds include frontend assets and backend binary packaging for supported desktop targets. | Web UI distribution must be reproducible. |
| `FR-PORT-003` | MUST | Updater downloads release assets, validates target platform naming, retries transient HTTP failures, and reports clear status. | Updates must be safe and diagnosable. |
| `FR-PORT-004` | SHOULD | Docker and release workflows keep dependency setup explicit for Go, Node, pnpm, QEMU, and GoReleaser. Repository-wide pull-request tests bound ordinary `go test` package parallelism to four, while coverage-delta serializes instrumented package processes, so concurrent SQLite mappings and large counter binaries cannot exhaust or truncate a small runner disk. Coverage excludes only exact tests that start a second repository-wide grader or config-lock process from the already fully instrumented binary; ordinary and race CI execute those cross-process contracts directly, while base and head coverage apply the same exact-name exclusion. Coverage-delta execution strips inherited product, credential, home, XDG/AppData, Git, temporary-directory, and user-bus authority; gives each compared Git ref a distinct `HOME`, PicoClaw home, workspace, SQLite database, and freshly built core binary throughout; withholds the default-path config while historical unit coverage preserves fallback-`HOME` semantics; publishes a valid loopback config before integration suites; and compares uncovered-statement debt rather than percentage or covered-statement count. Its plan is derived from the head: a suite newly added by the head runs for head coverage, while the immutable base omits only that exact absent suite; an absent head suite or a present non-directory suite on either ref remains a hard failure. Global debt cannot increase, impacted feature debt retains a ten-statement tolerance, and deletion is not a regression when it adds no uncovered debt. A base coverage command may retry exactly once only when its output proves one closed known historical failure: either cleanup-only TempDir race (the agent panic worker's `sessions` cleanup or the asynchronous workflow handler's `workflow_runs/wr_` cleanup), either exact repository-evaluation cancellation conflict, the provider-backed evolution draft test timing out on its one draft inside the exact base coverage sandbox, the repository-review auto-continuation test timing out at its exact immutable-base assertion before one generated `rra_` identity reaches completed, or one sole repository-review API test proving that `repository-reviews.db-wal` or `repository-reviews.db-shm` disappeared during open, identity recheck, or final private-file validation inside that failing test's bound immutable-base sandbox. The SQLite classifier is callsite- and line-independent but binds the exact package, repository-review test hierarchy, test source family, error shape, companion identity, and Go test temporary path. It rejects assertions, panics, build failures, another failing test, the wrong package, a malformed dynamic identity, a detached continuation, or a near-miss path; every head failure and every repeated or unrecognized base failure is final. | CI/release builds must be repeatable, state created by one ref must not alter or deadlock another ref's tests or reach an operator installation, tests that intentionally override `HOME` must retain fallback-home semantics, repository-wide SQLite and instrumented package sets must fit bounded runner resources, structural removals must not be mistaken for lost test coverage, and a synchronization fix in the PR cannot repair an asynchronous cleanup race in the immutable historical base. |
| `FR-PORT-005` | SHOULD | Memory benchmark tools measure ingestion/evaluation behavior without affecting runtime packages. | Low-resource goals need measurable support. |
| `FR-PORT-006` | MUST | Every supported launcher, gateway, and database-CLI build uses the provider-neutral broker protocol over an owner-only Unix-domain socket on Unix or a current-user named pipe on Windows. The supervisor retains one broker pool per catalogued physical store across runtime restart, while runtime clients address only opaque logical `StoreID` values and structured errors. There is no TCP, caller-opened provider, legacy-file, or in-process database fallback; a target without a secure local transport fails closed, and schema/legacy upgrades remain available only through the exclusively fenced offline migration command. | Packaging must preserve one storage ownership and migration contract across operating systems instead of silently changing durability or authority by target. |

## Data And State Model

Portability state includes target OS/architecture tuples, release asset names,
launcher frontend build output, packaged backend binaries, Docker/QEMU build
inputs, updater status and retry counters, downloaded asset metadata, and
memory benchmark inputs and metrics. Database portability state is limited to
the owner-only discovery endpoint, protocol/epoch metadata, and opaque catalog
identities. Physical store names, paths, pools, journal artifacts, and migration
implementation remain owned by the Database Layer and its provider.

## Surface Ownership

Owns: CODE cmd/membench/**
Owns: CODE pkg/updater/**
Owns: CODE scripts/copydir.go
Owns: CODE scripts/coverage_delta.go
Owns: CODE scripts/feature_delta_guard.go
Owns: CODE scripts/feature_inventory.go
Owns: CODE scripts/featuretools_lib.go
Owns: CODE scripts/lint-features.go
Owns: TEST pkg/updater/*
Owns: TEST cmd/membench/*
Owns: TEST integration/*
Owns: TEST scripts/coverage_delta_test.go *
Owns: TEST scripts/portability_requirements_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Make | `make build`, `make build-all`, `make build-launcher` | Cross-platform core and launcher builds. | `FR-PORT-001`, `FR-PORT-002` |
| CI | GitHub Actions release, nightly, build, Docker, DMG workflows | Repeatable packaging and release automation. | `FR-PORT-004` |
| Updater | Update endpoint and updater package | Download, retry, platform asset selection, and status. | `FR-PORT-003` |
| Bench | `cmd/membench` | Memory benchmark ingestion, metrics, and evaluation. | `FR-PORT-005` |
| Local database IPC | Unix-domain socket or current-user Windows named pipe | Carry the same authenticated typed broker protocol and opaque `StoreID` operations on every supported runtime target; unsupported secure transport returns `Unsupported` without local-file fallback. | `FR-PORT-006` |

## Algorithms And Ordering

1. Build commands resolve version metadata and target tuples before invoking Go,
   frontend, packaging, or cross-compilation steps.
2. Launcher packaging builds frontend assets, embeds or copies them into the
   backend distribution path, then emits platform-specific artifacts.
3. Release workflows install explicit Go, Node, pnpm, QEMU, Docker, and
   GoReleaser prerequisites before build or publish steps.
4. Updater logic maps current platform data to an expected release asset, rejects
   missing or mismatched assets, retries transient HTTP failures, and reports
   clear final status.
5. Memory benchmark commands load fixtures, run ingestion/evaluation paths,
   record metrics, and avoid importing benchmark behavior into runtime packages.
6. Launcher, gateway, and database CLI builds attach to or start the local
   supervisor before application storage use. Runtime restart retains its broker
   epoch and pools; only an explicit shutdown followed by an exclusive offline
   migration may replace that owner.
7. Coverage-delta creates separate temporary runtime homes for the base and head
   worktrees, clears inherited `PICOCLAW_HOME`, and replaces inherited `HOME`
   before executing code from either ref. It then compares each scoped profile's
   uncovered-statement debt (`total statements - covered statements`), allowing
   code removal when debt does not grow even if percentage or covered count
   falls. If and only if one base coverage command reports a closed, exact
   historical signature—the agent-session or workflow-run TempDir cleanup race,
   either repository-evaluation cancellation conflict, the provider-backed
   evolution draft timeout in its exact coverage sandbox, the repository-review
   auto-continuation completion timeout for one canonical generated identity, or
   one sole repository-review API failure proving that either
   `repository-reviews.db-wal` or `repository-reviews.db-shm` disappeared while
   being opened, identity-rechecked, or privately validated inside the failing
   test's bound immutable-base sandbox—rerun that complete base command once.
   This SQLite classifier is independent of the assertion callsite and line but
   binds the package, repository-review test hierarchy, source family, error
   shape, companion, and Go test temporary path. Do not retry head, an unrelated
   or additional assertion, a detached or near-miss continuation, panic, build
   failure, unrelated test or package, or any repeated failure.

## Cross-Feature Behavior

Launcher management invokes update behavior. CI gates feature requirements,
tests, integration suites, and builds. Feature inventory, delta, and coverage
guard scripts are packaging and maintenance tooling that enforce feature specs
for changed code in pull requests. Security controls apply to downloads and
credentialed release publishing. The Database Layer owns protocol semantics,
catalog resolution, provider pools, and migration; portability owns proving that
the same no-fallback contract builds and runs through each platform transport.

## Failure And Edge Cases

- Missing release assets return clear errors.
- HTTP 5xx or timeout paths retry before failure.
- Unsupported platform/arch does not select a wrong binary.
- Linux/MIPSLE, NetBSD, FreeBSD/ARM, and FreeBSD/RISC-V builds are not produced; FreeBSD
  AMD64/ARM64 and Android ARM64 remain supported.
- Android and WhatsApp-native variants remain build-tag controlled.
- A missing, insecure, or unsupported local database transport fails startup
  with a structured error. It never enables TCP or makes the launcher, runtime,
  or CLI open a database generation directly.
- Coverage comparison does not reuse launcher or test state across refs, even
  when the two revisions use incompatible lock-file or state formats.
- Coverage comparison fails on any global uncovered-debt increase or an impacted
  feature increase beyond ten statements; a percentage or covered-count drop
  caused only by deleting code is not a failure.
- Coverage comparison retries an exact recognized baseline flake at most once:
  either supported TempDir cleanup signature, one of the two pinned repository
  model-evaluation cancellation signatures, the exact provider-backed evolution
  draft timeout inside the base coverage sandbox, the exact repository-review
  auto-continuation completion timeout for a generated `rra_` identity, or the
  sole repository-review API failure proving a WAL/SHM companion disappearance
  during open, identity recheck, or final private-file validation inside the
  failing test's bound base coverage sandbox. The SQLite case is callsite- and
  line-independent while remaining package-, test-hierarchy-, source-family-,
  error-shape-, companion-, and path-bound. The classifier rejects any extra test
  diagnostic, failure marker, failed package, malformed dynamic identity, detached
  continuation, or near-miss path and preserves the second attempt's result; all
  head, unrecognized, or repeated failures remain visible.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-PORT-001`, `FR-PORT-002`, `FR-PORT-004` | [scripts/portability_requirements_test.go](../../scripts/portability_requirements_test.go), [scripts/coverage_delta_test.go](../../scripts/coverage_delta_test.go), [Makefile](../../Makefile), [web/Makefile](../../web/Makefile), [.github/workflows](../../.github/workflows) |
| `FR-PORT-003` | [pkg/updater/updater_test.go](../../pkg/updater/updater_test.go), [web/backend/api/update.go](../../web/backend/api/update.go) |
| `FR-PORT-005` | [cmd/membench](../../cmd/membench) |
| `FR-PORT-006` | [pkg/database/transport_unix.go](../../pkg/database/transport_unix.go), [pkg/database/transport_windows.go](../../pkg/database/transport_windows.go), [pkg/database/transport_other.go](../../pkg/database/transport_other.go), [pkg/database/supervisor_test.go](../../pkg/database/supervisor_test.go), [pkg/database/architecture_test.go](../../pkg/database/architecture_test.go) |

## Implementation Anchors

- [Makefile](../../Makefile)
- [.goreleaser.yaml](../../.goreleaser.yaml)
- [scripts/coverage_delta.go](../../scripts/coverage_delta.go)
- [pkg/updater/updater.go](../../pkg/updater/updater.go)
- [.github/workflows/release.yml](../../.github/workflows/release.yml)
