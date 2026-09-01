# Local-CI Passing Cache SQLite Cutover

Local CI now stores its mutable passing-result index in the private SQLite
database `<event-state>/pr-workspace-local-ci/evidence/cache.db`. Immutable,
content-addressed plans, executions, attestations, and discovery records remain
JSON files because they are portable evidence rather than mutable store state.

## Upgrade

1. Stop every PicoClaw launcher, gateway, CLI, and background process that uses
   the same workspace or event-state directory. Mixed old and new binaries are
   unsupported.
2. Back up the complete `pr-workspace-local-ci/evidence` directory.
3. Start the upgraded PicoClaw. On first evidence-store open it creates and
   validates `cache.db`, imports bounded valid
   `cache/<prefix>/<digest>.json` indexes after checking their referenced
   immutable execution and plan, and makes SQLite authoritative in the same
   transaction.
4. After commit, examined legacy indexes are moved without overwrite to
   `legacy-json/local-ci-cache-v1/cache/**`. Invalid records are skipped with
   safe issue codes and digests; their payloads are not copied into diagnostics
   or database rows. Archives are retained indefinitely.

An interrupted archive is retried on the next open without importing the same
source twice. A source changed after its committed import, an unsafe filesystem
entry, an oversized index, or a conflicting archive stops the open for operator
inspection. There is no JSON dual write or JSON fallback.

## Rollback

Stop every PicoClaw process first. Restore the archived cache indexes to their
original relative `cache/**` paths, then remove or restore `cache.db` together
with its matching `cache.db-wal` and `cache.db-shm` files from one consistent
backup. Restore the immutable evidence files from the same backup if they were
changed independently. Start only the old version after the complete set is in
place.
