# Ennote

AI-native bioinformatics agent workspace.

## 架构

两个长期运行的 Go 进程，一个职责边界；Next.js 仅在构建时生成静态前端：

```
Browser → ennogate (Go, :30142) → ennoworker (Go, :0, loopback)
           守门人                     劳动者
           认证 / 代理 / 静态文件       SQLite / Agent / 工具 / Skill
```

## 快速开始

```bash
make dev
```

打开 http://127.0.0.1:30142，首次访问会引导设置密码。`make dev` 会先生成静态前端，再由 ennogate 启动和认证 ennoworker。

## 安装与分发

Ennote 采用「源码私有、产物公开」的分发模式：源码仓库不公开，但 npm 包与运行时二进制均可公开获取。

```bash
npm install -g @seqyuan/ennote
ennote start
```

- npm 包（`@seqyuan/ennote`）只包含 CLI 脚本和编译后的静态前端（约 12 MB）。
- 首次 `ennote start` 时，CLI 会从 [seqyuan/ennote-bin](https://github.com/seqyuan/ennote-bin) 的 GitHub Release 按当前平台/架构下载 Go 二进制（ennogate + ennoworker），缓存到 `~/.ennote/bin/`，后续启动不再下载。
- 支持平台：linux-x64 / linux-arm64 / darwin-x64 / darwin-arm64。

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
    llm/              Provider 接口、OpenAI-compatible、fake
    runs/             Run Coordinator（并发、取消、恢复）
    skills/           Skill bundle 加载与 per-run 快照
    store/            SQLite、迁移、Repository
    tools/            7 个文件/Shell 工具 + registry
    workspace/        Path jail、bubblewrap 沙箱
  migrations/         SQL 版本化迁移

app/                  Next.js 前端（React + SSE 消费）
contracts/            OpenAPI 3.1 契约
skills/               内置 Skill bundle
docs/                 设计与实施计划
```

## Roadmap

当前开发状态、优先级和明确延期范围以 [`docs/roadmap.md`](docs/roadmap.md) 为唯一来源。`docs/plans/` 中的旧优先级仅保留为设计与实施历史。

## 开发

```bash
make dev          # 启动 ennogate + ennoworker
make test-go      # Go 测试（含 -race）
make test         # JS 测试
make lint         # ESLint
make typecheck    # tsc --noEmit
make build        # 构建静态前端和本机 Go 产物
npm run release:prepare   # 构建四个平台的发布包并检查 tarball
npm run smoke:installed  # 安装临时 tarball 并验证双进程生命周期
```
