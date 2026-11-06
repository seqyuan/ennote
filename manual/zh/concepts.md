# 概念模型

Ennote 的界面元素不多，但几个词的含义很精确。理解它们，后面所有操作都会变得可预期。

## Project（项目）

一个 Project 就是磁盘上的一个文件夹，加上它的配置（默认权限模式、所用的模型、启用的技能与
MCP 服务）。同一个仓库可以挂成多个 Project，互不影响。

## Session（会话）

一段连续的对话容器，属于某个 Project。Session 保存消息、分支、检查点与产物。
换话题就换 Session —— 历史不会互相污染，上下文也不会互相挤占。

一个 Session 可以有多个**分支**：从任意一条历史消息另起一支，原来的分支保持不动。

## Turn（轮次）与 Run（运行）

- **Turn**：你发一条消息，就是一个 Turn。
- **Run**：Turn 的一次执行。一个 Turn 可能对应多个 Run（重试、委派出的子 Run）。

Run 是有状态的机器，主要状态：

```
queued → running → waiting_for_approval → running → succeeded
                 ↘ waiting_children  ↗
                 ↘ waiting_delegation_admit
       任一状态 → cancelled / interrupted / failed
```

界面上的状态胶囊就是这些状态，点开 Run Inspector 能看到每一次状态跃迁对应的事件。

## Approval（审批）

当一次工具调用超过当前权限模式允许的范围时，Run 停在 `waiting_for_approval`，等你决定：

- **批准一次**：只放行这一次
- **总是允许**：对同类操作放开（按作用域记录）
- **拒绝**：Run 收到拒绝结果，通常会调整策略或直接收尾

审批内容里能看到工具名、风险等级与参数预览。**权限由 Worker 在执行前强制**，不是模型自觉。

## Attention（待办）

需要你处理的事情会进 Attention 面板：待审批、待回答问题、委派需要准入。
它们是跨 Session 的，所以切走再回来不会丢。

## Delegation（委派）

一个 Run 可以派生子 Run 去做子任务（例如"并行调查三个方向"）。父 Run 在等待期间状态是
`waiting_children`，子 Run 结束后结果回流到父 Run。委派的作用域绑定在 Session 上，
迟到或越权的子任务结果会被丢弃。

后台委派（background）不阻塞你的输入框：你可以继续聊天，完成时会有提示。

## Artifact（产物）

工具产生的文件（截图、生成的报告、抓取的页面）会作为 Artifact 记录，可以预览、下载。
它们属于 Session，跟着 Session 一起被清理。

## Skill 与 MCP

- **Skill**：一段可复用的流程说明与脚本，模型在需要时读取。
- **MCP**：Model Context Protocol 服务，给模型额外工具（数据库、外部 API 等）。

两者都在设置里管理，按 Project 或全局启用。

## 一条重要的边界

**Worker 是唯一的权威。** 界面只是它的客户端：所有状态从 Worker 读，所有变更发给 Worker。
这意味着刷新页面、换浏览器、甚至关掉界面，都不会让正在跑的 Run 停下来。

也因此，**界面在 Worker 不可达时是只读的**：它不会把你的操作排队等联网后重放，而是直接告诉你
现在不能写。这是有意的设计——静默重放会产生你无法预期的状态。
