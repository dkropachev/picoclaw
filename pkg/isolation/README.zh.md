# `pkg/isolation`

`pkg/isolation` 为 `picoclaw` 启动的子进程提供进程级隔离能力。

它当前不会把 `picoclaw` 主进程自身放进沙箱中运行。

## 生效范围

当前生效范围是子进程启动链路：

- `exec` 工具
- `claude-cli`、`codex-cli` 等 CLI provider
- 进程型 hooks
- MCP `stdio` server

## 一句话理解

- `picoclaw` 主进程仍运行在宿主环境中。
- 新的子进程所有者应从生效后的隔离配置构造显式、不可变的
  `ExecutionPolicy`，再通过 `policy.Start` 或 `policy.Run` 启动。
- 一次启动只携带一份分离后的配置；开启隔离时，还只解析一次实例根，
  并让它们贯穿校验、平台准备、进程启动和启动后处理。

## 架构

当前实现可以分为四层：

1. 策略层：`NewExecutionPolicy(config.IsolationConfig)` 会递归复制有序的
   暴露路径配置，形成不透明且可并发复用的值。
2. 单次启动投影层：最多解析一次 `config.GetHome()`，校验该策略与平台
   的精确组合，准备实例目录，并生成子进程环境。
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

构造过程不会操作文件系统或启动进程。它会保留 nil 与已分配但为空的
`expose_paths` 切片之间的区别，也不会持有调用方切片的底层存储。
策略值可以复制并并发复用；每次启动都会再生成自己的分离投影，因此
构造后修改源配置不会改变策略。

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

现有 shell/后台进程、进程 hook、stdio MCP 和 CLI provider 调用点仍走
这条兼容路径。后续传播改动会把一个 `ExecutionPolicy` 绑定到精确的
运行时/配置 generation，把它传给每个子进程所有者，并移除 agent 构造
期间对 `Configure` 的调用。在那之前，显式 API 已不受可变全局策略
影响，但旧的生产调用点还没有按 agent generation 隔离。

## 配置

隔离配置位于：

```json
{
  "isolation": {
    "enabled": false,
    "expose_paths": []
  }
}
```

字段说明：

- `enabled`：是否启用子进程隔离。默认值：`false`。
- `expose_paths`：显式把宿主路径带入隔离环境。仅在 `enabled=true` 时生效。目前只在 Linux 上支持。

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

## 用户环境重定向

隔离开启后，子进程会收到重定向到实例目录下的独立用户环境。

Linux 注入变量：

- `HOME`
- `TMPDIR`
- `XDG_CONFIG_HOME`
- `XDG_CACHE_HOME`
- `XDG_STATE_HOME`

Windows 注入变量：

- `USERPROFILE`
- `HOME`
- `TEMP`
- `TMP`
- `APPDATA`
- `LOCALAPPDATA`

这些路径都会指向实例根下的 `runtime-user-env`。当前仍会保留其他宿主
环境变量，但投影顺序是确定的。Windows 上会按大小写不敏感方式合并
变量名，因此环境中的 `Home` 等别名无法覆盖规范的重定向 `HOME`；
其他重复环境别名仍保留最后一个值的语义。

这还不是受限子进程环境边界。后续传播/环境改动会让受限进程从空环境
加显式 allowlist 开始，同时保留所需的 PATH、home、hook、MCP 和 CLI
provider 变量。

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
- 在受限环境后续改动完成前，子进程仍可获得其他宿主环境变量。
- 在按 generation 传播策略完成前，现有生产子进程所有者仍选择已弃用
  的全局兼容策略。
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
8. `pkg/providers/cli/claude_cli_provider.go` 和
   `pkg/providers/cli/codex_cli_provider.go`
9. `pkg/agent/hook_process.go`
10. `pkg/mcp/isolated_command_transport.go`

这样能最快建立对配置模型、运行流程和平台边界的整体理解。
