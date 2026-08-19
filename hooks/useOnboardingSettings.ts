"use client";

import { useCallback, useEffect, useRef } from "react";
import type { ProviderProfile, SettingsTab } from "@/components/settings/types";

/**
 * First-run onboarding: when no provider is usable yet, open the Models
 * settings section once so the user can configure a provider instead of
 * staring at an inert composer. Mirrors dsh's onboarding step, which opens
 * the Models section explicitly; the plain trigger lands on General.
 *
 * The guidance only fires once: closing the dialog is treated as a
 * deliberate dismissal (persisted in localStorage), so refreshing the
 * page never re-traps the user.
 */
const ONBOARDING_DONE_KEY = "ennote-onboarding-done";

export function useOnboardingSettings({
  loading,
  error,
  providers,
  openSettings,
  closeSettings,
}: {
  loading: boolean;
  error: string | null;
  providers: ProviderProfile[];
  openSettings: (tab?: SettingsTab) => void;
  closeSettings: () => void;
}) {
  const autoOpenedSettings = useRef(false);

  // Dismiss is a deliberate user action: remember it so the guidance never
  // reopens after a refresh.
  const closeSettingsOnboarding = useCallback(() => {
    try {
      window.localStorage.setItem(ONBOARDING_DONE_KEY, "1");
    } catch {
      // Storage unavailable (private mode): the in-memory ref still guards
      // the current page load.
    }
    closeSettings();
  }, [closeSettings]);

  useEffect(() => {
    if (autoOpenedSettings.current || loading || error) return;
    if (providers.some((provider) => provider.credentialConfigured)) return;
    let dismissed = false;
    try {
      dismissed = window.localStorage.getItem(ONBOARDING_DONE_KEY) === "1";
    } catch {
      // Storage unavailable: fall through and open once for this page load.
    }
    if (dismissed) return;
    autoOpenedSettings.current = true;
    openSettings("models");
  }, [loading, error, providers, openSettings]);

  return { closeSettings: closeSettingsOnboarding };
}
