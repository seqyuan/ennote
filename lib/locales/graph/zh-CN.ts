/**
 * Simplified Chinese copy for the Graph editor.
 *
 * Lives beside the settings dictionary for the same reason: a board's copy grows
 * in its own module instead of pushing `lib/locales/*.ts` past the `lib/` line
 * budget (scripts/check-file-sizes.mjs).
 */
export const graphZhCN: Record<string, string> = {
  "graph.save.retry": "重试保存",
  "graph.save.viewDisk": "查看磁盘版本",
  "graph.save.keepMine": "保留我的",
  "graph.save.discardMine": "丢弃我的",
  "graph.save.inFlight": "保存仍在进行——等它结束后再决定。",
  "graph.save.fileChanged": "磁盘上的文件已变更。",
  "graph.builder.title": "构建器",
  "graph.builder.desc": "针对该 Graph 的持久对话。",
  "graph.builder.hint": "描述 Task、执行选项与依赖关系。构建器会返回一份经过校验的提案。",
  "graph.builder.inputAria": "Graph 构建器指令",
  "graph.builder.placeholder": "描述要改的 Graph 内容…",
  "graph.identity.heroEdited": "Hero 已编辑——未包含在已发布版本中",
  "graph.identity.neverPublished": "尚未发布",
  "graph.identity.runsFrom": "运行来源",
  "graph.identity.notBound": "未绑定任何项目",
  "graph.localHero.title": "Graph 局部 Hero",
  "graph.localHero.defaultsNote": "上下文、委派上限与报告契约沿用该 Hero 创建时的默认值。",
  "graph.localHero.changedOnDisk": "该 Hero 在磁盘上已变更，你的文本尚未保存。",
  "graph.dependencies.empty": "添加一个 Task 即可开始构建 Graph。",
  "graph.dependencies.levelsAria": "Task 依赖层级",
  "graph.dependencies.noOthers": "没有其它 Task",
  "graph.header.saving": "保存中",
  "graph.header.unsaved": "有未保存的修改",
  "graph.header.draftSaved": "草稿已保存",
  "graph.tabs.addAria": "添加 Graph",
  "graph.task.addAria": "添加 Task",
  "graph.task.empty": "还没有 Task。添加一个，或让构建器起草。",
  "graph.task.localHeroNote": "局部 Hero 仅属于该 Graph；全局 Hero 为共享且在此只读。",
  "graph.task.newHero": "新建 Graph 局部 Hero",
  "graph.task.invalidHeroId": "Graph 局部 Hero 的 id 需以字母开头，仅可含 a-z、0-9、- 或 _（2–32 个字符）。",
};
