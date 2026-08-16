"use client";

import { useT } from "@/components/LocaleProvider";

/**
 * The API-key state dot shown on a provider row: a green solid dot only when a
 * credential is confirmed configured, and a red solid dot only when a named
 * reference is confirmed missing. Both are accessible (`role="img"` + label).
 * Provider-native authentication stays unmarked, so this renders null for it.
 */
export function CredentialDot({ configured, missing }: {
  configured?: boolean;
  missing?: boolean;
}) {
  const t = useT();
  if (configured) {
    return (
      <span
        role="img"
        aria-label={t("settings.models.credentialConfigured")}
        title={t("settings.models.credentialConfigured")}
        style={{ width: 8, height: 8, borderRadius: "50%", background: "#2f9e44", display: "inline-block", flexShrink: 0 }}
      />
    );
  }
  if (missing) {
    return (
      <span
        role="img"
        aria-label={t("settings.models.credentialMissing")}
        title={t("settings.models.credentialMissing")}
        style={{ width: 8, height: 8, borderRadius: "50%", background: "var(--stg-danger)", display: "inline-block", flexShrink: 0 }}
      />
    );
  }
  return null;
}
