/**
 * Line-aligned text diff for read-only "what changed" views: rows are prefixed
 * with "- " (removed), "+ " (added), or "  " (unchanged). A deterministic
 * index-aligned comparison is enough for surfacing a file's changes without
 * pulling in a diff library.
 *
 * It lived in a browser-side YAML editor model that nothing used; the diff was its
 * only consumer.
 */
export function diffText(oldText: string, newText: string): string[] {
  const oldLines = oldText.split("\n");
  const newLines = newText.split("\n");
  const out: string[] = [];
  const max = Math.max(oldLines.length, newLines.length);
  for (let i = 0; i < max; i++) {
    const before = oldLines[i];
    const after = newLines[i];
    if (before === undefined) {
      out.push(`+ ${after}`);
    } else if (after === undefined) {
      out.push(`- ${before}`);
    } else if (before === after) {
      out.push(`  ${before}`);
    } else {
      out.push(`- ${before}`);
      out.push(`+ ${after}`);
    }
  }
  return out;
}
