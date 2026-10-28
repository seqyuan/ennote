# Ennote

本地优先、可审计的 Agent 运行时，核心能力是 **Agent Flow**。

Ennote 把长任务编排成可恢复的 Agent Flow：Task 图、Role 绑定、预算、审批、
checkpoint 和恢复状态都持久化在本地 SQLite 中。它不依赖账号或云端即可启动、
计算和保存结果；生信是参考工作流，而不是平台承诺。

## 核心特点

### Agent Flow

- 用 Task 图表达依赖、分支、收敛和只读 fan-out，而不是把复杂流程塞进一条提示词。
- Agent Flow 是不可变版本；每次运行冻结 Role、Skill、模型和权限配置。
- 每个 Task 是独立子 Run，带自己的预算、审批和输出契约。
- Task 的 `writes` 声明只影响调度（reader/writer lane 与 disjoint writers），**不是沙箱隔离边界**。
- 串行执行、checkpoint 续跑、取消与恢复都已落地，并经过真实 Provider 验证。
- Graph 编辑器、版本校验、导入/导出共用同一套文件格式。

### 可审计执行

- append-only Run 事件、commit-before-publish、durable SQLite 历史。
- 危险工具调用走审批；支持 standing grants 与冻结的策略快照。
- 失败/中断 Run 可恢复，Session 可 rewind 和 branch，不覆盖已有历史。
- 每个请求、Run、审批和 Artifact 都有稳定的本地标识，便于回溯“谁在什么时候做了什么”。

### 本地优先

- `ennoworker` 只监听 loopback，`ennogate` 负责认证、静态 UI 和进程监督。
- 没有账号、Relay 或 Connect Token 也能完整使用 localhost/LAN Web。
- 云端不可用只会影响远程访问，不会停止 Worker、取消 Run 或删除数据。
- LAN 访问默认关闭；需要显式开启，并优先使用 TLS。

### 可组合

- 不可变 Role、Skills、MCP 工具、typed Artifact 和托管委托。
- 生信示例（`skills/bio-workspace`）展示如何把本地数据、shell 工具和 Agent Flow 组合起来。

## 架构

两个长期运行的 Go 进程，一个职责边界；Next.js 仅在构建时生成静态前端：

```
Browser → ennogate (Go, :45131) → ennoworker (Go, :0, loopback)
           守门人                     劳动者
           认证 / 代理 / 静态文件      SQLite / Agent Flow / 审批 / 工具 / Skill
```

Worker 是 Project、Session、Message、Run、Attention、审批、Role/模型配置、
Workspace、Artifact 和委托状态的唯一权威。Web UI 只是同一套 Worker API 的客户端。

## 快速开始

```bash
make dev
```

打开 http://127.0.0.1:45131，首次访问会引导设置密码。`make dev` 会先生成静态前端，再由 ennogate 启动和认证 ennoworker。

## 安装与分发

Ennote 是开源项目（MIT），仓库公开；npm 包与运行时二进制均由 GitHub Actions 在打版本 tag 时自动构建发布。

```bash
npm install -g @seqyuan/ennote
ennote start
```

- npm 包（`@seqyuan/ennote`）只包含 CLI 脚本和编译后的静态前端（约 12 MB）。
- 首次 `ennote start` 时，CLI 会从本仓库（[seqyuan/ennote](https://github.com/seqyuan/ennote)）的 GitHub Release 按当前平台/架构下载 Go 二进制（ennogate + ennoworker），缓存到 `~/.ennote/bin/`，后续启动不再下载。
- 支持平台：linux-x64 / linux-arm64 / darwin-x64 / darwin-arm64。
- 发布流程：打版本 tag（`vX.Y.Z`）后，[release workflow](.github/workflows/release.yml) 运行测试、构建四平台二进制、验证 npm 版本已生效，最后创建 GitHub Release。

### 审查 Agent Pack

`ennote pack inspect` 调用正在运行的 Worker 做只读审查：不安装、不执行、不验证签名、不写磁盘。

```bash
ennote pack inspect ~/Downloads/cell-qc.enpack      # 归档
ennote pack inspect ~/src/cell-qc                  # 目录包（需含 pack.yaml）
ennote pack inspect ~/Downloads/cell-qc.enpack --json      # 原始 inspect 结果
ennote pack inspect ~/Downloads/cell-qc.enpack --documents # 附带 flow/role/skill 正文
```

输出包含 `manifestDigest`（`pack.yaml` 原始字节）和 `packDigest`（排序后的 payload 行，排除顶层 `pack.sig`）、flow/role/skill 摘要、能力信封与 warnings。`format: flow` 与 `format: graph` 会如实标注，不会被悄悄降级。Worker 未启动时命令直接失败并提示 `ennote start`，不做离线降级。

### 安装 / 列出 / 卸载 Agent Pack

安装同样只通过 loopback Worker 落盘，CLI 自己不做解析或写盘。

```bash
ennote pack install ~/Downloads/cell-qc.enpack --dry-run   # 只出计划，不写任何文件
ennote pack install ~/Downloads/cell-qc.enpack             # 正式安装（可传目录包）
ennote pack install ~/src/cell-qc --as-flow qc-review=cell-qc-review
ennote pack list                                          # 已安装 Pack 摘要
ennote pack remove seqyuan/cell-qc-review                 # 卸载
ennote pack remove seqyuan/cell-qc-review --force          # 覆盖本地编辑 / 共享 Role 的拒绝
```

S2a 只安装两种形态：Role-only Pack（每个 Role 变成 Worker 全局 Role 并发布 revision），以及 `format: graph` 的 Flow Pack（未发布 revision 的 Graph draft + 其私有 Role/Skill）。`format: flow`、非空 `mcp`、非空 `requires` 会被明确拒绝并返回稳定错误码（如 `pack_flow_format_unsupported`），不会降级安装。dry-run 恒返回计划（含 blockers），正式安装遇到 blocker/冲突返回 422。安装后的 Graph 默认不可运行：需要用户按现有 Graph 发布流程发布 revision 才算启用。

### 从 v0.1.x 升级

v0.1.x 的旧数据库（`data/ennote.db`）不再被读取，但也不会被删除：升级后首次启动会把它（含 `-wal`/`-shm`）原子移动到 `data/legacy-v1/` 并打印一条 `archived legacy V1 database` 警告。迁移完成后再次启动不会改动该归档。需要找回旧数据时，从 `data/legacy-v1/` 手动复制即可。

### 局域网访问

默认只监听 `127.0.0.1`。需要从局域网打开时：

```bash
# 推荐：TLS 终止在 ennogate，cookie 带 Secure
ENNOTE_HOSTNAME=0.0.0.0 ENNOTE_ALLOW_LAN=1 \
ENNOTE_TLS_CERT=/path/cert.pem ENNOTE_TLS_KEY=/path/key.pem ennogate

# 不推荐：显式接受明文风险（密码和 7 天 cookie 会明文过网）
ENNOTE_HOSTNAME=0.0.0.0 ENNOTE_ALLOW_LAN=1 ENNOTE_ALLOW_INSECURE_LAN=1 ennogate
```

非 loopback 地址在缺少 `ENNOTE_ALLOW_LAN=1` 时会拒绝启动；缺少 TLS 且没有 `ENNOTE_ALLOW_INSECURE_LAN=1` 时同样拒绝启动。

## 目录

```
ennoworker/
  cmd/ennogate/       BFF：认证、代理、静态文件
  cmd/ennoworker/     Agent 引擎、数据库、工具、沙箱
  internal/
    agent/            Go Agent Loop + steer/follow-up
    api/              ennoworker HTTP API + SSE
    config/           配置与环境变量
    domain/           领域类型和状态机
    events/           Durable EventWriter + 订阅 Hub
    graphrun/         Agent Flow 执行与恢复
    llm/              Provider 接口、OpenAI-compatible、fake
    runs/             Run Coordinator（并发、取消、恢复）
    skills/           Skill bundle 加载与 per-run 快照
    store/            SQLite、Repository
    tools/            文件/Shell/审批工具 + registry
    workspace/        Path jail、bubblewrap 沙箱
  sessionmigrations/  Session SQL 版本化迁移
  catalogmigrations/  Catalog 投影迁移
  usagemigrations/    Usage 投影迁移
app/                  Next.js 前端（React + SSE 消费）
contracts/            OpenAPI 3.1 契约
skills/               内置 Skill bundle
```

## 当前重点

- Agent Flow 的可观测性、恢复语义和实际采用，而不是继续扩张产品表面。
- Run 的可审计性、审批体验和本地留痕。
- 本地优先与沙箱边界（bwrap 网络隔离、SandboxNone 的凭据保护）。
- 生信作为参考工作流维护，不承诺 Warm Kernel、Notebook 或 Publication 工作台。

## 开发

```bash
make dev          # 启动 ennogate + ennoworker
make test-go      # Go 测试（go test ./...）
make test         # JS 测试
make lint         # ESLint + design token guard
make typecheck    # tsc --noEmit
make build        # 构建静态前端和本机 Go 产物
npm run test:e2e  # Playwright
npm run release:prepare   # 构建四个平台的发布包并检查 tarball
npm run smoke:installed   # 安装临时 tarball 并验证双进程生命周期
```

CI 会在 Go 测试矩阵里额外运行 `-race`。
