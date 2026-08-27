# `pkg/isolation`

`pkg/isolation` 为 `picoclaw` 启动的子进程提供进程级隔离能力。

它当前不会把 `picoclaw` 主进程自身放进沙箱中运行。

## 生效范围

当前生效范围是子进程启动链路：

- `exec` 工具
- 通过 `exec` 工具执行的 Cron 命令
- `claude-cli`、`codex-cli` 等 CLI provider
- 进程型 hooks
- MCP `stdio` server

仓库内其他受信任的管理型子进程不自动属于这个 agent 子进程边界；只有
显式采用 `ExecutionPolicy` 的 package 才受此约束。

## 一句话理解

- `picoclaw` 主进程仍运行在宿主环境中。
- 新的子进程所有者应从生效后的隔离配置构造显式、不可变的
  `ExecutionPolicy`，再通过 `policy.Start` 或 `policy.Run` 启动。
- 策略构造时会固定一份受限宿主环境与可执行文件查找快照。一次启动只
  携带这份快照和一份分离后的配置；开启隔离时，还只解析一次实例根，
  并让它们贯穿校验、平台准备、进程启动和启动后处理。

## 架构

当前实现可以分为四层：

1. 策略层：`NewExecutionPolicy(config.IsolationConfig)` 会递归复制有序的
   暴露路径和环境 allowlist，只捕获允许的宿主变量及私有可执行查找
   状态，形成不透明且可并发复用的值。
2. 单次启动投影层：最多解析一次 `config.GetHome()`，校验该策略与平台
   的精确组合，准备实例目录，从空基线生成受限子进程环境，并使用最终
   `PATH`/`PATHEXT` 解析可执行文件。
3. 平台后端层：Linux 使用 `bwrap`；Windows 使用受限 token、低完整性级别和 `Job Object`；其他平台未实现。
4. 统一启动层：`ExecutionPolicy.Start(cmd)` 和
   `ExecutionPolicy.Run(cmd)` 会让同一投影贯穿启动前与启动后处理。

所有新的子进程接入点都应持有所属策略并使用这些方法，而不是直接调用
`cmd.Start` 或 `cmd.Run`。

## 显式策略 API

从已经解析好的生效隔离配置构造策略：

```go
policy := isolation.NewExecutionPolicy(cfg.Isolation)
cmd := exec.CommandContext(ctx, command, args...)
if err := policy.Run(cmd); err != nil {
    // 处理校验、启动或进程失败。
}
```

构造过程不会操作文件系统或启动进程，但会有意读取一次宿主环境。它会
保留 nil 与已分配但为空的 `expose_paths`、`environment_allowlist` 切片
之间的区别，也不会持有调用方切片的底层存储。策略值可以复制并并发
复用；修改源配置、调用 `os.Setenv`、reload 或修改旧全局策略都不会
改变已经构造的策略。

`ExecutionPolicy{}` 被刻意定义为无效值，并会以
`ErrExecutionPolicyUnavailable` 失败关闭。显式构造且 `enabled:false` 的
策略是有效的，即使当前平台没有隔离后端。显式策略绝不会回退到进程
全局策略或默认策略。

`Run` 会复用完全相同的 `Start` 路径，然后只等待一次。`Start` 只有在
所有必须的平台启动后处理成功后才返回，后续等待仍由调用方负责。

### 已弃用的全局兼容路径

为保持源码兼容，`Configure`、`CurrentConfig`、`Preflight`、
`PrepareCommand`、包级 `Start` 和包级 `Run` 暂时保留，但均已弃用。
它们会在输入和输出处深拷贝配置，并在每次操作入口只快照一次所选
策略；不过策略选择仍是进程全局且后写覆盖前写。

`PrepareCommand` 本身无法完成 Windows Job Object 设置，因此在 Windows
隔离开启时会失败关闭。应使用 `ExecutionPolicy.Start` 或
`ExecutionPolicy.Run`。

生产 shell/后台进程、Cron、进程 hook、stdio MCP 和 CLI provider 已不
再走这条兼容路径。一个精确策略会在 provider/agent generation 之前
构造，与 config/registry 原子发布，并由所有所有者（包括 MCP reconnect
和 gateway rollback）持有。`NewAgentInstance` 不再调用 `Configure`。
旧全局 API 只为外部源码兼容和测试保留。

## 配置

隔离配置位于 `isolation`。下面是显式最小 allowlist 示例；它会替换而非
扩展可移植默认列表：

```json
{
  "isolation": {
    "enabled": false,
    "expose_paths": [],
    "environment_allowlist": ["PATH", "HOME", "TMPDIR", "LANG", "TERM"]
  }
}
```

字段说明：

- `enabled`：是否启用子进程隔离。默认值：`false`。
- `expose_paths`：显式把宿主路径带入隔离环境。仅在 `enabled=true` 时生效。目前只在 Linux 上支持。
- `environment_allowlist`：构造策略 generation 时从宿主环境捕获的精确
  变量名。即使 `enabled=false`，环境限制也始终生效。

字段缺失或程序传入 nil 时使用可移植兼容默认值；显式 JSON `[]` 表示不
允许任何可选宿主变量。该字段不使用 `omitempty`，所以空列表可经
save/reload 保留。名称必须符合 `[A-Za-z_][A-Za-z0-9_]*`，最多 128 个、
每个最多 128 字节，并按大小写不敏感方式保持唯一。

默认名称为：

```text
PATH
HOME TMPDIR XDG_CONFIG_HOME XDG_CACHE_HOME XDG_STATE_HOME
PATHEXT USERPROFILE HOMEDRIVE HOMEPATH TEMP TMP APPDATA LOCALAPPDATA
LANG LANGUAGE LC_ALL LC_CTYPE LC_COLLATE LC_MESSAGES
LC_MONETARY LC_NUMERIC LC_TIME
TZ TERM COLORTERM NO_COLOR
```

token/key/password、proxy、SSH/GPG/DBus socket、动态 loader 注入、定制
信任根、provider home、Git override 与语言/toolchain 注入变量均不在默认
列表。把名称加入列表就是显式授予宿主能力；Linux 隔离下，指向宿主路径
的值还可能需要匹配的 `expose_paths`。

迁移表：

| 现有依赖 | 显式替代方式 |
| --- | --- |
| Shell/Cron 命令读取普通宿主变量 | 把精确名称加入 `isolation.environment_allowlist`。 |
| 进程 hook 只需要 hook 专用值 | 优先使用 `hooks.processes.<name>.env`；只有所有目标进程都需要时才放入全局 allowlist。 |
| Stdio MCP server 需要 server 专用值 | 优先使用该 server 的 `env_file` 或 `env`；config `env` 覆盖 `env_file`。 |
| 企业 proxy 含凭证 | 有意加入精确 proxy 名称；它们绝不是默认值。 |
| 定制 CA/信任根 | 加入精确信任变量；文件系统隔离开启时同时 expose 对应宿主路径。 |
| Toolchain/runtime home 或 cache | 加入精确名称并 expose 必需路径；不要把 provider 凭证全局授予 shell/hooks/MCP。 |
| 隔离开启时的 Codex/Claude 登录目录 | 不会自动挂载；只能结合对应 provider/admission 策略显式配置。 |

reload/restart 会捕获当时允许的宿主值；已运行 generation 保留旧快照。

Go API 说明：`config.IsolationConfig` 新增了 `EnvironmentAllowlist`。外部
代码若使用位置式（unkeyed）复合字面量，需要迁移到 keyed 字面量；keyed
字面量保持源码兼容。

示例：

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

`expose_paths` 规则：

- `source`：宿主机路径。
- `target`：隔离环境内的目标路径。
- `mode`：只能是 `ro` 或 `rw`。
- `source` 和生效后的 `target` 必须是绝对路径，且不能包含 NUL 字节。
- `target` 为空时，默认等于 `source`。
- 路径规范化后，同目标的多条用户配置会被拒绝。
- 一条用户配置可以替换同目标的内置规则。

平台说明：

- Linux 会真实使用 `source -> target` 挂载视图。
- Windows 当前不支持 `expose_paths`。

## 实例根与目录

每次开启隔离的启动只从 `config.GetHome()` 解析一次实例根：

- 如果设置了 `PICOCLAW_HOME`，使用该值。
- 否则默认使用用户目录下的 `.picoclaw`。

解析结果必须是绝对路径。如果回退到 `.`，或者 `PICOCLAW_HOME` 是其他
相对路径，都会在创建目录或启动进程前失败。

默认实例目录包括：

- 实例根本身
- `skills`
- `logs`
- `cache`
- `state`
- `runtime-user-env`

`workspace` 始终是实例本地的 `<instance-root>/workspace`；它不会跟随
agent 单独配置的 workspace 路径。

Windows 还会额外准备：

- `runtime-user-env/AppData/Roaming`
- `runtime-user-env/AppData/Local`

## 受限子进程环境

每个目标子进程（包括 `enabled=false`）都从空继承环境开始。先加入策略
构造时捕获的 allowlist 值，再覆盖受信任所有者显式配置的值：进程 hook
`env`，或 MCP `env_file` 后接 MCP config `env`。`PWD` 来自生效工作目录，
最终输出分离且有序。

最终环境最多 256 项；名称最多 128 字节、值最多 16 KiB，总编码大小
最多 24 KiB。名称和值必须有效且不含 NUL；显式空值会保留；错误不会
包含变量值。

文件系统隔离开启时，实例级用户目录重定向最后应用并具有最高优先级。

Linux 注入变量：

- `HOME`
- `TMPDIR`
- `XDG_CONFIG_HOME`
- `XDG_CACHE_HOME`
- `XDG_STATE_HOME`

Windows 注入变量：

- `USERPROFILE`
- `HOME`
- `HOMEDRIVE`
- `HOMEPATH`
- `TEMP`
- `TMP`
- `APPDATA`
- `LOCALAPPDATA`

这些路径都会指向实例根下的 `runtime-user-env`。Windows 变量名按大小写
不敏感方式合并；策略始终显式提供固定的 `SYSTEMROOT`，以及一致的
`WINDIR`、`SYSTEMDRIVE`、`COMSPEC`，避免 Go 从之后的父进程环境静默
补入值。所有者显式变量也不能覆盖这些系统值。策略还会强制
`NoDefaultCurrentDirectoryInExePath=1`，避免 Go/Windows 后代在 `PATH`
之前搜索工作目录。

裸命令会在 `ExecutionPolicy.Start`/`Run` 内再次使用最终子进程 `PATH`
和 Windows `PATHEXT` 解析。空或相对搜索目录会被忽略，绝不隐式搜索
当前目录。解析出的绝对路径在本次启动中固定，子进程也得到相同 PATH，
供 shebang 和后代使用。Linux `bwrap` 使用另一份私有宿主 PATH 快照，
不会被子进程专用 PATH 覆盖。

环境值在 generation 构造时捕获；之后修改宿主环境要到新 generation/
restart 才生效。Gateway A-to-B-to-A rollback 会恢复原始 A 快照，而不是
重新构造 A。

## 平台行为

### Linux

Linux 后端当前依赖 `bwrap`（`bubblewrap`）。

能力：

- 最小文件系统视图
- `ipc namespace`
- 子进程用户环境重定向
- `source -> target` 只读或读写挂载

默认映射包括实例根，以及宿主机上确实存在的 `/usr`、`/bin`、`/lib`、
`/lib64`、`/etc/resolv.conf` 等最小运行时系统路径。

运行时还会按需补充可执行文件本身、其所在目录、生效后的工作目录，以及命令行中的绝对路径参数。

子进程可执行文件和 `bwrap` 均使用策略持有的固定查找状态；之后修改父
进程 PATH 无法换成另一个二进制。

缺少 `bwrap` 时不会自动回退。

安装示例：

- `apt install bubblewrap`
- `dnf install bubblewrap`
- `yum install bubblewrap`
- `pacman -S bubblewrap`
- `apk add bubblewrap`

如果需要临时关闭隔离：

```json
{
  "isolation": {
    "enabled": false
  }
}
```

关闭隔离后，子进程访问或修改更多宿主文件的风险会明显上升。

### Windows

Windows 隔离当前提供的是进程级限制，例如 restricted token、low integrity、job object，以及用户环境目录重定向。

`expose_paths` 目前不支持 Windows。如果配置了该字段，启动应直接失败，而不是假装这些路径已经被暴露进隔离环境。

Windows 后端当前使用：

- 受限 primary token
- 低完整性级别
- `Job Object`
- 子进程用户环境重定向
- 不搜索当前目录的策略级 `PATH`/`PATHEXT` 可执行解析
- 显式固定的 `SYSTEMROOT`

它当前不会实现真正的 `source -> target` 文件系统重映射。

仓库的 Ubuntu CI 会交叉编译 Windows package 和核心二进制；这只能证明
源码和构建可移植性。它不会运行或认证 Windows 路径构造、受限 token、
低完整性级别或 Job Object 分配。这些声明需要原生 Windows 运行证据。

### macOS 与其他平台

当前尚未实现。

当在未支持的平台上显式开启隔离时，上层运行时应将其视为不支持的配置，而不是假装隔离成功。

## 日志与排障

隔离开启后，PicoClaw 会打印生成后的隔离计划，便于排障。

Linux 日志名：

- `linux isolation mount plan`

Windows 日志名：

- `windows isolation process constraints`

如果你怀疑隔离未生效，先检查这些日志里是否出现了不应暴露的宿主路径。

## 与 `restrict_to_workspace` 的关系

- `restrict_to_workspace` 限制的是 agent 默认可访问的路径。
- `pkg/isolation` 限制的是子进程运行时能看到什么文件系统，以及它的用户环境指向哪里。

两者互补，不互相替代。

## 当前限制

- Linux 基于 `bwrap` 实现，而不是纯内建 isolation runtime。
- Linux 当前没有默认启用独立的 `pid namespace`。
- Windows 还没有对所有允许/拒绝路径做完整 ACL 落地。
- macOS 尚未实现。
- 当前隔离的是子进程，不是 `picoclaw` 主进程自身。
- 受限环境只覆盖上面列出的 agent 子进程，不自动覆盖仓库中所有受信任
  管理型 `exec.Command`。
- proxy、定制 CA、provider home 和 toolchain 变量必须显式加入 allowlist；
  这可能要求现有 MCP、hook、shell 或 CLI 配置迁移。
- 手工进程 hook 在 reload 后保留启动时策略；已启动的后台进程同样保留
  启动时固定的环境与沙箱。
- 可执行查找不会受之后环境修改影响；但在最终校验与 kernel 打开进程
  之间，外部对已允许文件系统项的恶意删除/替换仍不属于当前 path-based
  launcher 合约。
- Linux 不承诺不同发行版使用完全相同的可选系统挂载；一次启动只会
  包含其固定宿主视图中确实存在的路径。

## 建议阅读顺序

如果你是第一次看这部分代码，建议按这个顺序阅读：

1. `pkg/config/config.go`
2. `pkg/isolation/policy.go`
3. `pkg/isolation/runtime.go`
4. `pkg/isolation/platform_linux.go`
5. `pkg/isolation/platform_windows.go`
6. 调用点：
7. `pkg/tools/shell.go`
8. `pkg/agent/agent_init.go`、`pkg/agent/agent.go` 和
   `pkg/agent/agent_mcp.go`
9. `pkg/providers/cli/claude_cli_provider.go` 和
   `pkg/providers/cli/codex_cli_provider.go`
10. `pkg/agent/hook_process.go`
11. `pkg/mcp/isolated_command_transport.go`

这样能最快建立对配置模型、运行流程和平台边界的整体理解。
