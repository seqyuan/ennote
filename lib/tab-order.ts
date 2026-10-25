/** Move one tab before another while preserving the rest of the order. */
export function moveTab<T extends { id: string }>(tabs: T[], fromId: string, toId: string): T[] {
  if (fromId === toId) return tabs;
  const from = tabs.findIndex((tab) => tab.id === fromId);
  const to = tabs.findIndex((tab) => tab.id === toId);
  if (from < 0 || to < 0) return tabs;
  const next = [...tabs];
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved);
  return next;
}
