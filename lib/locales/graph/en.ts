/**
 * English copy for the Graph editor.
 *
 * Lives beside the settings dictionary for the same reason: a board's copy grows
 * in its own module instead of pushing `lib/locales/*.ts` past the `lib/` line
 * budget (scripts/check-file-sizes.mjs).
 */
export const graphEn: Record<string, string> = {
  "graph.save.retry": "Retry save",
  "graph.save.viewDisk": "View disk version",
  "graph.save.keepMine": "Keep mine",
  "graph.save.discardMine": "Discard mine",
  "graph.save.inFlight": "A save is still in flight — decide when it finishes.",
  "graph.save.fileChanged": "The file changed on disk.",
  "graph.builder.title": "Builder",
  "graph.builder.desc": "Persistent conversation for this Graph.",
  "graph.builder.hint": "Describe Tasks, execution choices, and dependencies. Builder will return a validated proposal.",
  "graph.builder.inputAria": "Graph Builder instruction",
  "graph.builder.placeholder": "Describe the Graph change…",
  "graph.identity.heroEdited": "Hero edited — not in published revision",
  "graph.identity.neverPublished": "Never published",
  "graph.identity.runsFrom": "Runs from",
  "graph.identity.notBound": "Not bound to any Project",
  "graph.localHero.title": "Graph-local Hero",
  "graph.localHero.defaultsNote": "Context, delegation limits and the report contract keep the defaults this Hero was created with.",
  "graph.localHero.changedOnDisk": "This Hero changed on disk. Your text is not saved.",
  "graph.dependencies.empty": "Add a Task to start the Graph.",
  "graph.dependencies.levelsAria": "Task dependency levels",
  "graph.dependencies.noOthers": "No other Tasks",
  "graph.header.saving": "Saving",
  "graph.header.unsaved": "Unsaved changes",
  "graph.header.draftSaved": "Draft saved",
  "graph.tabs.addAria": "Add Graph",
  "graph.task.addAria": "Add Task",
  "graph.task.empty": "No Tasks yet. Add one or ask Builder to draft the Graph.",
  "graph.task.localHeroNote": "Local Heroes belong only to this Graph. Global Heroes are shared and read-only here.",
  "graph.task.newHero": "New Graph-local Hero",
  "graph.task.invalidHeroId": "A Graph-local Hero needs an id that starts with a letter and uses only a-z, 0-9, - or _ (2–32 characters).",
};
