# Config Schema Versioning Guide

## Overview

PicoClaw uses a schema versioning system for `config.json` to ensure smooth upgrades as the configuration format evolves.

## Version History

### Version 1
- **Introduction**: Initial version with version field support
- **Changes**: Added `version` field to Config struct
- **Migration**: No structural changes needed for existing configs

### Version 2
- **Introduction**: Model enable/disable support and channel config unification
- **Changes**:
  - Added `enabled` field to `ModelConfig` — allows disabling individual model entries without removing them
  - During V1→V2 migration, `enabled` is auto-inferred: models with API keys or the reserved `local-model` name are enabled; others default to disabled
  - Migrated legacy channel fields: Discord `mention_only` → `group_trigger.mention_only`, OneBot `group_trigger_prefix` → `group_trigger.prefixes`
  - V0 configs now migrate directly to CurrentVersion (V2) instead of going through V1
  - `makeBackup()` now uses date-only suffix (e.g., `config.json.20260330.bak`) and also backs up `.security.yml`

### Version 3
- **Introduction**: Enhanced type safety and improved error handling
- **Changes**:
  - Added comma-ok type assertions in channel configuration decoding to prevent potential panics
  - Improved error logging for Weixin channel configuration decoding
  - Enhanced security configuration documentation and examples
  - **Auto-migration**: V2 configs are automatically migrated to V3 on load with no user action required
  - **Backup**: Before migration, the system creates a date-stamped backup (e.g., `config.json.20260413.bak`) in the same directory
  - **Downgrade risk**: Once migrated to V3, the config cannot be safely loaded by older V2-only versions. To downgrade, restore from the auto-created backup file.

### Version 4

- **Introduction**: Explicit account selection and first-class model aliases.
- **Changes**:
  - Added top-level `model_aliases[]`.
  - Added `agents.defaults.account_ref`, per-agent `account_ref`,
    `voice.account_ref`, and `voice.tts_account_ref`.
  - `model_name`, agent primary/fallback values, image/light/subagent model
    references, voice model names, workflow selections, chat selections, and
    model-router terminals now name exact aliases (or an enabled model router
    where the primary selector permits one).
  - Provider metadata and credential accounts no longer supply executable
    default models. A missing effective alias fails with
    `no model configured`.
- **Auto-migration**: V3 configurations are backed up and migrated
  deterministically. The migration never invents a provider model.

### Version 5

- **Introduction**: Semantic developer model aliases.
- **Changes**:
  - Launcher model management exposes `chat`, `code`, `investigate`, `review`,
    and `fast` as stable task roles.
  - Catalog roles are metadata only. They do not select, persist, or imply a
    concrete model until explicitly configured.
  - Aliases mechanically generated from legacy account names or concrete model
    IDs are removed. References to removed aliases are cleared rather than
    redirected to a guessed model.
  - Explicit custom aliases remain supported for specialized capabilities;
    legacy web-search aliases remain custom and never configure a predefined
    semantic role implicitly.
- **Auto-migration**: V4 configurations are backed up and migrated
  deterministically.

### Version 6

- **Historical introduction**: Trusted pull-request review attention policies.
- **Changes**:
  - Added top-level `reviews.attention.global`, keyed by canonical decision
    point.
  - Added `reviews.attention.repositories`, keyed by exact GitHub
    `owner/repo`, with repository-local inherit, overlay, replace, or disable
    policy.
  - Gate policy uses the same working-context AI, isolated AI,
    deterministic, and zero gate types consumed by workflow compilation.
  - Repository identities, decision points, collections, and encoded policy
    size are bounded and validated before use or persistence.
- **Auto-migration**: V5 configurations are backed up and migrated with a
  version-only transformation. Any already-present policy maps, including
  explicit empty decision or repository policies, are preserved semantically.

> This section describes the historical v6 schema. The current breaking
> development-workspace cutover removes `reviews` rather than preserving or
> translating it.

### Current development lifecycle configuration

The current config schema uses top-level `development` for `devw_` development
workspaces. It contains:

- named `workflow-configurations`, one `default-workflow-configuration`, exact
  `repository-assignments`, and atomic action overrides bound by the exact pair
  `workflow-ref` plus full `gate-ref: gates.<id>`;
- independent review and completion adaptive-nudge minimum/maximum bounds;
- strict/relaxed `scope-disposition` defaults plus optional work-type overrides
  and bounded custom relevance prompts;
- monotonic `XS`, `S`, and `M` file, semantic-line, and module thresholds used
  to derive the `XS` through `L` implementation size grade.

The authenticated scoped APIs are split by ownership:

- `GET`/`PUT /api/development/workflow-configurations` owns the named
  configurations, default selection, nudge bounds, and scope thresholds;
- `GET`/`PUT /api/development/repositories` owns verified repository
  descriptors and exact assignments, and projects name/deferred-policy
  summaries for selectable configurations.

Both writes use the same exact full-config revision compare-and-swap fence,
preserve the other endpoint's fields server-side, and report whether a gateway
restart is required. Removing an assigned configuration fails atomically.
Configuration action precedence is exact: a matching binding replaces the
complete workflow `default-action`; without a binding the default is used; if
neither exists, execution fails closed. Partial action merging is unsupported.

`reviews` is no longer a supported top-level field or an empty compatibility
placeholder. It is not auto-migrated to `development`; recreate desired
workflow configurations explicitly. Gate V2 serialized fields are unknown to the current
schema and are rejected rather than migrated.
See [Development Workspaces](../guides/development-workspaces.md) and
[Development Workspace V20 Cutover](../migration/development-workspace-v20-cutover.md).

## How It Works

### Automatic Migration
When you load a config file:
1. The system first reads the `version` field from the JSON
2. Based on the detected version, it loads the appropriate config struct (`configV0`, `configV1`, etc.)
3. If the loaded version is less than the latest, migrations are applied incrementally
4. Before saving, the system automatically creates a date-stamped backup of `config.json` and `.security.yml`
5. The version number is updated automatically
6. The migrated config is automatically saved back to disk

### Version Field
The `version` field in `config.json` indicates the schema version:
- `0` or missing: Legacy config (no version field)
- `1`: Previous version (will be auto-migrated to V2 on load)
- `2`: Previous version (will be auto-migrated to V3 on load)
- `3`: Previous version (will be auto-migrated to V4 on load)
- `4`: Previous version (will be migrated incrementally to V6 on load)
- `5`: Previous version (will be auto-migrated to V6 on load)
- `6`: Current version

```json
{
  "version": 6,
  "agents": {...},
  ...
}
```

## Adding a New Migration

When making breaking changes to the config schema:

### Step 1: Define the New Version Struct

Create a new struct for the new version if the structure changes significantly:

```go
// ConfigV6 represents version 6 config structure
type ConfigV6 struct {
    Version   int             `json:"version"`
    Agents    AgentsConfig    `json:"agents"`
    // ... other fields with new structure
}
```

### Step 2: Update Current Config Version

```go
const CurrentVersion = 6  // Increment this
```

### Step 3: Add a Loader Function

```go
// loadConfigV6 loads a version 6 config
func loadConfigV6(data []byte) (*Config, error) {
    cfg := DefaultConfig()

    // Parse to ConfigV6 struct
    var v6 ConfigV6
    if err := json.Unmarshal(data, &v6); err != nil {
        return nil, err
    }

    // Convert to current Config
    cfg.Version = v6.Version
    cfg.Agents = v6.Agents
    // ... map other fields

    return cfg, nil
}
```

### Step 4: Add Migration Logic

```go
func (c *configV5) Migrate() (*Config, error) {
    // Apply V5→V6 structural changes here
    migrated := &c.Config
    migrated.Version = 6
    // Apply structural changes
    return migrated, nil
}
```

### Step 5: Update LoadConfig Switch

```go
func LoadConfig(path string) (*Config, error) {
    // ... read file ...

    switch versionInfo.Version {
    case 0:
        cfg, err = loadConfigV0(data)
    case 1:
        cfg, err = loadConfigV1(data)
    case 2:
        cfg, err = loadConfig(data)
    case 3:
        cfg, err = loadConfigV3(data)
    case 4:
        cfg, err = loadConfigV4(data)
    case 5:
        cfg, err = loadConfigV5(data)
    case 6:
        cfg, err = loadConfigV6(data)
    default:
        return nil, fmt.Errorf("unsupported config version: %d", versionInfo.Version)
    }

    // ... migrate and validate ...
}
```

### Step 6: Test Your Migration

Create a test in `config_migration_test.go`:

```go
func TestMigrateV5ToV6(t *testing.T) {
    // Create a version 5 configuration map
    v5Config := map[string]any{
        "version": 5,
        // ... set up data that must be preserved
    }

    // Apply migration
    if err := migrateV5ToV6(v5Config); err != nil {
        t.Fatalf("Migration failed: %v", err)
    }

    // Verify version is updated
    if v5Config["version"] != 6 {
        t.Errorf("Expected version 6, got %v", v5Config["version"])
    }

    // Verify data is preserved/transformed correctly
    // ...
}
```

## Migration Best Practices

1. **Version-Specific Structs**: Define a separate struct for each version that has structural changes
2. **Backward Compatibility**: Ensure old configs can still be loaded with their specific structs
3. **No Data Loss**: Migrations should preserve all user settings
4. **Idempotent**: Running the same migration multiple times should be safe
5. **Auto-Save**: Migrated configs are automatically saved to update the user's file
6. **Auto-Backup**: Before saving, the system creates a date-stamped backup of `config.json` and `.security.yml`
7. **Test Thoroughly**: Test with real user config files
8. **Update Defaults**: Keep `defaults.go` in sync with the latest schema

## V2→V3 Migration Guide

### What Changed?

Version 3 introduces improved type safety and error handling:

- **Type-safe channel decoding**: All channel type assertions now use comma-ok pattern (`val, ok := v.(*Settings)`) to prevent panics if Type and Settings are mismatched
- **Enhanced error logging**: Weixin channel now logs errors on `GetDecoded()` failure for consistency with other channels
- **Documentation fixes**: Corrected stray quotes in JSON configuration examples

### Auto-Migration Behavior

When you run PicoClaw with a V2 config file:

1. **Detection**: PicoClaw reads the `version` field and detects V2
2. **Backup**: Before any changes, creates `config.json.YYYYMMDD.bak` (e.g., `config.json.20260413.bak`)
3. **Migration**: Applies V2→V3 structural changes (primarily internal type safety improvements)
4. **Save**: Writes the updated config with `"version": 3`
5. **Continue**: Starts normally with the V3 config

**No user action required** — the migration happens automatically on first load.

### Backup Location

Backups are created in the same directory as your config file:

- **Default**: `~/.picoclaw/config.json.20260413.bak`
- **Custom path**: If using `PICOCLAW_CONFIG`, backup is created next to that file
- **Security file**: `.security.yml` is also backed up as `.security.yml.YYYYMMDD.bak`

### Downgrade Risk

⚠️ **Important**: Once migrated to V3, the config **cannot** be safely loaded by older PicoClaw versions that only support V2.

**To downgrade:**

1. Stop PicoClaw
2. Restore the backup:
   ```bash
   cp ~/.picoclaw/config.json.20260413.bak ~/.picoclaw/config.json
   cp ~/.picoclaw/.security.yml.20260413.bak ~/.picoclaw/.security.yml  # if it exists
   ```
3. Use a PicoClaw version that supports V2 configs

**Alternative**: Manually edit `config.json` and change `"version": 3` to `"version": 2`. This works because V3 changes are primarily code-level safety improvements, not structural schema changes.

## V3→V4 Model Alias Migration

Version 4 separates the two values that version 3 often overloaded in
`model_name`:

```json
{
  "version": 4,
  "agents": {
    "defaults": {
      "account_ref": "openai-work",
      "model_name": "coding"
    }
  },
  "model_aliases": [
    {
      "name": "coding",
      "model": "gpt-5.4",
      "account_overrides": {
        "credential:github-copilot:work": "gpt-4.1"
      }
    }
  ]
}
```

Migration rules are intentionally conservative:

1. A non-router `model_list` name becomes an alias only when all rows with that
   name map to one concrete model.
2. A legacy raw model reference becomes an alias only when it maps to exactly
   one concrete account/model pair.
3. A successfully migrated selection stores the concrete account in
   `account_ref` and retains the generated alias in the model field.
4. Credential-only, account-router, unknown, and ambiguous selections move to
   `account_ref`, but their model alias is cleared. Startup then reports
   `no model configured` until the user chooses or creates an alias.
5. Legacy fallback, image, light, subagent, voice, and model-router references
   that are not generated aliases are removed or cleared. No provider default
   is substituted.

After migration, review every empty `model_name`, choose an exact alias, and
verify that alias overrides use concrete account refs rather than account-router
names.

## Example Migration

### Scenario: Unambiguous legacy model selection

Old config (version 3):
```json
{
  "version": 3,
  "model_list": [
    {
      "model_name": "openai-work",
      "provider": "openai",
      "model": "gpt-5.4"
    }
  ],
  "agents": {
    "defaults": {
      "model_name": "openai-work"
    }
  }
}
```

New config (version 4):
```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "openai-work",
      "provider": "openai",
      "model": "gpt-5.4"
    }
  ],
  "model_aliases": [
    {
      "name": "openai-work",
      "model": "gpt-5.4"
    }
  ],
  "agents": {
    "defaults": {
      "account_ref": "openai-work",
      "model_name": "openai-work"
    }
  }
}
```

## Troubleshooting

### Config Not Upgrading
- Check that `CurrentVersion` is incremented
- Verify migration logic handles the target version
- Ensure `Migrate()` is called in `LoadConfig()`

### Migration Errors
- Check error messages for specific migration failures
- Review migration logic for edge cases
- Ensure all required fields are properly initialized
- Verify the loader function for the source version

### Data Loss After Migration
- Ensure all fields are copied during migration
- Check that the migration doesn't overwrite values with defaults unnecessarily
- Review the conversion logic in the loader functions
- Check the auto-backup files (e.g., `config.json.20260330.bak`) to recover original data
