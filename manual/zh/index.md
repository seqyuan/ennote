---
title: 快速开始
---

# Ennote

本地优先、可审计的 agent 运行时。它在你的机器上跑一个 Worker 进程，用浏览器打开一个界面，
所有 Session、消息、Run、审批记录和文件变更都留在本地的 SQLite 里。

不需要账号，不需要联网就能开始工作。云端只负责可选的远程访问。

## 它和别的 agent 工具差在哪

- **数据在本地**：`~/.ennote/` 下的 SQLite 与文件，删掉 tool 不影响你的历史。
- **过程可审计**：一次 Run 的每个事件、每次工具调用、每次审批都有记录，能回答"这条结果是怎么来的"。
- **权限在工具边界上强制**：不是靠提示词请求模型自觉，而是在执行工具前由 Worker 拦截。
- **云端不可用不影响本地**：账号过期、Relay 故障、DNS 挂了，本机照常启动、照常计算。

## 环境要求

| 项 | 要求 |
|---|---|
| 操作系统 | macOS 或 Linux（x64 / arm64） |
| Node.js | 22 及以上 |
| 浏览器 | 支持 SSE 的现代浏览器 |
| 网络 | 首次安装需要；之后本地使用不需要 |

## 安装

```bash
npm install -g @seqyuan/ennote
ennote start
```

`start` 会做三件事：把 Worker 二进制下载到 `~/.ennote/bin/`（已缓存则跳过）、启动 `ennogate`
监听 `127.0.0.1:45131`、用默认浏览器打开界面。

首次打开会要求设置一个密码。这个密码只保护本机界面，不是云端账号。

!!! note "首次安装离线怎么办"
    如果下载失败（无网络、墙内、内网），`ennote` 会回退到 `~/.ennote/bin/` 里已有的最近一个
    完整版本并打印告警，而不是直接退出。用 `ennote status --json` 可以看到实际在跑哪个版本：

    ```bash
    ennote status --json
    # { "running": true, "binaryVersion": "0.6.0", "binaryFallback": false, ... }
    ```

## 五分钟上手

1. **建一个 Project**：Project 就是一个文件夹。选一个空目录或已有代码库，Ennote 把它作为工作区。
2. **新建 Session**：一个 Session 是一段连续对话。换话题就换 Session，历史互不干扰。
3. **说第一句话**：比如"读一下这个仓库的结构，告诉我入口在哪"。
4. **看它请求权限**：读文件的工具通常直接放行；写文件、执行命令、访问网络会弹出审批卡片。
   批准前你可以看到具体参数。
5. **看它是怎么得出答案的**：点开 Run Inspector（运行检查器），能看到事件流、工具调用、
   token 用量与耗时。

## 数据放在哪

```
~/.ennote/
  bin/                      下载的 Worker 二进制（按版本命名）
  config/                   模型、Provider 凭据、策略、MCP 配置（0600）
  agents/roles/             角色定义
  agents/graphs/            图谱定义
  skills/                   技能
  projects/<project>/sessions/   每个 Session 一个 SQLite 数据库
  data/                     投影库：catalog.db、usage.db
  cache/                    各类缓存
  runtime/                  Worker 握手文件（worker-state.json）
  spill/                    超长工具输出的落盘文件
  logs/ennote.log           启动器与进程日志
```

想搬家就整体拷走；想彻底清空就删掉这个目录。

## 升级

```bash
ennote stop
npm install -g @seqyuan/ennote@latest
ennote start
```

数据库 schema 有版本化迁移，升级会自动执行；升级前的数据不会被删除。

## 常用命令

| 命令 | 作用 |
|---|---|
| `ennote start` | 启动服务（默认 `127.0.0.1:45131`） |
| `ennote stop` | 停止 |
| `ennote status` | 是否在运行、地址、实际二进制版本 |
| `ennote passwd` | 重设或移除本机界面密码 |
| `ennote pack inspect <path>` | 查看一个 Agent Pack 会安装什么（不改动任何东西） |

需要从别的机器访问？见 [局域网与安全](lan-security.md)。
