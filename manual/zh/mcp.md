# MCP

MCP（Model Context Protocol）服务器给模型额外的工具：数据库、内部 API、文件索引等等。

Ennote 的 MCP 不是"全局一开就全都有"，而是一条 **Profile → Project 绑定 → Run 快照 → Role 取交集**
的链路。搞清楚这条链路，就不会出现"我明明配了，模型却看不到"的情况。

## 链路全貌

```
Profile（服务器身份 + 不可变连接版本）        ← Worker 级，或在 Project 里声明
   ↓ 建 binding，选具体工具、绑凭据
ProjectBinding（某个 Project 启用哪些工具）   ← 开关在这一层
   ↓ Run 冻结
RunMCPServerSnapshot（本次 Run 的能力集）     ← 之后改 binding 不影响它
   ↓ 委派给 Role 时再与 allowlist 取交集
子 Run 可见工具 = 绑定已选工具 ∩ Role.allowedTools
```

## 第一步：定义服务器（Profile）

设置 → MCP → 新建，两种来源：

| 来源 | 存在哪 | 说明 |
|---|---|---|
| `managed` | `~/.ennote/config/mcp.json` | 你自己加的服务，Worker 级 |
| `project_file` | Project 目录内 | 跟着仓库走，`ProjectScope` 限定到该 Project |
| `bundled` | 随 Ennote 发布 | 内置目录里挑选，无需自己写连接配置 |

每种服务器有**不可变的版本**（`ProfileVersion`），记录传输方式与连接参数：

| 传输 | 需要的字段 |
|---|---|
| `stdio` | `executable`、`argv`、可选 `cwd`、`envLiterals` |
| `streamable_http` | `endpoint`、可选 `headerLiterals` |
| `legacy_sse` | `endpoint` |

改连接参数会产生新版本，旧版本仍可被引用 —— 这样一次 Run 用的是哪套参数永远查得到
（`configDigest` 记录在快照里）。

## 第二步：给 Project 建绑定

这是**真正的开关**。绑定记录：

- `desiredEnabled` —— 是否启用
- `selectedRemoteToolNames` —— **只暴露你勾选的工具**，不会自动全给
- `credentialRefs` —— 凭据引用
- `required` —— 见下一节

同一个服务器在不同 Project 可以有不同绑定、不同工具子集。没建绑定的 Project 看不到任何工具。

## 第三步：Run 启动时冻结

Run 开始时把 Project 的启用绑定解析成快照：服务器、版本、协商协议、catalog digest、
每个工具的 schema digest、以及 `required` 与不可用原因。

两条规则值得记住：

- **幂等**：审批恢复、rewind 重跑都复用原快照，中途改 binding 不会改变进行中的 Run
- **只冻结被选中的工具**：没有任何"顺手多给几个"的自动扩充

## `required` 的语义

| 取值 | 服务器连不上时 |
|---|---|
| `required: true` | **阻断 Run 启动**。即使有热缓存也会先做一次连通性验证 |
| `required: false`（默认） | 冻结一个 `unavailableReason` 快照，Run 照常开始，只是没有那些工具 |

所以：把关键依赖标成 `required`，失败会立刻暴露；把"有更好、没有也行"的标成可选，
不会因为一个服务器挂了就让整个 Run 起不来。

一次 Run 的服务器数量有上限（`maxServerCountPerRun`），超了直接失败而不是悄悄截断。

## 凭据

引用格式 `scheme:value`：

| 形式 | 解析方式 |
|---|---|
| `env:MY_TOKEN` | 从 **Worker 进程的环境变量**读取 |
| `file:/path/to/secret` | 需要注入解析器；当前随包构建未注入，会明确报错而不是静默失败 |
| `keyring:service/account` | 同上 |

凭据值**只在建立连接时解析一次**，不写日志、不落库。stdio 服务器还会过滤环境变量黑名单，
Ennote 自己的密钥不会漏给子进程。

## 网络策略

- **loopback 端点永远允许**（本机服务）
- **私有网段默认拒绝**：endpoint 解析到内网地址会报
  `endpoint resolves to a private network address ... requires an explicit network policy`
- HTTP 传输会**先解析一次再钉住 IP**，防止 DNS rebinding；默认不跟随跨域重定向、默认严格 TLS

## 工具在 Run 里长什么样

暴露名是 `服务器 slug__工具名`（例如 `mydb__query`）。这个名字很重要 —— 在一处会用错：

!!! warning "给 Role 写 allowedTools 要用暴露名"
    Role 定义的 `allowedTools` 里必须写 **暴露名**，写远端原名 `query` 匹配不上，
    结果是子 Run 静默拿不到这个工具。

    委派子 Run 的 MCP 工具 = 绑定已选工具 **∩** `Role.allowedTools`。
    Host Run（直接对话，不经过 Role）不做这层过滤，拿到绑定的全部已选工具。

工具的风险等级来自服务器声明（`readOnlyHint` 影响分类），因此**审批仍按你当前的权限模式执行**：
`ask` 模式下，写类 MCP 工具会弹审批，`discuss` 模式下会被拒绝。MCP 不是绕开审批的后门。

## 服务器改了工具列表怎么办

服务器发 `tools/list_changed` 时：

- 该绑定的**未来** catalog 被标记为与当前不一致（stale）
- **正在跑的 Run 不受影响** —— 它的工具集已经冻结，不会在运行中热更新

也就是说：不会出现"跑到一半工具变了"，但下次 Run 会重新拉取 catalog。
如果服务器的工具 schema 变了，旧的审批记录不会误用到新的 schema 上（审批身份绑定 schema digest）。

## 排障速查

| 现象 | 多半是 |
|---|---|
| Run Inspector 里没有 MCP 区块 | 该 Project 没有 `desiredEnabled` 的绑定 |
| 服务器出现但工具为空 | `selectedRemoteToolNames` 没勾选，或服务器 `unavailableReason` 非空 |
| 子 Run 少了某个工具 | Role 的 `allowedTools` 写的是远端原名，或没写暴露名 |
| Run 起不来，报 binding/profile unavailable | 标了 `required: true` 而服务器不可达 |
| 连接报私有网段 | endpoint 是内网地址，需要显式网络策略 |
| `credential resolver required for file:` | 用了 `file:`/`keyring:` 引用；改用 `env:` |

每次 Run 的服务器与工具快照、以及每次 MCP 调用的状态（planned / dispatched / completed /
failed / cancelled）都能在 Run Inspector 里看到。
