# Session 系统

> 返回 [README](../README.md)

本文说明 PicoClaw 运行时的 Session 系统如何完成以下事情：

- 把入站消息映射到稳定的会话作用域
- 持久化消息历史与摘要
- 在运行时使用不透明 canonical key 的同时，继续兼容旧的 `agent:...` session key
- 严格读取完整 session snapshot，并在后端支持时用 CAS 原子替换它

本文覆盖 `pkg/session`、`pkg/memory` 和 `pkg/agent` 中的核心运行时链路。
它不讨论 `web/backend/middleware` 中 launcher 登录 Cookie 或 dashboard 鉴权 session。

## 职责

Session 系统承担五件事：

1. 决定哪些消息应该共享同一段上下文。
2. 让这段上下文能跨 turn、跨进程重启持久存在。
3. 向 agent loop 暴露一个足够小的 `SessionStore` 抽象。
4. 在存储层和路由层迁移期间继续兼容旧 session key。
5. 让自动化 workflow 能检查某个精确 session revision，并在后端支持时
   原子替换它，避免发布 history 与 metadata 相互撕裂的状态。

## 主要组件

| 层次 | 文件 | 作用 |
| --- | --- | --- |
| Session 抽象 | `pkg/session/session_store.go` | 定义 agent loop 依赖的 `SessionStore`，以及可选的严格 `SnapshotReader` 与原子 `SnapshotReplacer` 能力。 |
| 旧后端 | `pkg/session/manager.go` | 每个 session 一个 JSON 文件的旧实现，仍作为回退方案保留。 |
| Session 适配层 | `pkg/session/jsonl_backend.go` | 把 `pkg/memory.Store` 适配成 `SessionStore`，支持 alias/scope metadata、严格 snapshot 读取与可选的底层替换能力。 |
| 持久化存储 | `pkg/memory/jsonl.go` | 以追加为主的 JSONL 存储、`.meta.json` 元数据侧文件，以及用于 crash-consistent tuple 替换的有界 `a`/`b` history slot。 |
| Scope / Key 构建 | `pkg/session/scope.go`、`pkg/session/key.go`、`pkg/session/allocator.go` | 从路由结果生成结构化 scope、不透明 canonical key 和 legacy alias。 |
| 运行时集成 | `pkg/agent/instance.go`、`pkg/agent/loop.go`、`pkg/agent/loop_message.go` | 初始化存储、分配 session scope，并在 turn 执行前落 metadata。 |

## Session 数据模型

结构化的会话身份由 `session.SessionScope` 表示：

| 字段 | 含义 |
| --- | --- |
| `Version` | Scope 模式版本，当前为 `ScopeVersionV1`。 |
| `AgentID` | 处理该 turn 的路由 agent。 |
| `Channel` | 归一化后的入站 channel 名称。 |
| `Account` | 归一化后的 bot / account 标识。 |
| `Dimensions` | 当前启用的隔离维度顺序，例如 `chat` 或 `sender`。 |
| `Values` | 每个维度对应的具体归一化值。 |

Allocator 当前只识别四个维度：

- `space`
- `chat`
- `topic`
- `sender`

默认配置是：

```json
{
  "session": {
    "dimensions": ["chat"]
  }
}
```

也就是默认按 chat 共享上下文；如果 dispatch rule 覆盖了维度，则以 rule 为准。

## Canonical Key 与 Legacy Alias

运行时现在优先使用不透明 canonical key：

```text
sk_v1_<sha256>
```

它由 `pkg/session/key.go` 中的 scope signature 计算得到。
这样可以让存储 key 稳定，同时不再把持久化格式和某一种旧文本 key 绑定死。

为了兼容旧数据，allocator 还会生成 legacy alias，例如：

```text
agent:main:direct:user123
agent:main:slack:channel:c001
agent:main:pico:direct:pico:session-123
```

这些 alias 很重要，因为旧 session、部分测试以及某些工具仍然会引用这种格式。
JSONL store 会解析 alias，并持续持有 directory lock 直到 canonical 读写结束，
因此 alias ownership 无法在解析与访问之间改变。

此外，如果调用方已经显式传入了受支持的 session key，agent loop 会保留它，不强行改成新分配的 routed key。
这条逻辑在 `pkg/agent/loop_utils.go:resolveScopeKey` 中：

- 不透明 canonical key
- legacy `agent:...` key

都属于“显式 key”。

## 分配流程

普通入站消息的完整链路如下：

```text
InboundMessage
  -> RouteResolver.ResolveRoute(...)
  -> session.AllocateRouteSession(...)
  -> resolveScopeKey(...)
  -> ensureSessionMetadata(...)
  -> AgentLoop turn 执行
  -> SessionStore 读写
```

具体来说：

1. `pkg/agent/loop_message.go` 先用归一化后的 inbound context 解析 agent route。
2. `session.AllocateRouteSession` 把 route 的 `SessionPolicy` 和 inbound context 组合成结构化 `SessionScope`。
3. Allocator 会生成：
   - `SessionKey`：当前路由会话的 canonical key
   - `SessionAliases`：该路由会话的兼容 alias
   - `MainSessionKey`：agent 级主会话 key
   - `MainAliases`：主会话对应的 legacy alias
4. `runAgentLoop` 通过 `ensureSessionMetadata` 持久化 scope metadata 和 alias。
5. 后续读写时，`JSONLBackend.ResolveSessionKey` 会先把 alias 映射回 canonical key。

`MainSessionKey` 和普通聊天会话是分开的。
它主要服务于 agent 级、系统级的上下文场景，比如 `processSystemMessage`。

## Scope 构建规则

`pkg/session/allocator.go` 会从归一化后的 inbound context 生成 scope 值。
关键规则如下：

- `space` 变成 `<space_type>:<space_id>`
- `chat` 变成 `<chat_type>:<chat_id>`
- `topic` 变成 `topic:<topic_id>`
- `sender` 会先经过 `session.identity_links` 归一化再写入

其中有两个需要单独记住的特殊规则。

### Telegram forum 隔离

Telegram forum topic 必须默认保持隔离，即使配置只写了 `chat` 维度。
为此，如果消息来自 Telegram forum 且策略里没有显式包含 `topic`，allocator 会把 `/<topic_id>` 拼到 `chat` 值后面。

例如：

```text
group:-1001234567890/42
group:-1001234567890/99
```

这两者会得到不同的 session key。

### Identity links

`session.identity_links` 可以把多个 sender 标识折叠为一个 canonical identity。
dispatch 匹配和 session 分配都会使用这套映射，因此同一个人即使跨 channel 或 account 使用不同原始 sender ID，也可以继续落到同一段上下文里。

## 存储格式

默认运行时后端是 `pkg/memory.JSONLStore`，外面包了一层 `session.JSONLBackend`。

每个 session 都有 metadata 与一份被选中的 history。已有 session 继续使用
legacy history 文件；支持替换的写入会在两个有界 slot 之间轮换：

```text
{sanitized_key}.jsonl       # legacy history；HistorySlot 为空时选中
{sanitized_key}.history-a   # 有界替换 slot a
{sanitized_key}.history-b   # 有界替换 slot b
{sanitized_key}.meta.json   # metadata 与 active-history selector
```

这些文件保存：

- 被选中的 history 文件：一行一个 `providers.Message`
- `.meta.json`：摘要、时间戳、行数、逻辑截断偏移、scope、aliases、
  thread metadata 与 `HistorySlot`

`HistorySlot` 是 commit selector：

- 空值只选择 legacy `.jsonl`
- `a` 只选择 `.history-a`
- `b` 只选择 `.history-b`

未被选中的 slot 是 inactive，不得影响读取。selector 非法，或非空 selector
指定的文件不存在，都属于损坏并 fail closed；读取方不得回退到 legacy 或
inactive history。没有对应 metadata 的 slot 文件是不完整 orphan，不是可发现
session。

与 session 相关的 `SessionMeta` 字段包括：

- `Key`
- `Summary`
- `Skip`
- `Count`
- `CreatedAt`
- `UpdatedAt`
- `Scope`
- `Aliases`
- `HistorySlot`

严格 snapshot 还会返回 `Aliases` 与不透明 `Revision`。revision 由 canonical
key、精确可见 history 与已提交 metadata 计算得到；它是瞬态字段
（`json:"-"`），不会写入 sidecar。`HistorySlot` 是新增的可选 metadata 字段，
空值保留旧布局，因此此能力不提升 `ScopeVersionV1`，也不要求存储迁移。

## 写入与崩溃语义

普通 turn 存储仍以“追加优先、宁可暂时读到旧数据也不要丢数据”为核心：

- `AddMessage` / `AddFullMessage` 先验证编码后的单行不超过共享 scanner
  上限，再追加 JSON、执行 `fsync`，最后更新 metadata。
- `TruncateHistory` 先做逻辑截断，本质上只是推进 `meta.Skip`。
- `SetHistory` 与 `Compact` 会把完整 history 写入并同步到 inactive
  `a`/`b` slot，再原子替换 metadata，让它选择该 slot。
- 读取 JSONL 时如果碰到损坏行，会跳过该行，而不是让整个 session 读取失败。

严格 snapshot 读取有意不同于普通恢复：它要求 metadata 与每个 retained
record 都有效，把 alias 解析到唯一 canonical key，并在 canonical session lock
内读取 metadata 及其选中的 history。因此返回的 `Revision` 是某个精确
point-in-time tuple 的 compare-and-swap token。严格 metadata 要求 `Skip` 与
`Count` 非负且 `Skip <= Count`；物理非空记录数可以因为 append-first crash
recovery 而大于 `Count`，但不能小于它。只有 `Skip=Count=0` 时，缺失的 legacy
history 才表示合法的空 tuple。

`SnapshotReplacer.ReplaceSessionSnapshot` 把可见 history、summary、scope 与
aliases 作为一次 optimistic transaction 替换：

1. 校验精确 opaque key/scope 绑定、当前 scope version、canonical
   owner/channel/account、唯一且规范化的 dimensions、与 dimensions 精确对应且
   没有额外语义字段的 values、canonical aliases，以及 message 是否可持久化。
2. 在共享 directory lock 与 canonical-session lock 内要求当前 revision 精确
   匹配；expected revision 为空表示要求 session 精确不存在。
3. 把完整新 history 写入 inactive `a`/`b` slot 并 `fsync`。
4. 发布之前检查 cancellation。
5. 原子 rename 新 `.meta.json`，让新 tuple 与 `HistorySlot` 一起生效。该 rename
   是唯一 visibility/commit point，之后再同步 directory。
6. 验证所有已提交 alias 的 ownership。新加入的 alias 必须唯一解析到该
   canonical key；原 tuple 中不变的 legacy shared fallback alias（包括 main
   fallback）可以继续保持
   strict-ambiguous，保留的 promoted direct shadow 则使用 owner-aware 解析验证，
   replacement 不能新引入这两种例外。

在第 5 步之前，旧 metadata 仍选择旧 history，因此并发 coherent reader 只会
看到完整旧 tuple；rename 之后只会看到完整新 tuple。stale revision、校验失败、
history 写入失败、rename 前的 metadata 写入失败，或第 4 步观察到
cancellation，都会让旧 tuple 保持可见。第 5 步开始后，稍后到达的 cancellation
不会中断或撤销发布。Metadata rename 之后的任何 error，包括 directory sync、
alias 校验期间的 cancellation 或 alias 校验本身失败，都属于
commit-uncertain：方法可能返回 error，而新 tuple 已经可见，调用方必须先做
一次严格重读再决定是否重试。

`JSONLBackend.Save` 对应到底层的 `store.Compact(...)`。
也就是说，`Save` 在新实现里不再是“把内存脏数据刷盘”，而是“在逻辑截断后回收无效行占用的磁盘空间”。

## 并发模型

`pkg/memory.JSONLStore` 使用进程级共享、固定 64 分片的 lock 数组。Session
lock 对清理后的绝对存储目录与 canonical key 一起做 hash；directory RW lock
对存储目录做 hash。因此，同一进程里分别构造、但指向同一目录的 store 也会
协调，同时不需要无界 lock map。

Session lock 让 metadata selector 及其选中 history 成为一个 coherent 读写
单元。Directory write lock 串行化 alias catalog 扫描、whole-session replacement
与相邻模块的协调 metadata 更新；普通 append、summary、truncate、完整 history、
compact 与 ensure-history 操作从 tolerant alias 解析开始，到 canonical session
访问结束为止持续持有 directory read lock，并在其内取得 session lock。因此并发
replacement 无法在解析与实际读写之间移除或重绑 alias。相邻 metadata callback
在 directory write lock 内完成解析；若 callback 没有修改 aliases，则会原样保留
其 legacy 形状。Web 与 thread 消费者使用同一组 store helper，所以同一进程中的另一个
store instance 不会用 stale metadata 覆盖 slot flip。
`UpdateSessionMeta` 还会拒绝修改 canonical key 或 history-owned selector/count
字段。

这些是 shared-process lock，不是 filesystem lock 或 cross-process lock。本设计
不承诺多个 PicoClaw 进程同时写同一目录时的 transaction isolation。

分组删除具备 crash recovery，而不只是进程内原子性。实现会先原子写入并同步一个
列出精确 canonical/shadow keys 的 manifest，再持久删除每个成员的 metadata、
legacy body 与两个 bounded slots，最后持久删除 manifest。`NewJSONLStore` 会在
返回可用 store 之前，于共享 directory write lock 内完成任何有效的未完成
manifest；无效 manifest 会 fail closed。等待 lock 期间若 context 已取消，则不会
提交 manifest；manifest 一旦持久化，cleanup 会忽略之后的 cancellation 并继续。
若 atomic manifest replacement 已让精确 intent 可见、却在 directory sync 时报错，
删除仍会同步完成，而不会返回错误的“尚未提交”结果并在 reopen 时误删之后创建的
session generation。若后续 cleanup 仍失败，该精确目录会在进程范围保持
recovery-pending；所有已打开 store 的 ordinary、strict、metadata 与 CAS 访问都会
fail closed，直到 constructor 或 delete recovery 在同一把 directory lock 下成功并
清除标记。按 identity 分组删除会在同一把 lock 下扫描完整的当前 metadata
catalog、重新验证所有匹配 owner，并选择兼容 shadow；metadata-less filename candidate
只有在 metadata 仍不存在时才可删除，alias 也不会删除不匹配的 metadata-backed 资源。

旧的 `SessionManager` 则是一个内存 map 加 RW mutex。

这两个实现都满足同一个 `SessionStore` 接口，所以 agent loop 不需要写任何存储后端特化逻辑。严格 snapshot 读取与替换是另外的可选能力。

## 兼容与迁移

`pkg/agent/instance.go:initSessionStore` 会优先初始化 JSONL 后端。

启动过程如下：

1. 创建 `memory.NewJSONLStore(dir)`。
2. 执行 `memory.MigrateFromJSON(...)`，把旧 `.json` session 迁入新格式。
3. 用 `session.NewJSONLBackend(store)` 包装。
4. 如果 JSONL 初始化或迁移失败，则回退到 `session.NewSessionManager(dir)`。

这个回退是刻意设计的：做一半的迁移，比整轮继续使用旧后端更危险。
迁移会跳过 `.meta.json` sidecar，因此不会把 metadata 当成空 legacy session
重新导入，也不会覆盖 legacy-selected 或 slotted history。

### History slot 兼容

Legacy metadata 没有 `HistorySlot`，所以它的零值仍选择已有 `.jsonl` 文件。
第一次完整 history 重写或原子 snapshot replacement 会写 `.history-a`，再提交
选择 `a` 的 metadata；后续重写在 `a` 与 `b` 之间交替。系统只使用两个
replacement slot；inactive/legacy 文件可以留在磁盘上，但不会因此变为可见。
这是 additive compatibility mechanism，不是 scope schema 或 version 变更。

### Alias 提升

第一次为 canonical key 建 metadata 时，`EnsureSessionMetadata` 会尝试把某个非空 legacy alias 的历史提升到 canonical session。
但这件事只会在 canonical session 仍然为空时发生，因此不会覆盖已经存在的 canonical 历史。
提升只读取 alias metadata 选中的 history，并通过同一套 inactive-slot / metadata
flip 协议提交 canonical 副本；stale legacy 或 inactive 文件会被忽略。为兼容性，
direct legacy 文件仍然保留，因此 launcher 与 thread discovery 会先解析 metadata
owner，并在同一个 directory/session-locked 操作中投影 history。结构化 non-Pico
channel 具有权威性，即使 sender 或 alias 看起来像 Pico 也不会回退分类。Launcher
删除会在 lock 内重新验证并选择投影到请求 Pico ID 的所有当前 canonical owner，
再连同 owned retained shadows 通过一个 durable grouped-delete manifest 一起删除，
避免第二个 owner、shadow、竞态或 crash 后旧 session 再次出现。

这保证了系统在迁移到 opaque key 的同时，仍能保留旧历史，例如：

- 旧的 direct-message key
- 旧的 Pico direct-session key

## 其他 SessionStore 实现

`pkg/agent/subturn.go` 里定义了 `ephemeralSessionStore`。
它同样实现 `SessionStore`，但只存在于内存里，在 sub-turn 结束时销毁。

这样 SubTurn 就能复用相同的 session 接口，而不会把子任务历史写进父会话的持久存储。

`SnapshotReplacer` 是可选能力。Legacy `SessionManager` 与 ephemeral store 不
提供原子 whole-session replacement。`JSONLBackend` 只在底层 memory store
支持时委托替换，否则返回 `ErrSnapshotUnsupported`。调用方必须 fail closed；
把 legacy `SetHistory`、`SetSummary` 或 metadata setter 串起来会暴露 torn
tuple，不能作为回退方案。

## 运行时消费者

Session 系统不只被 agent loop 使用：

- `web/backend/api/session.go` 通过一次 alias-aware `ReadSessionState`/metadata
  访问，让 list/detail 把 canonical metadata 与它选中的 legacy/`a`/`b` history
  作为一个 tolerant tuple 投影，并在暴露 history 前从该精确 tuple 返回的 metadata
  重新验证 ID，关闭 lookup/read rebind 竞态；delete 在同一个 directory write lock
  内从完整当前 catalog 选择并删除该投影 ID 的所有 owner 及其兼容 retained Pico
  shadows。
- `pkg/threads/registry.go` 和 `pkg/threads/threads.go` 使用同一个 coherent
  owner-aware projection 生成 preview/count，并以选中 history 的时间作为
  timestamp fallback；它们以 canonical key 通过 `UpdateSessionMeta` 更新 thread
  linkage。Thread creation 只会为真正空的新 session 初始化 identity，并保留
  replacement-owned scope、aliases、summary 与 history selector。
- `pkg/agent/steering.go` 可以在 steering 场景下恢复 scope metadata。
- 因为 alias 解析发生在 agent loop 之下，测试和工具仍然可以继续使用 legacy alias。

## 相关文件

- `pkg/session/session_store.go`
- `pkg/session/manager.go`
- `pkg/session/jsonl_backend.go`
- `pkg/session/scope.go`
- `pkg/session/key.go`
- `pkg/session/allocator.go`
- `pkg/memory/jsonl.go`
- `pkg/threads/registry.go`
- `pkg/threads/threads.go`
- `web/backend/api/session.go`
- `pkg/agent/instance.go`
- `pkg/agent/loop.go`
- `pkg/agent/loop_message.go`
