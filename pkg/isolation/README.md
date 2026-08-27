# `pkg/isolation`

`pkg/isolation` provides process-level isolation for child processes started by `picoclaw`.

It does not sandbox the main `picoclaw` process itself.

## Scope

The current scope is the child-process startup path:

- `exec` tool
- CLI providers such as `claude-cli` and `codex-cli`
- process hooks
- MCP `stdio` servers

## One-Sentence Model

- The `picoclaw` main process still runs in the host environment.
- New subprocess owners construct an explicit immutable `ExecutionPolicy` from
  their effective isolation config and launch through `policy.Start` or
  `policy.Run`.
- One launch carries one detached config and, when enabled, one resolved
  instance root through validation, platform preparation, process start, and
  post-start handling.

## Architecture

The implementation has four layers:

1. Policy layer: `NewExecutionPolicy(config.IsolationConfig)` recursively copies
   the ordered exposed-path configuration into an opaque, concurrency-safe
   value.
2. Per-launch projection layer: resolves `config.GetHome()` at most once,
   validates the exact policy/platform combination, prepares instance
   directories, and derives the child environment.
3. Platform backend layer: Linux uses `bwrap`; Windows uses a restricted token, low integrity, and a `Job Object`; other platforms are not implemented.
4. Unified startup layer: `ExecutionPolicy.Start(cmd)` and
   `ExecutionPolicy.Run(cmd)` carry that same projection through pre-start and
   post-start work.

All new integrations that spawn subprocesses should retain the appropriate
policy and use these methods instead of calling `cmd.Start` or `cmd.Run`
directly.

## Explicit Policy API

Construct the policy from the already resolved effective isolation config:

```go
policy := isolation.NewExecutionPolicy(cfg.Isolation)
cmd := exec.CommandContext(ctx, command, args...)
if err := policy.Run(cmd); err != nil {
    // Handle validation, startup, or process failure.
}
```

Construction has no filesystem or process effect. It preserves the distinction
between a nil and an allocated-empty `expose_paths` slice and does not retain
caller-owned slice storage. A copied policy may be reused concurrently. Each
launch makes its own detached projection, and later mutation of the source
config cannot change it.

`ExecutionPolicy{}` is deliberately invalid and fails closed with
`ErrExecutionPolicyUnavailable`. An explicitly constructed policy with
`enabled:false` is valid, including on an otherwise unsupported platform. An
explicit policy never falls back to a process-global or default policy.

`Run` uses the exact `Start` path and then waits once. `Start` returns after all
required platform post-start work succeeds; the caller remains responsible for
waiting.

### Deprecated Global Compatibility

`Configure`, `CurrentConfig`, `Preflight`, `PrepareCommand`, package-level
`Start`, and package-level `Run` remain temporarily for source compatibility.
They are deprecated. They deep-copy config on ingress/egress and snapshot the
selected policy once per operation, but selection is still process-global and
last-writer-wins.

`PrepareCommand` alone cannot complete Windows Job Object setup and therefore
fails closed when Windows isolation is enabled. Use `ExecutionPolicy.Start` or
`ExecutionPolicy.Run`.

Existing shell/background, process-hook, stdio MCP, and CLI-provider call sites
still use this compatibility path. The follow-up propagation work will bind one
`ExecutionPolicy` to each exact runtime/config generation, pass it to every
subprocess owner, and remove agent-construction calls to `Configure`. Until
then, the explicit API is safe from mutable global policy state, but the legacy
production consumers do not yet have per-agent generation isolation.

## Configuration

Isolation lives under:

```json
{
  "isolation": {
    "enabled": false,
    "expose_paths": []
  }
}
```

Field meanings:

- `enabled`: enables or disables subprocess isolation. Default: `false`.
- `expose_paths`: explicitly exposes host paths inside the isolated environment. It only matters when `enabled=true`. This is currently supported on Linux only.

Example:

```json
{
  "isolation": {
    "enabled": true,
    "expose_paths": [
      {
        "source": "/opt/toolchains/go",
        "target": "/opt/toolchains/go",
        "mode": "ro"
      },
      {
        "source": "/data/shared-assets",
        "target": "/opt/picoclaw-instance-a/workspace/assets",
        "mode": "rw"
      }
    ]
  }
}
```

Rules for `expose_paths`:

- `source` is a host path.
- `target` is the path inside the isolated environment.
- `mode` must be `ro` or `rw`.
- `source` and the effective `target` must be absolute and contain no NUL byte.
- When `target` is empty, it defaults to `source`.
- Duplicate configured targets are rejected after path normalization.
- One configured rule may replace a built-in rule with the same target.

Platform note:

- Linux uses a real `source -> target` mount view.
- Windows does not currently support `expose_paths`.

## Instance Root And Directories

For each enabled launch, the instance root is resolved once from
`config.GetHome()`:

- If `PICOCLAW_HOME` is set, use it.
- Otherwise use the default `.picoclaw` directory under the user home.

The result must be absolute. A fallback to `.` or any other relative
`PICOCLAW_HOME` fails before directory creation or process start.

Default instance directories include:

- instance root
- `skills`
- `logs`
- `cache`
- `state`
- `runtime-user-env`

`workspace` is always the instance-local `<instance-root>/workspace`; it does
not follow an agent's separately configured workspace path.

Windows also prepares:

- `runtime-user-env/AppData/Roaming`
- `runtime-user-env/AppData/Local`

## User Environment Redirect

When isolation is enabled, child processes receive a redirected per-instance user environment.

Linux variables:

- `HOME`
- `TMPDIR`
- `XDG_CONFIG_HOME`
- `XDG_CACHE_HOME`
- `XDG_STATE_HOME`

Windows variables:

- `USERPROFILE`
- `HOME`
- `TEMP`
- `TMP`
- `APPDATA`
- `LOCALAPPDATA`

These paths point into `runtime-user-env` under the instance root. The current
ambient environment is otherwise retained, but its projection is deterministic.
On Windows, names are folded case-insensitively so ambient aliases such as
`Home` cannot override the canonical redirected `HOME`; other duplicate ambient
aliases preserve last-value semantics.

This is not the restricted child-environment boundary. The follow-up
propagation/environment change will start restricted processes from an empty
environment plus an explicit allowlist while preserving required PATH, home,
hook, MCP, and CLI-provider variables.

## Platform Behavior

### Linux

The Linux backend currently depends on `bwrap` (`bubblewrap`).

Capabilities:

- minimal filesystem view
- `ipc` namespace isolation
- redirected child-process user environment
- `source -> target` read-only or read-write mounts

Default mounts include the instance root plus minimum runtime system paths such
as `/usr`, `/bin`, `/lib`, `/lib64`, and `/etc/resolv.conf` when they exist on
the host.

At runtime, PicoClaw also adds the executable path, its directory, the effective working directory, and absolute path arguments when needed.

There is no automatic fallback when `bwrap` is missing.

Install examples:

- `apt install bubblewrap`
- `dnf install bubblewrap`
- `yum install bubblewrap`
- `pacman -S bubblewrap`
- `apk add bubblewrap`

If isolation must be disabled temporarily:

```json
{
  "isolation": {
    "enabled": false
  }
}
```

Disabling isolation increases the risk that child processes can access or modify more host files.

### Windows

Windows isolation currently supports process-level restrictions such as restricted tokens, low integrity, job objects, and redirected user-environment directories.

`expose_paths` is not currently supported on Windows. If it is configured, startup should fail instead of pretending the paths were exposed.

The Windows backend currently uses:

- a restricted primary token
- low integrity level
- a `Job Object`
- redirected child-process user environment

It does not currently implement true `source -> target` filesystem remapping.

The repository's Ubuntu CI cross-compiles the Windows package and core binary,
which proves source/build portability only. It does not execute or certify
Windows path construction, restricted-token creation, low-integrity behavior,
or Job Object assignment. Those claims require native Windows runtime evidence.

### macOS And Other Platforms

They are not implemented yet.

When isolation is explicitly enabled on an unsupported platform, the higher-level runtime should surface that as an unsupported configuration instead of pretending isolation succeeded.

## Logging And Debugging

When isolation is enabled, PicoClaw logs the generated isolation plan.

Linux log name:

- `linux isolation mount plan`

Windows log name:

- `windows isolation process constraints`

If you suspect isolation is ineffective, check whether unexpected host paths appear in those logs.

## Relationship To `restrict_to_workspace`

- `restrict_to_workspace` limits the paths an agent is normally allowed to access.
- `pkg/isolation` limits what a child process can see and where its user environment points.

They complement each other and do not replace each other.

## Current Limits

- Linux isolation is implemented with `bwrap`, not a custom in-process isolation runtime.
- Linux does not currently enable a dedicated `pid` namespace by default.
- Windows does not yet implement full host ACL enforcement for every allowed or denied path.
- macOS is not implemented.
- The current design isolates child processes, not the main `picoclaw` process.
- Ambient child-process variables remain available until the restricted
  environment follow-up lands.
- Existing production subprocess owners still select the deprecated global
  compatibility policy until per-generation propagation lands.
- Linux does not promise identical optional system mounts across distributions;
  only paths present in the fixed host view for a launch are included.

## Suggested Reading Order

If you are new to this code, read it in this order:

1. `pkg/config/config.go`
2. `pkg/isolation/policy.go`
3. `pkg/isolation/runtime.go`
4. `pkg/isolation/platform_linux.go`
5. `pkg/isolation/platform_windows.go`
6. Call sites:
7. `pkg/tools/shell.go`
8. `pkg/providers/cli/claude_cli_provider.go` and
   `pkg/providers/cli/codex_cli_provider.go`
9. `pkg/agent/hook_process.go`
10. `pkg/mcp/isolated_command_transport.go`

That path gives the fastest overview of the configuration model, runtime flow, and platform-specific limits.
