# Session 系统

> 返回 [README](../README.md)

PicoClaw 把入站消息映射到稳定的会话作用域，并把 session、thread 与
handoff 状态保存在 workspace 本地 SQLite 数据库中。HTTP 与 Go 接口保持
不变；JSON/JSONL 只作为旧数据导入格式。

## 职责

Session 子系统负责：

1. 从路由作用域生成 canonical 会话身份；
2. 持久化有序消息、摘要、scope 与 alias；
3. 提供严格一致读取和 CAS snapshot 替换；
4. 在事务中维护 thread registry、成员关系、链接与 handoff；
5. 自动导入并归档旧 session/thread JSON，不做双写。

## 主要组件

| 层次 | 文件 | 作用 |
| --- | --- | --- |
| Session 接口 | `pkg/session/session_store.go` | `SessionStore` 及可选 snapshot、替换、scope admission 能力。 |
| 内存存储 | `pkg/session/manager.go` | 仅当路径为空时使用的非持久存储；非空路径是已弃用的 SQLite facade。 |
| SQLite 适配层 | `pkg/session/jsonl_backend.go` | `NewSQLiteBackend`；JSONL 命名的类型/构造器保留一个兼容周期。 |
| 持久存储 | `pkg/memory/sqlite_store.go`、`sqlite_schema.go`、`sqlite_migration.go` | 类型化表、事务、schema 校验与旧数据导入。 |
| Thread 存储 | `pkg/threads/threads.go`、`pkg/threads/registry.go` | Thread 投影及 create/update/attach/handoff 事务。 |
| Runtime 集成 | `pkg/agent/instance.go` | 在文件工具前打开数据库；校验失败时 fail closed。 |

## 身份模型

`session.SessionScope` 包含 agent、channel、account、有序 dimensions 和每个
dimension 的值。支持 `space`、`chat`、`topic`、`sender`。Canonical key：

```text
sk_v1_<sha256>
```

旧 `agent:...` key 作为 alias 保留。严格解析会拒绝歧义 ownership；普通
兼容读取保持既有 direct-key 与确定性 owner 规则。Alias promotion 在一个
事务中把符合条件的旧 history 移入空 canonical session，绝不会覆盖已有
canonical history。

## 数据库布局

权威文件：

```text
<workspace>/sessions/sessions.db
```

同目录可能存在 WAL/SHM companion。主要表：

| 表 | 状态 |
| --- | --- |
| `sessions`、`session_messages` | 身份、摘要、时间、version 与有序消息 |
| `session_scopes`、`session_scope_dimensions` | 类型化 scope 与有序 dimension/value |
| `session_aliases` | 有序兼容 alias |
| `threads`、`thread_context`、`thread_aliases` | Thread 身份、状态、context 与 alias |
| `thread_sessions`、`session_thread_links` | 有序成员、唯一 primary、当前 attach link |
| `thread_handoffs` | origin/target 关系与 handoff 摘要 |
| `storage_imports`、`storage_import_issues` | source digest、安全计数、归档状态、issue code 与 record digest |

消息 role/content/model/timestamp 与 tool-call 身份使用类型化列。只有嵌套的
media、attachment、part、system block、tool call payload 使用 canonical
JSON BLOB。

## SQLite 约束

每次打开都会强制：

- 私有目录及 `0600` database/WAL/SHM；
- WAL、foreign keys、有限 busy timeout、`synchronous=FULL`；
- 在 `BEGIN IMMEDIATE` 中按 `PRAGMA user_version` 迁移；
- 精确 table/index 定义，并拒绝未知 schema object；
- row/byte/timestamp/canonical JSON/sequence/primary/link reciprocity 校验；
- 使用前执行 integrity 与 foreign-key 检查。

比当前 binary 更新的版本、损坏数据库或错误 schema 一律 fail closed，不回退
到 JSON。

## 事务与并发

相关变化共享一个 immediate transaction：消息追加与 version 更新、scope 与
alias 替换、snapshot CAS、级联删除、thread create/update/membership/link，
以及 attach 与 handoff 发布。带 aggregate 状态的 row 使用单调 version；
read/modify/write 用 version fence。多个进程通过 SQLite WAL 与 busy timeout
协调；不支持旧/新 binary 混用。

严格 snapshot 返回 detached canonical key、history、summary、scope、alias
与临时 revision。替换必须匹配该 revision；expected revision 为空表示要求精确
不存在。Stale token 返回 `ErrSnapshotConflict`，不会发布半个 tuple。

## 旧数据迁移

首次打开时，store 会确定性扫描 `sessions/` 与 `threads/` 下的 aggregate JSON、
metadata、JSONL/选中的 history slot、delete manifest、thread registry 与
handoff。导入顺序为 session，然后 thread 与 handoff。

输入受大小限制并经过 hardened path 校验。无效单条记录会跳过，只记录安全
issue code 与 SHA-256 digest；不安全 root/symlink/mode、枚举或大小失败、
SQLite/integrity 失败会回滚整个迁移。冲突与依赖解析完成后才写最终每-source
imported/skipped 计数，因此 audit 与已提交 row 一致。

提交后，所有已检查 source 无覆盖地移动到：

```text
<workspace>/legacy-json/sessions-v1/<原相对路径>
```

归档保留权限。归档中断会在下次打开时重试而不重复导入；digest 改变的 source
不会被归档。SQLite 从 commit 起立即成为权威，不双写、不回退。

回滚前必须停止全部 PicoClaw 进程，恢复归档原布局，并删除或恢复
`sessions.db` 及匹配的 `-wal`、`-shm`。

## 文件工具保护

Agent write/edit/append/apply-patch 工具保护活动的 `sessions`、`threads`、
`sessions.db`、WAL/SHM 以及 workspace `legacy-json` namespace。每个 generation
使用一个覆盖默认、配置及所有命名 agent workspace 的有界目录：第一遍仅保留
流式摘要，第二遍保留去重后的物理文件身份集合。root/owner factory 与 local
repair 共享该目录，并在读取前校验实际打开的 handle；旧源文件移动到
`sessions-v1` 后，其 namespace 外的硬链接别名仍不可写。Session store 在
apply-patch 捕获 volatile roots 前打开，避免数据库创建或归档使普通源码 patch
失效。

## 兼容性

- HTTP request/response 与 session/thread ID 不变。
- `memory.NewJSONLStore`、`session.NewJSONLBackend` 保留一个兼容周期，但底层
  是 SQLite。
- `session.NewSessionManager("")` 仍是非持久内存 store。
- `session.NewSessionManager(nonempty)` 是已弃用 SQLite facade，打开失败即
  fail closed。
- 支持的 runtime 不再创建可变 session/thread JSON。

## 验证

主要证据：`pkg/memory/sqlite_store_test.go`、
`pkg/session/jsonl_backend_test.go`、`pkg/threads/threads_test.go`、
`web/backend/api/session_test.go`、`web/backend/api/thread_test.go` 与
`pkg/agent/file_mutation_policy_test.go`。
