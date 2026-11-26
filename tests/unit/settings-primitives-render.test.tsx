import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SettingsEmpty, SettingsLoading } from "@/components/settings/SettingsPrimitives";

describe("settings loading and empty primitives", () => {
  it("announces a loading block as a status and keeps the caller's wording", () => {
    const html = renderToStaticMarkup(<SettingsLoading label="Loading Graph…" />);
    expect(html).toContain('role="status"');
    expect(html).toContain("Loading Graph…");
    // The spinner is decoration; the label is what a screen reader reads.
    expect(html).toContain('aria-hidden="true"');
  });

  it("renders an empty state as plain text with no live region", () => {
    const html = renderToStaticMarkup(<SettingsEmpty>No versions yet. Create one above.</SettingsEmpty>);
    expect(html).toContain("No versions yet. Create one above.");
    expect(html).not.toContain("role=");
  });
});
