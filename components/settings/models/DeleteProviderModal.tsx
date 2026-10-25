"use client";

import { X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import { useOverlayLayer } from "@/hooks/useOverlayLayer";

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
  useOverlayLayer(open, onCancel, { dismissOnEscape: !busy });
  if (!open) return null;
  const title = t("settings.models.deleteTitle").replace("{provider}", providerName);
  const description = t("settings.models.deleteDescriptionWithCredential").replace("{provider}", providerName);
  const confirm = t("settings.models.deleteConfirm").replace("{provider}", providerName);
  const deleting = t("settings.models.deleting").replace("{provider}", providerName);
  return (
    <div className="settings-overlay" role="dialog" aria-modal="true" aria-label={title}>
      <div className="project-create-dialog">
        <div className="project-create-header">
          <span>{title}</span>
          <button type="button" className="follow-up-close" aria-label={t("settings.models.close")} title={t("settings.models.close")} onClick={onCancel}><X size={14} aria-hidden="true" /></button>
        </div>
        <div className="project-create-form">
          <p className="settings-delete-description">{description}</p>
          {failure && <p className="settings-delete-error">{failure}</p>}
          <div className="project-create-actions">
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
