# Database Migration Stage Retirement

## Feature ID

`FR-DATABASE-STAGE-RETIREMENT`

## Behavior Summary

PicoClaw provides dormant provider-internal capabilities that retain and
retire exact SQLite migration-stage identities without trusting later pathname
absence or deleting a replacement object.

## Reconstruction Notes

- Retention binds an absolute clean path, its owner-private single-link regular
  main, and its private parent through retained platform handles.
- Retirement first proves the original path still names that identity, moves
  it to a cryptorandom no-replace quarantine in the retained parent, proves the
  quarantine, then unlinks or disposes that exact handle.
- Unix proves link count zero and fsyncs the retained parent. Windows keeps its
  long-lived handle free of delete access so SQLite can open the file, then
  uses `ReOpenFile` to add delete access to the same identity only after SQLite
  closes and proves deletion-pending after disposition.
- A path check never refreshes or substitutes the retained identity. Unsupported
  platforms and Unix filesystems without atomic no-replace rename fail closed.

## Requirements

| ID | Level | Trigger/Input | Required Output | State Mutation | Failure/Edge | Rationale |
| --- | --- | --- | --- | --- | --- | --- |
| `FR-DATABASE-STAGE-RETIREMENT-001` | MUST | Provider code retains one newly created absolute stage main before untrusted migration code or cleanup can move it. | The exact private regular single-link file and its exact private parent are held by platform identities; `Check` proves any supplied current path still names that object. | Retains bounded file/parent handles only. | Invalid/canceled input, unsafe ancestor/type/owner/mode/link/reparse, identity transition, missing object, or unsupported platform fails without granting a capability. | A later pathname must not replace the identity that provider code created. |
| `FR-DATABASE-STAGE-RETIREMENT-002` | MUST | Provider cleanup retires a retained stage under exclusive migration authority. | The original path is rechecked, the retained identity is atomically no-replace quarantined, rechecked, removed through the retained parent/handle, and proven unlinked or deletion-pending with both names absent. | Removes only the exact retained object and durably syncs the Unix parent; Windows performs handle-scoped write-through rename/disposition. | Cancellation before mutation, rename/replacement/decoy, hardlink, parent drift, quarantine collision/change, unlink/disposition uncertainty, sync failure, or closed handles reports failure and preserves evidence where retirement is unproved. | Pathname absence alone cannot prove deletion and must never authorize deleting a substitute. |
| `FR-DATABASE-STAGE-RETIREMENT-003` | MUST | Copy, normalization, migration, validation, pin discard, or cutover completes or fails. | Every provider-created main is retained from creation until exact retirement or successful installed-identity proof; partial copy members are individually retained and cleanup never falls back to a raw name after retention admission. | Successful pre-cutover cleanup retires exact disposable identities; successful cutover closes retention only after the installed target matches it. | A renamed partial, working, or final stage remains diagnostic evidence and blocks unsafe progress; a decoy at the old name remains untouched. | Continuous identity ownership closes the gaps between creation, SQLite work, validation, and cleanup. |

## Data And State Model

`retainedStagedGeneration` is a one-process, provider-private capability with
one platform state, monotonic live/retired/closed state, and no serialization.
Quarantine names contain 128 random bits and are never protocol values.

## Surface Ownership

Owns: CODE internal/sqliteprovider/staged_retirement*.go
Owns: TEST internal/sqliteprovider/staged_retirement*_test.go *

## Auxiliary Interfaces

| Type | Surface | Contract | Requirement IDs |
| --- | --- | --- | --- |
| Internal Go API | `retainStagedGeneration`, `retainedStagedGeneration.Check` | Retain and re-prove one exact stage identity/path without refreshing it. | `FR-DATABASE-STAGE-RETIREMENT-001`, `FR-DATABASE-STAGE-RETIREMENT-003` |
| Internal Go API | `retainedStagedGeneration.Retire`, `Close` | Retire only the exact retained identity, or release handles without mutation. | `FR-DATABASE-STAGE-RETIREMENT-002`, `FR-DATABASE-STAGE-RETIREMENT-003` |

## Algorithms And Ordering

Validate syntax and ancestors; retain parent then main; recheck parent, path,
identity, privacy, type, and link count. For retirement, check cancellation;
revalidate; choose an unused random sibling; atomically no-replace rename the
retained identity; prove source absence and quarantine identity; unlink or
dispose the exact object; prove terminal handle state and both names absent;
sync the retained Unix parent; close handles.

## Cross-Feature Behavior

The offline SQLite provider creates and transfers these capabilities. Physical
claims independently pin the final replacement. Backup live-source verification
consumes only the separate opaque final-stage proof and never receives a
retirement capability.

## Failure And Edge Cases

Close is idempotent and never deletes. Retire is idempotent only after terminal
proof. Once quarantine mutation starts, the bounded critical section finishes
synchronously rather than abandoning an ambiguous identity on cancellation.
The caller-held fence, claims, private directory, and quiescence protocol remain
the authority boundary against another same-UID process.

## Acceptance Evidence

| Requirement IDs | Evidence |
| --- | --- |
| `FR-DATABASE-STAGE-RETIREMENT-001` | [staged_retirement_unix_test.go](../../internal/sqliteprovider/staged_retirement_unix_test.go), [staged_retirement_windows_test.go](../../internal/sqliteprovider/staged_retirement_windows_test.go) |
| `FR-DATABASE-STAGE-RETIREMENT-002` | [staged_retirement_coverage_unix_test.go](../../internal/sqliteprovider/staged_retirement_coverage_unix_test.go), [staged_retirement_coverage_linux_test.go](../../internal/sqliteprovider/staged_retirement_coverage_linux_test.go) |
| `FR-DATABASE-STAGE-RETIREMENT-003` | [provider_offline_test.go](../../internal/sqliteprovider/provider_offline_test.go), [staged_source_closeout_test.go](../../internal/sqliteprovider/staged_source_closeout_test.go) |

## Implementation Anchors

[staged_retirement.go](../../internal/sqliteprovider/staged_retirement.go),
[staged_retirement_unix.go](../../internal/sqliteprovider/staged_retirement_unix.go),
and [staged_retirement_windows.go](../../internal/sqliteprovider/staged_retirement_windows.go).
