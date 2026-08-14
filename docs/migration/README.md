# Migration

Migration notes for major configuration and behavior changes across PicoClaw
versions.

- [Provider accounts and model aliases](model-list-migration.md): migrate
  legacy providers and version 3 model references to version 4
  `account_ref` plus exact `model_aliases[]` selections.
- [Configuration versioning](../reference/config-versioning.md): backup,
  migration, and downgrade rules for every schema version.
- [PR Workspace V19 Cutover](pr-workspace-v19-cutover.md): prepare for the
  destructive replacement of separate review/development records with unified
  PR workspaces.
