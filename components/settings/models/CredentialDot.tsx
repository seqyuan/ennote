"use client";

import { useT } from "@/components/LocaleProvider";

/**
 * The API-key state dot shown on a provider row: a green solid dot only when a
 * credential is confirmed configured, and a red solid dot only when a named
 * reference is confirmed missing. Both are accessible (`role="img"` + label).
 */
export function CredentialDot({ configured, missing }: {
  configured?: boolean;
  missing?: boolean;
}) {
  const t = useT();
  if (configured) {
    return (
      <span
        className="settings-models-dot settings-models-dot-ok"
        role="img"
        aria-label={t("settings.models.credentialConfigured")}
        title={t("settings.models.credentialConfigured")}
      />
    );
  }
  if (missing) {
    return (
      <span
        className="settings-models-dot settings-models-dot-missing"
        role="img"
        aria-label={t("settings.models.credentialMissing")}
        title={t("settings.models.credentialMissing")}
      />
    );
  }
  return null;
}
