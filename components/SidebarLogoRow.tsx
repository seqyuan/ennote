"use client";

import { X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

export function SidebarLogoRow(props: {
  onToggleSidebar: () => void;
  onCloseNavigation: () => void;
  onNewSession: () => void;
  newSessionDisabled: boolean;
}) {
  const { onToggleSidebar, onCloseNavigation, onNewSession, newSessionDisabled } = props;
  const t = useT();
  return (
    <div className="sidebar-logo-row">
      <button
        type="button"
        className="sidebar-brand"
        disabled={newSessionDisabled}
        onClick={onNewSession}
        aria-label={t("sidebar.newChatAria")}
        title={newSessionDisabled ? t("sidebar.selectProjectFirst") : t("sidebar.newChatAria")}
      >
        <span className="sidebar-brand-identity" aria-hidden="true">
          <span className="brand-mark">E</span>
          <span className="sidebar-brand-name">Ennote</span>
        </span>
      </button>

      {/* Desktop collapse (dsh panel-left toggle). Mobile drawer has its
          own close X; this button is hidden below 641px via CSS. */}
      <button
        type="button"
        className="sidebar-collapse-toggle"
        onClick={onToggleSidebar}
        title={t("sidebar.collapse")}
        aria-label={t("sidebar.collapseNav")}
        aria-expanded="true"
        aria-controls="workspace-navigation"
      >
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="3" y="3" width="18" height="18" rx="2" />
          <line x1="9" y1="3" x2="9" y2="21" />
        </svg>
      </button>

      <button
        type="button"
        className="sidebar-close-nav navigation-close"
        onClick={onCloseNavigation}
        aria-label={t("sidebar.closeNav")}
      >
        <X size={15} />
      </button>
    </div>
  );
}
