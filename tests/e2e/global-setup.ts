import type { FullConfig } from "@playwright/test";

/**
 * Next.js 16 dev mode renders a floating DevTools indicator inside a
 * <nextjs-portal> overlay. On narrow viewports the portal sits on top of the
 * composer and swallows pointer events, which breaks mobile E2E clicks.
 * The dev server exposes a dedicated endpoint to disable the indicator for
 * its process lifetime (default cooldown 24h). Production builds have no such
 * endpoint, so the request failing there is expected and ignored.
 */
export default async function globalSetup(config: FullConfig) {
  const baseURL = config.projects[0]?.use?.baseURL;
  if (!baseURL || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(baseURL)) return;
  try {
    await fetch(`${baseURL}/__nextjs_disable_dev_indicator`, { method: "POST" });
  } catch {
    // Non-dev server (static build / production proxy): nothing to disable.
  }
}
