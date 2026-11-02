import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { OfflineBanner } from "@/components/OfflineBanner";

describe("offline read-only banner", () => {
  it("announces read-only state while the browser is offline", () => {
    const html = renderToStaticMarkup(<OfflineBanner offline />);
    expect(html).toContain('role="status"');
    expect(html).toContain("Offline: Ennote is read-only until the connection returns.");
  });

  it("renders nothing while online", () => {
    expect(renderToStaticMarkup(<OfflineBanner offline={false} />)).toBe("");
  });
});
