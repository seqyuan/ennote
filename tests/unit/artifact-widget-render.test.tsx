import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ArtifactView } from "@/components/ArtifactView";
import {
  WIDGET_FRAME_SANDBOX, WIDGET_MAX_HEIGHT, isWidgetReady, parseWidgetError, parseWidgetHeight,
  readCachedWidgetHeight, rememberWidgetHeight, resetWidgetHeightCache,
} from "@/lib/widget-frame";
import { isWidgetArtifact } from "@/lib/artifacts";
import { WIDGET_THEME_VARS, resolveWidgetThemeVars } from "@/lib/widget-theme";
import type { ArtifactReference } from "@/lib/artifacts";

// The frame's policy is an attribute, so the markup is the assertion. Interaction
// (height reporting, error banners, retry) belongs to Playwright; vitest runs in a
// node environment with no DOM for it to drive.
function widget(overrides: Partial<ArtifactReference> = {}): ArtifactReference {
  return {
    artifactId: "artifact-widget",
    name: "canvas.html",
    kind: "widget",
    mimeType: "text/html; charset=utf-8",
    sizeBytes: 2048,
    sha256: "widget-sha",
    ...overrides,
  };
}

describe("artifact widget frame", () => {
  it("renders a script-enabled frame with an opaque origin", () => {
    const html = renderToStaticMarkup(<ArtifactView sessionId="session-1" artifact={widget()} />);
    expect(html).toContain("<iframe");
    expect(html).toContain('sandbox="allow-scripts"');
    // allow-same-origin beside allow-scripts would let the frame reach
    // parent.document and strip its own sandbox.
    expect(html).not.toContain("allow-same-origin");
    expect(html).not.toContain("allow-popups");
    expect(html).not.toContain("allow-top-navigation");
    expect(html).toContain('referrerPolicy="no-referrer"');
    expect(html).toContain("/api/worker/v1/sessions/session-1/artifacts/artifact-widget/preview");
    expect(html).toContain('data-widget-artifact="artifact-widget"');
    expect(html).toContain('data-widget-state="loading"');
  });

  it("keeps static_html on the script-free sandbox", () => {
    const html = renderToStaticMarkup(<ArtifactView sessionId="session-1"
      artifact={widget({ artifactId: "artifact-page", kind: "static_html", name: "report.html" })} />);
    expect(html).toContain('sandbox=""');
    expect(html).not.toContain("allow-scripts");
  });

  it("marks only the widget kind as a widget, and gives it its own icon", () => {
    expect(isWidgetArtifact({ kind: "widget" })).toBe(true);
    for (const kind of ["image", "table", "static_html", "text", "file"]) {
      expect(isWidgetArtifact({ kind })).toBe(false);
    }
    const html = renderToStaticMarkup(<ArtifactView sessionId="session-1" artifact={widget()} />);
    expect(html).toContain("lucide-sparkles");
  });

  it("offers no way to open the canvas from inside the canvas", () => {
    // The lane draws this same section for the artifact it already contains; a
    // second "open in the canvas" there would be a control that does nothing.
    const inMessage = renderToStaticMarkup(<ArtifactView sessionId="session-1" artifact={widget()} />);
    const inCanvas = renderToStaticMarkup(<ArtifactView sessionId="session-1" artifact={widget()} inCanvas />);
    expect(inMessage).toContain("artifact-canvas-open");
    expect(inCanvas).not.toContain("artifact-canvas-open");
    // Everything else is the same surface, so the lane and the message cannot drift.
    expect(inCanvas).toContain('sandbox="allow-scripts"');
    expect(inCanvas).toContain("artifact-download");
  });

  it("carries the sandbox constant the Worker's CSP expects", () => {
    expect(WIDGET_FRAME_SANDBOX).toBe("allow-scripts");
    expect(WIDGET_FRAME_SANDBOX.split(/\s+/)).toEqual(["allow-scripts"]);
  });
});

describe("widget frame messages", () => {
  it("accepts a positive finite height and clamps it to the message width budget", () => {
    expect(parseWidgetHeight({ type: "ennote:widget:height", height: 420 })).toBe(420);
    expect(parseWidgetHeight({ type: "ennote:widget:height", height: 900.4 })).toBe(900);
    expect(parseWidgetHeight({ type: "ennote:widget:height", height: 100000 })).toBe(WIDGET_MAX_HEIGHT);
  });

  it("drops anything that is not a usable height", () => {
    for (const payload of [
      { type: "ennote:widget:height", height: Number.NaN },
      { type: "ennote:widget:height", height: Number.POSITIVE_INFINITY },
      { type: "ennote:widget:height", height: -1 },
      { type: "ennote:widget:height", height: 0 },
      { type: "ennote:widget:height", height: "100" },
      { type: "ennote:widget:height" },
      { type: "something-else", height: 100 },
      null,
      undefined,
      42,
      "100",
    ]) {
      expect(parseWidgetHeight(payload)).toBeNull();
    }
  });

  it("classifies a blocked dependency apart from a script throw", () => {
    expect(parseWidgetError({ type: "ennote:widget:error", kind: "csp", detail: "script-src https://x" }))
      .toEqual({ kind: "csp", detail: "script-src https://x" });
    expect(parseWidgetError({ type: "ennote:widget:error", detail: "boom" }))
      .toEqual({ kind: "script", detail: "boom" });
    expect(parseWidgetError({ type: "ennote:widget:error", detail: "x".repeat(500) })?.detail).toHaveLength(200);
    expect(parseWidgetError({ type: "ennote:widget:height", height: 10 })).toBeNull();
  });

  it("recognizes the ready message", () => {
    expect(isWidgetReady({ type: "ennote:widget:ready" })).toBe(true);
    expect(isWidgetReady({ type: "ennote:widget:height", height: 10 })).toBe(false);
  });
});

describe("widget height cache", () => {
  it("remembers a reported height across a remount", () => {
    resetWidgetHeightCache();
    expect(readCachedWidgetHeight("artifact-widget")).toBe(0);
    rememberWidgetHeight("artifact-widget", 420);
    expect(readCachedWidgetHeight("artifact-widget")).toBe(420);
    // A second artifact must not inherit the first one's height.
    expect(readCachedWidgetHeight("artifact-other")).toBe(0);
  });

  it("refuses to cache a height it would not render", () => {
    resetWidgetHeightCache();
    for (const height of [0, -5, Number.NaN, Number.POSITIVE_INFINITY]) {
      rememberWidgetHeight("artifact-widget", height);
      expect(readCachedWidgetHeight("artifact-widget")).toBe(0);
    }
  });
});

describe("widget theme bridge", () => {
  const tokens = new Map(WIDGET_THEME_VARS.map(({ token }) => [token, `value-of-${token}`]));

  it("publishes exactly the documented property set", () => {
    const vars = resolveWidgetThemeVars(token => tokens.get(token) ?? "");
    expect(Object.keys(vars).sort()).toEqual([
      "--color-accent",
      "--color-background-danger",
      "--color-background-info",
      "--color-background-primary",
      "--color-background-secondary",
      "--color-background-success",
      "--color-background-tertiary",
      "--color-background-warning",
      "--color-border-primary",
      "--color-border-secondary",
      "--color-border-tertiary",
      "--color-chart-1",
      "--color-chart-2",
      "--color-chart-3",
      "--color-chart-4",
      "--color-chart-5",
      "--color-chart-6",
      "--color-text-danger",
      "--color-text-info",
      "--color-text-primary",
      "--color-text-secondary",
      "--color-text-success",
      "--color-text-tertiary",
      "--color-text-warning",
      "--font-mono",
      "--font-sans",
      "--radius-lg",
      "--radius-md",
      "--radius-sm",
    ]);
    expect(vars["--color-background-primary"]).toBe("value-of---surface-0");
    expect(vars["--font-mono"]).toBe("value-of---font-mono");
  });

  it("omits tokens the host does not define instead of sending an empty value", () => {
    const vars = resolveWidgetThemeVars(token => (token === "--accent" ? "  " : "x"));
    expect(vars).not.toHaveProperty("--color-accent");
    expect(vars["--color-text-primary"]).toBe("x");
  });

  it("never exposes a token as a property the frame could not name", () => {
    for (const { name } of WIDGET_THEME_VARS) expect(name.startsWith("--")).toBe(true);
  });
});
