# Migration

Migration notes for major configuration and behavior changes across PicoClaw
versions.

- [Provider accounts and model aliases](model-list-migration.md): migrate
  legacy providers and version 3 model references to version 4
  `account_ref` plus exact `model_aliases[]` selections.
- [Configuration versioning](../reference/config-versioning.md): backup,
  migration, and downgrade rules for every schema version.
- [Development Workspace V20 Cutover](development-workspace-v20-cutover.md):
  prepare for the destructive replacement of v18/v19 PR state with `devw_`
  development workspaces and durable notifications.
