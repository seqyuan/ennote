import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { OfflineBanner } from "@/components/OfflineBanner";

describe("offline read-only banner", () => {
  it("announces read-only state while the Worker is unreachable", () => {
    const html = renderToStaticMarkup(<OfflineBanner workerUnreachable />);
    expect(html).toContain('role="status"');
    expect(html).toContain("Worker unreachable");
    expect(html).toContain("read-only until it returns");
  });

  it("renders nothing while the Worker answers", () => {
    expect(renderToStaticMarkup(<OfflineBanner workerUnreachable={false} />)).toBe("");
  });
});
