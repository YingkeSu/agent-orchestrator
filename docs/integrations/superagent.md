# SuperAgent 执行桥接契约

> 面向外部编排器（当前是 DeepSeek SuperAgent）的 AO 执行接口。SuperAgent 保留 Router/Ticket/Mandate/注意力层；AO 只负责单个工作区内的 session、worktree、harness 与 PR/CI 事实。本文描述 AO 侧契约，不把 Ticket 概念引入 AO。

## 1. 边界

| 层 | 负责 |
|---|---|
| SuperAgent Router | 人类入口、Source、Trigger 去重、Policy/Authorization、Eval、Ticket、Mandate、Report、注意力 |
| 桥接层（SuperAgent 侧） | workspace registry、`workspaceRef → AO projectId` 解析、`Ticket/Assignment → externalRef` 幂等键、Report 轮询、重试/恢复 |
| AO | project、隔离 worktree、harness 会话、Chat/TUI、activity、PR/CI/review、Kanban 派生状态 |

一个 **Ticket 只属于一个 workspace**。workspace 在 AO 里就是一个 project；跨 workspace 的请求拆成多个 Ticket。AO 不知道 Ticket，也不知道 workspace alias。

## 2. 幂等键

- `externalRef`：编排器生成的稳定工作项 id，全局唯一，建议带命名空间，例如 `superagent:tk_123`。
- `assignmentRef`：同一工作项内的执行代次，例如 `asg_01`；重试时递增。
- `requestFingerprint`：不可变启动参数的稳定哈希（project、harness、model、mode、branch、prompt、displayName）。同一 `externalRef` 换 fingerprint = `409 EXTERNAL_REF_CONFLICT`，不静默复用。

同一 `externalRef` + 同一 fingerprint 重复 `PUT` 返回已有 session（`created=false`），只推进 `assignmentRef`。这保证 effect 重放、桥接重启、AO daemon 重启都不会重复创建 worktree。

## 3. 接口

### 3.1 Ensure

`PUT /api/v1/execution/sessions/{externalRef}`

```json
{
  "projectId": "mer",
  "assignmentRef": "asg_01",
  "requestFingerprint": "sha256:…",
  "prompt": "……",
  "displayName": "fix-login",
  "harness": "claude-code",
  "model": "sonnet-5",
  "mode": "chat",
  "branch": "agent/tk_123"
}
```

返回 `201`（新建）或 `200`（复用）：

```json
{
  "created": true,
  "session": {
    "externalRef": "superagent:tk_123",
    "assignmentRef": "asg_01",
    "projectId": "mer",
    "sessionId": "mer-42",
    "createdAt": "…",
    "updatedAt": "…",
    "execution": { "status": "working", "needsPerson": false, "…": "…" }
  }
}
```

### 3.2 State

`GET /api/v1/execution/sessions/{externalRef}`

返回同一个 `session` 形状。`execution` 是 AO 在读取时派生的，不落库：

| 字段 | 含义 |
|---|---|
| `status`, `displayStatus`, `kanbanColumn` | AO 的派生状态（与面板同源） |
| `activity` | `active/idle/waiting_input/blocked/exited` |
| `isTerminated` | session 是否已终止 |
| `needsPerson` + `waitingReason` | 是否在等人；`question/approval/input` |
| `latestAssistantUpdate`, `latestUserPrompt` | 最近的 agent/人消息摘要 |
| `prs[]` | PR URL、number、state、CI、review、mergeability |
| `updatedAt` | 轮询去重依据 |

`needsPerson` 只覆盖“人必须回答/批准/输入”；CI 失败和 review changes 不算，因为 AO 的 worker 会处理。

### 3.3 Stop

`POST /api/v1/execution/sessions/{externalRef}/stop`，body `{"reason":"superseded"}`。

幂等：已终止返回 `200` + `stopped=false`；否则 kill session 并返回 `stopped=true`。worktree 不删除（AO 禁止强删 dirty worktree），由 AO 的 cleanup 策略处理。

### 3.4 List / reconcile

`GET /api/v1/execution/sessions?projectId=mer` 返回该 project 的全部外部工作项。桥接启动或 AO 重启后按 `externalRef` 逐个 `GET`，以 Assignment 为意图真相、session 为执行事实，恢复或标记失败。

## 4. 投递输入

AO 不新增投递路径。桥接用现有：

`POST /api/v1/sessions/{sessionId}/conversation/messages`

```json
{"text":"……","clientMessageId":"input_01"}
```

`clientMessageId` 幂等；返回 `duplicate=true` 时不要重发。`state=queued|running` 表示已接收；`running` 才是 agent 已拾取。

## 5. 状态映射（桥接侧）

| SuperAgent | AO 事实 | 动作 |
|---|---|---|
| Assignment assigned | ensure 成功 | 不重复创建 |
| Worker active | `status=working` 或 `activity=active` | 静默 |
| Worker waiting | `status=idle` / `activity=idle` 且无未读输入 | `report waiting` |
| Worker ready | PR 已开/已批准/可合并，且 turn 结束 | `report ready` + PR link |
| Worker blocked | `needsPerson=true` | `report blocked` + 结构化 human_action |
| Worker failed | 启动失败 / harness 非零退出 / `terminated` 且无 PR | `report failed` |
| Worker dropped | stop 完成 | `report dropped` |
| CI failed / review changes | PR facts | 作为 Input 投回同一 worker（AO 已有 auto-inject） |

## 6. 工作区解析

workspace registry 在桥接层，不在 AO：

```yaml
workspaces:
  - ref: checkout
    alias: 结算服务
    repoURL: https://github.com/acme/checkout
    aoProjectId: checkout
    harness: claude-code
    model: sonnet-5
```

- 新 workspace：人改配置 + 热加载；不在 Router 里自动注册未授权仓库。
- 解析优先级：上游显式 workspaceRef → Source repo 命中 → 文本别名唯一命中 → 线程粘性（TTL） → 唯一启发 → 问一次。
- 面向人的文案只用 alias（`[结算服务] 已开 PR…`），不暴露 worktree 路径或 projectId。

## 7. 非目标

- 不把 Policy/Mandate/Ticket/Source 搬进 AO。
- 不让 AO 自动注册 workspace（权限模型在桥接层）。
- 不做跨 workspace 的共享 session。
- 首个 PR 不含 SSE/webhook；桥接用轮询 + `updatedAt` 去重，SSE 作为后续增强。
