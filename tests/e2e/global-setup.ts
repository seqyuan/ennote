import { mkdirSync, writeFileSync } from "node:fs";
import type { FullConfig } from "@playwright/test";

/**
 * Next.js 16 dev mode renders a floating DevTools indicator inside a
 * <nextjs-portal> overlay. On narrow viewports the portal sits on top of the
 * composer and swallows pointer events, which breaks mobile E2E clicks.
 * The dev server exposes a dedicated endpoint to disable the indicator for
 * its process lifetime (default cooldown 24h). Production builds have no such
 * endpoint, so the request failing there is expected and ignored.
 */
async function disableDevIndicator(baseURL: string) {
  try {
    await fetch(`${baseURL}/__nextjs_disable_dev_indicator`, { method: "POST" });
  } catch {
    // Non-dev server (static build / production proxy): nothing to disable.
  }
}

/**
 * The gate enforces a fail-closed local auth boundary (setup screen before a
 * password exists, login screen after). Most specs mock the worker API but
 * still navigate through "/", so every context needs a valid session cookie.
 * Seed a password if needed, log in against the running gate, and persist the
 * session cookie as a Playwright storageState for all tests. The gate's
 * session store is in-memory, so this must run against the same gate process
 * that serves the tests.
 */
const STORAGE_PATH = ".auth/ennote-auth.json";
const PASSWORD = process.env.ENNOTE_E2E_PASSWORD ?? "preview1234";

async function seedAuth(baseURL: string): Promise<boolean> {
  const origin = new URL(baseURL).origin;
  const headers = { Origin: origin, "Content-Type": "application/json" };
  try {
    const statusRes = await fetch(`${origin}/api/auth/status`, { headers: { Accept: "application/json" } });
    if (!statusRes.ok) return false;
    const status = (await statusRes.json()) as { requiresPassword: boolean; authenticated: boolean };
    if (!status.requiresPassword) {
      const setupRes = await fetch(`${origin}/api/auth/setup`, {
        method: "POST", headers, body: JSON.stringify({ password: PASSWORD }),
      });
      if (!setupRes.ok && setupRes.status !== 409) return false;
    }
    if (status.authenticated) return true;
    const loginRes = await fetch(`${origin}/api/auth/login`, {
      method: "POST", headers, body: JSON.stringify({ password: PASSWORD }),
    });
    if (!loginRes.ok) return false;
    const setCookie = loginRes.headers.get("set-cookie");
    if (!setCookie) return false;
    const [nameValue] = setCookie.split(";");
    const [name, value] = nameValue.split("=");
    const hostname = new URL(baseURL).hostname;
    const storage = {
      cookies: [{
        name, value, domain: hostname, path: "/",
        expires: -1, httpOnly: true, secure: false, sameSite: "Lax" as const,
      }],
      // Seed the onboarding-dismissed flag: first-run guidance would
      // otherwise auto-open the Settings dialog (no providers in tests)
      // and its backdrop intercepts clicks for every spec.
      origins: [{
        origin: new URL(baseURL).origin,
        localStorage: [{ name: "ennote-onboarding-done", value: "1" }],
      }],
    };
    mkdirSync(".auth", { recursive: true });
    writeFileSync(STORAGE_PATH, JSON.stringify(storage));
    return true;
  } catch {
    return false;
  }
}

export default async function globalSetup(config: FullConfig) {
  const baseURL = config.projects[0]?.use?.baseURL;
  if (!baseURL || !/^https?:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(baseURL)) return;
  await disableDevIndicator(baseURL);
  await seedAuth(baseURL);
}
