import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PermissionChip } from "@/components/PermissionChip";
import type { PermissionMode } from "@/lib/permission-mode";

function render(mode: PermissionMode) {
  return renderToStaticMarkup(
    <PermissionChip permissionMode={mode} permissionReady setPermissionMode={() => {}} disabled={false} />,
  );
}

describe("PermissionChip labels", () => {
  // The chip names what happens to a non-read-only tool, not the mood of the
  // conversation, and the label has to come from the dictionary so a Chinese
  // session does not read "Discuss".
  it("names each mode by its effect", () => {
    expect(render("discuss")).toContain("Read-only");
    expect(render("ask")).toContain("Ask");
    expect(render("auto")).toContain("Auto");
  });

  it("keeps the stored mode value out of the label", () => {
    const html = render("discuss");
    expect(html).not.toContain("Discuss");
    expect(html).toContain("Permission mode: Read-only");
  });
});
