# Agent Flow 编排

一次对话能解决的任务，靠聊天就行。多步骤、有依赖、要重跑的长任务，用 Agent Flow 表达成一张
**Task 图**：谁是上游、谁能并行、预算多少、什么条件下算做完。

Agent Flow 是可恢复的：中途崩溃、机器重启、你手动取消，都不会让已经从图里消失的工作白做。

## 它由什么组成

```
FlowDefinition（声明式 Task DAG）
   ↓ 发布
AgentFlowVersion（不可变 revision，冻结 role/skill/模型/权限）
   ↓ 在 Project 上启用 + 发起
Anchor Run（元 Run：只做编排，自己从不调用 Provider）
   ↓ 每个 Task 一个
子 Run（干活的那一层，有自己的预算、审批与输出契约）
```

几个由此推出的结论：

- **编排与执行分离**：元 Run 只做状态机，不做模型调用，所以它能被单独测试、也容易推理
- **每个 Task 都是独立子 Run**：审批、预算、失败、取消都按子 Run 记录，出问题定位到具体 Task
- **版本不可变**：一条 Run 用的是哪版定义、哪版 Role、什么模型和权限，事后都能查

## 定义一个 Flow

```yaml
schemaVersion: 1
id: qc-review
description: 对一批样本做质控并汇总
budget:
  max_total_tokens: 400000         # 必填；发布时缺失会被拒绝

inputs:
  samples: { type: path, required: true }
outputs:
  report: { type: artifact }

tasks:
  inspect:
    role: explorer@3               # Role 引用带版本
    skills: [bio-workspace]
    goal: 读取 {{samples}}，列出样本与质控指标
    depends: []

  stats:
    role: analyst@2
    goal: 计算每个样本的 QC 指标
    depends: [inspect]

  gate:                            # check 任务：确定性命令
    type: check
    command: "python -m qc --fail-on low-quality"
    depends: [stats]

  report:
    role: writer@1
    goal: 汇总成报告
    depends: [stats]               # 与 gate 同层，可并行
    next:
      pass: done
      fail: retry

  retry:
    role: analyst@2
    goal: 修复低质量样本后重算
    depends: [gate]

  done:
    type: terminal
    terminal: { status: success, output: report }
```

### 任务字段

| 字段 | 作用 |
|---|---|
| `role` | `role@version`，发布时解析真实定义；解析不到直接拒绝发布 |
| `skills` | 该 Task 可用的技能 |
| `goal` | 交给子 Run 的目标描述 |
| `depends` | 上游任务；决定就绪顺序 |
| `type` | `role`（默认，生成子 Run）/ `check`（确定性命令闸门）/ 配合 `terminal` 的终点 |
| `command` | 仅 `check`：要执行的命令，走与工具调用相同的策略闸门与审批 |
| `fanOut` | 只读并行展开，`min`/`max` 控制实例数，输出按序聚合 |
| `budget` | 覆盖该 Task 的 token 上限 |
| `next` | 条件跳转（`pass`/`fail` → 任务名） |
| `terminal` | 终点任务，不生成子 Run，直接以 `success` 收尾 |
| `writes` | 声明本 Task 会写哪些工作区路径（glob）。**只影响调度**，见下文 |

### 收敛与并行

```yaml
convergence:
  - { from: retry, to: stats, max_rounds: 3 }   # 回边 + 轮数上限

parallelism:
  max: 6                     # 就绪任务并行上限，默认 10，发布时上限 16
  allow_disjoint_writers: true
```

收敛轮数超过 `max_rounds`，整个 Flow 以 `convergence_exceeded` 终结 —— 不会无限重试。

!!! warning "`writes` 只影响调度，不是沙箱边界"
    `writes` 声明用来判断两个写任务能不能并行（`allow_disjoint_writers` 打开时要求两两不重叠），
    **它不构成文件隔离**。真正的隔离来自沙箱（`bwrap`）。不要用 `writes` 声明来替代权限控制。

    不写 `writes` 的任务按"整个工作区"处理，独占一个 lane。

## 状态

| 层 | 状态 |
|---|---|
| Flow（元 Run） | `pending` → `running` → `completed` / `failed` / `cancelled` / `convergence_exceeded` / `budget_exceeded` |
| Task（节点） | `pending` → `running` → `completed` / `failed` / `blocked` / `cancelled` / `interrupted` |

Task 是 checkpoint：每个节点完成时写入，所以恢复时不需要重跑已完成的节点。

## 预算

- **Flow 级 `max_total_tokens` 必填**，发布时缺失直接拒绝
- Task 级 `budget.tokens` 可以收紧单个任务的额度
- 累计超过 Flow 预算，以 `budget_exceeded` 终结，并停掉未完成的任务

## 发布与启用

Flow 定义只是草稿，运行前必须走完两步：

```bash
# 1. 发布成不可变 revision（会跑真实校验）
POST /v1/flows/{flowID}/publish

# 2. 在 Project 上启用（显式绑定，不会自动发生）
PUT /v1/projects/{projectID}/graph-bindings/{flowID}
```

发布时的校验是"失败即拒绝"，包括：`role@version` 能否解析、Skill 是否存在、`check`/`fanOut`/
`terminal`/`next` 的结构是否自洽、并行上限、回边是否成环且有上限。

从 Agent Pack 装进来的 Flow 同样是**草稿**：安装器永远不会自动发布、也不会自动启用。

## 运行与恢复

- 在 `/graphs` 页面启动，或在 Session 里由模型请求
- 元 Run 会出现在 Session 里，点开能看到每个 Task 的状态与产出
- 后台运行不阻塞你的输入框

恢复语义：

- 进程重启后，服务会扫描该 Session 里所有非终态 Flow Run 并恢复（`RecoverSession`）
- 恢复只续跑未完成节点，已完成的 checkpoint 不重放
- 取消只写一个 `cancel_requested` 标志，由编排器在节点边界收敛，避免把正在执行的子 Run 撕开

## 读一次运行

| 看什么 | 在哪 |
|---|---|
| 哪些 Task 已完成 / 在跑 / 阻塞 | `/graphs` 的 Graph 活动面板，与 Run Inspector |
| 某个 Task 为什么失败 | 打开对应子 Run：事件流、工具调用、审批记录 |
| 花了多少 token | Flow 累计 + 每个子 Run 的用量 |
| 检查闸门为什么没过 | check 任务的结果与它的审批记录 |

## 常见失败

| 现象 | 原因 |
|---|---|
| 发布失败：`role@version` 解析不到 | Role 未发布，或版本号写错 |
| 运行按钮点了没反应 | Flow 有 draft 为未发布 revision，或 Project 没建 GraphBinding |
| 一直 `pending` | 依赖成环，或依赖的 Task 失败后没有 `next` 兜底 |
| `convergence_exceeded` | 回边轮数超限，检查 `max_rounds` 与修复任务是否真的在改输入 |
| `budget_exceeded` | Flow 预算太小，或某个 Task 的 `fanOut` 展开过多 |
| 两个写任务没有并行 | 没开 `allow_disjoint_writers`，或 `writes` 有重叠 |
