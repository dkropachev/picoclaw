# `pkg/isolation`

`pkg/isolation` provides process-level isolation for child processes started by `picoclaw`.

It does not sandbox the main `picoclaw` process itself.

## Scope

The current scope is the child-process startup path:

- `exec` tool
- Cron commands executed through the `exec` tool
- CLI providers such as `claude-cli` and `codex-cli`
- process hooks
- MCP `stdio` servers

Other trusted administrative subprocesses in the repository are outside this
targeted agent-owned boundary unless their package explicitly adopts an
`ExecutionPolicy`.

## One-Sentence Model

- The `picoclaw` main process still runs in the host environment.
- New subprocess owners construct an explicit immutable `ExecutionPolicy` from
  their effective isolation config and launch through `policy.Start` or
  `policy.Run`.
- Construction captures one restricted host-environment and executable-lookup
  snapshot. One launch carries that snapshot, one detached config and, when
  enabled, one resolved instance root through validation, platform preparation,
  process start, and post-start handling.

## Architecture

The implementation has four layers:

1. Policy layer: `NewExecutionPolicy(config.IsolationConfig)` recursively copies
   the ordered exposed-path/environment-allowlist configuration and captures
   only admitted host variables plus private executable lookup state into an
   opaque, concurrency-safe value.
2. Per-launch projection layer: resolves `config.GetHome()` at most once,
   validates the exact policy/platform combination, prepares instance
   directories, derives an empty-base restricted child environment, and resolves
   the executable against the exact final `PATH`/`PATHEXT`.
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

Construction has no filesystem or process effect, but intentionally reads the
host environment once. It preserves the distinction between nil and
allocated-empty `expose_paths` and `environment_allowlist` slices and does not
retain caller-owned slice storage. A copied policy may be reused concurrently.
Each launch makes its own detached projection; later source-config mutation,
`os.Setenv`, reload, or deprecated-global mutation cannot change it.

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

Production shell/background, Cron, process-hook, stdio MCP, and CLI-provider
owners do not use this compatibility path. One exact policy is constructed
before each provider/agent generation, published atomically with its config and
registry, and retained by every owner, including MCP reconnects and gateway
rollback. `NewAgentInstance` never calls `Configure`. The deprecated globals
remain only for external source compatibility and tests.

## Configuration

Isolation lives under `isolation`. This minimal example uses an explicit
allowlist; it replaces, rather than extends, the portable defaults:

```json
{
  "isolation": {
    "enabled": false,
    "expose_paths": [],
    "environment_allowlist": ["PATH", "HOME", "TMPDIR", "LANG", "TERM"]
  }
}
```

Field meanings:

- `enabled`: enables or disables subprocess isolation. Default: `false`.
- `expose_paths`: explicitly exposes host paths inside the isolated environment. It only matters when `enabled=true`. This is currently supported on Linux only.
- `environment_allowlist`: exact host environment names captured when the
  policy generation is constructed. Environment restriction applies even when
  `enabled=false`.

An omitted or programmatic nil `environment_allowlist` uses portable
compatibility defaults. An explicit JSON `[]` admits no optional ambient
variables. The field is persisted without `omitempty`, so empty remains
different from omitted/default. Names use portable
`[A-Za-z_][A-Za-z0-9_]*` syntax, are capped at 128 entries/128 bytes, and must be
unique case-insensitively so a Unix config remains unambiguous on Windows.

Portable defaults are:

```text
PATH
HOME TMPDIR XDG_CONFIG_HOME XDG_CACHE_HOME XDG_STATE_HOME
PATHEXT USERPROFILE HOMEDRIVE HOMEPATH TEMP TMP APPDATA LOCALAPPDATA
LANG LANGUAGE LC_ALL LC_CTYPE LC_COLLATE LC_MESSAGES
LC_MONETARY LC_NUMERIC LC_TIME
TZ TERM COLORTERM NO_COLOR
```

Credential/token/key/password variables, proxy variables, SSH/GPG/DBus
sockets, loader injection, custom trust roots, provider-specific homes, Git
overrides, and language/toolchain injection variables are not defaults. Adding
one name is an explicit host-capability grant. With Linux isolation, a value
that names a host path may also require a matching `expose_paths` rule.

Migration guide:

| Existing dependency | Explicit replacement |
| --- | --- |
| Shell/Cron command reads an ordinary host variable | Add its exact name to `isolation.environment_allowlist`. |
| Process hook needs a hook-only value | Prefer `hooks.processes.<name>.env`; use the global allowlist only when every targeted process should receive it. |
| Stdio MCP server needs a server-only value | Prefer that server's `env_file` or `env`; config `env` overrides `env_file`. |
| Enterprise proxy contains credentials | Opt in the exact proxy names deliberately; they are never defaults. |
| Custom CA/trust root | Opt in the exact trust variable and expose its host path when filesystem isolation is enabled. |
| Toolchain/runtime home or cache | Opt in the exact name and expose required paths; avoid granting provider credentials to shell/hooks/MCP globally. |
| Codex/Claude login directory under enabled isolation | Do not auto-mount it. Configure deliberate paths only with the corresponding provider/admission policy. |

Reload/restart captures current admitted host values. Running generations keep
their old snapshot.

Go API note: `config.IsolationConfig` gained `EnvironmentAllowlist`. External
code using positional (unkeyed) composite literals must migrate to keyed
literals; keyed literals remain source-compatible.

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

## Restricted Child Environment

Every targeted launch, including `enabled=false`, starts from an empty inherited
environment. Policy-captured allowlisted values are added first. Trusted
owner-specific values are then overlaid: process-hook `env`, or MCP `env_file`
followed by MCP config `env`. `PWD` is computed from the effective command
directory. Output is detached and sorted.

The maximum final environment is 256 entries, 128 bytes per name, 16 KiB per
value, and 24 KiB encoded. Names and values must be valid and NUL-free. Empty
explicit values are preserved. Error messages never include values.

When filesystem isolation is enabled, per-instance redirects are authoritative
and applied last.

Linux variables:

- `HOME`
- `TMPDIR`
- `XDG_CONFIG_HOME`
- `XDG_CACHE_HOME`
- `XDG_STATE_HOME`

Windows variables:

- `USERPROFILE`
- `HOME`
- `HOMEDRIVE`
- `HOMEPATH`
- `TEMP`
- `TMP`
- `APPDATA`
- `LOCALAPPDATA`

These paths point into `runtime-user-env` under the instance root. Windows names
are folded case-insensitively. The policy always supplies its frozen
`SYSTEMROOT` (and coherent `WINDIR`, `SYSTEMDRIVE`, and `COMSPEC`) so Go cannot
silently inject a later live-parent value; explicit owner values cannot
override them. `NoDefaultCurrentDirectoryInExePath=1` is also authoritative so
Go/Windows descendants do not search the working directory before `PATH`.

Bare executable lookup is repeated inside `ExecutionPolicy.Start`/`Run` using
the final child `PATH` and Windows `PATHEXT`. Empty/relative search directories
are ignored; current-directory lookup is never implicit. The resolved absolute
path is frozen for that launch, and the child receives the same PATH for
shebangs and descendants. Linux `bwrap` instead resolves from a private host
PATH snapshot that child-specific overrides cannot replace.

Environment values are captured at generation construction. Changing the host
environment later has no effect until a new runtime generation/restart. Gateway
A-to-B-to-A rollback restores the original A snapshot rather than rebuilding it.

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

Both the child executable and `bwrap` are resolved against policy-owned frozen
lookup state; a later parent PATH change cannot select another binary.

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
- policy-owned `PATH`/`PATHEXT` resolution without current-directory search
- an explicit frozen `SYSTEMROOT` environment value

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
- The restricted environment covers the targeted agent-owned subprocess list,
  not every trusted administrative `exec.Command` elsewhere in the repository.
- Proxy, custom CA, provider-home, and toolchain variables require explicit
  allowlist grants; this can be a compatibility change for existing MCP, hook,
  shell, or CLI setups.
- A manual process hook retains its launch policy across reload. Existing
  background processes likewise retain the environment/sandbox fixed at start.
- Executable lookup is frozen against environment mutation, but an adversarial
  external delete/replace of the admitted filesystem entry between final
  validation and the kernel process-open remains outside this path-based
  launcher contract.
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
8. `pkg/agent/agent_init.go`, `pkg/agent/agent.go`, and
   `pkg/agent/agent_mcp.go`
9. `pkg/providers/cli/claude_cli_provider.go` and
   `pkg/providers/cli/codex_cli_provider.go`
10. `pkg/agent/hook_process.go`
11. `pkg/mcp/isolated_command_transport.go`

That path gives the fastest overview of the configuration model, runtime flow, and platform-specific limits.
