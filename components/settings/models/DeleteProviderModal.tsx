"use client";

import { useT } from "@/components/LocaleProvider";

/**
 * Confirmation dialog for deleting a provider. Names the provider in the title,
 * description, and final action, and shows a retryable failure inline when the
 * deletion does not land.
 */
export function DeleteProviderModal({ open, providerName, busy, failure, onCancel, onConfirm }: {
  open: boolean;
  providerName: string;
  busy: boolean;
  failure?: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const t = useT();
  if (!open) return null;
  const title = t("settings.models.deleteTitle").replace("{provider}", providerName);
  const description = t("settings.models.deleteDescriptionWithCredential").replace("{provider}", providerName);
  const confirm = t("settings.models.deleteConfirm").replace("{provider}", providerName);
  const deleting = t("settings.models.deleting").replace("{provider}", providerName);
  return (
    <div className="settings-overlay" style={{ display: "grid", placeItems: "center" }} role="dialog" aria-modal="true" aria-label={title}>
      <div className="project-create-dialog" style={{ maxWidth: 420 }}>
        <div className="project-create-header">
          <span>{title}</span>
          <button type="button" className="follow-up-close" aria-label={t("settings.models.close")} title={t("settings.models.close")} onClick={onCancel}>✕</button>
        </div>
        <div className="project-create-form">
          <p style={{ margin: 0, fontSize: 13, lineHeight: "20px", color: "var(--stg-text-secondary)" }}>{description}</p>
          {failure && <p style={{ margin: 0, fontSize: 12, lineHeight: "18px", color: "var(--stg-danger)" }}>{failure}</p>}
          <div className="project-create-actions" style={{ justifyContent: "flex-end" }}>
            <button type="button" className="secondary-btn" disabled={busy} onClick={onCancel}>{t("settings.models.cancel")}</button>
            <button type="button" className="project-create-submit" disabled={busy} onClick={onConfirm}>
              {busy ? deleting : confirm}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
