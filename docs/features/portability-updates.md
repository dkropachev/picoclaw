# Portability, Updates, And Packaging

## Feature ID

`FR-PORT`

## Behavior Summary

PicoClaw builds and updates across supported desktop, server, embedded, and
container targets while keeping startup, binary size, and resource expectations
compatible with low-cost hardware.

## Requirements

| ID | Level | Requirement | Rationale |
| --- | --- | --- | --- |
| `FR-PORT-001` | MUST | Makefile builds produce core binaries for supported Linux, Darwin, Windows, ARM, MIPS, RISC-V, and LoongArch targets. | Portability is a project-level promise. |
| `FR-PORT-002` | MUST | Launcher builds include frontend assets and backend binary packaging for supported desktop targets. | Web UI distribution must be reproducible. |
| `FR-PORT-003` | MUST | Updater downloads release assets, validates target platform naming, retries transient HTTP failures, and reports clear status. | Updates must be safe and diagnosable. |
| `FR-PORT-004` | SHOULD | Docker and release workflows keep dependency setup explicit for Go, Node, pnpm, QEMU, and GoReleaser. | CI/release builds must be repeatable. |
| `FR-PORT-005` | SHOULD | Memory benchmark tools measure ingestion/evaluation behavior without affecting runtime packages. | Low-resource goals need measurable support. |

## Auxiliary Interfaces

Owns: TEST pkg/updater/*
Owns: TEST cmd/membench/*
Owns: TEST integration/*

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Make | `make build`, `make build-all`, `make build-launcher` | Cross-platform core and launcher builds. | `FR-PORT-001`, `FR-PORT-002` |
| CI | GitHub Actions release, nightly, build, Docker, DMG workflows | Repeatable packaging and release automation. | `FR-PORT-004` |
| Updater | Update endpoint and updater package | Download, retry, platform asset selection, and status. | `FR-PORT-003` |
| Bench | `cmd/membench` | Memory benchmark ingestion, metrics, and evaluation. | `FR-PORT-005` |

## Cross-Feature Behavior

Launcher management invokes update behavior. CI gates feature requirements,
tests, integration suites, and builds. Security controls apply to downloads and
credentialed release publishing.

## Failure And Edge Cases

- Missing release assets return clear errors.
- HTTP 5xx or timeout paths retry before failure.
- Unsupported platform/arch does not select a wrong binary.
- Android and WhatsApp-native variants remain build-tag controlled.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-PORT-001`, `FR-PORT-002`, `FR-PORT-004` | [Makefile](../../Makefile), [web/Makefile](../../web/Makefile), [.github/workflows](../../.github/workflows) |
| `FR-PORT-003` | [pkg/updater/updater_test.go](../../pkg/updater/updater_test.go), [web/backend/api/update.go](../../web/backend/api/update.go) |
| `FR-PORT-005` | [cmd/membench](../../cmd/membench) |

## Implementation Anchors

- [Makefile](../../Makefile)
- [pkg/updater/updater.go](../../pkg/updater/updater.go)
- [.github/workflows/release.yml](../../.github/workflows/release.yml)
