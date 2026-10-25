"use client";

import { PanelLeft, X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";
import { BrandMark } from "@/components/BrandMark";

/**
 * Brand row. The brand is not a destructive/primary action: New Chat lives in
 * NewSessionButton immediately below. Keeping the logo inert avoids two
 * competing "create session" affordances in the first 100px of the sidebar.
 */
export function SidebarLogoRow(props: {
  onToggleSidebar: () => void;
  onCloseNavigation: () => void;
}) {
  const { onToggleSidebar, onCloseNavigation } = props;
  const t = useT();
  return (
    <div className="sidebar-logo-row">
      <div className="sidebar-brand">
        <span className="sidebar-brand-identity">
          <span className="brand-mark"><BrandMark size={20} /></span>
          <span className="sidebar-brand-name">Ennote</span>
        </span>
      </div>

      {/* Desktop collapse (panel-left toggle). Mobile drawer has its
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
        <PanelLeft size={16} aria-hidden="true" />
      </button>

      <button
        type="button"
        className="sidebar-close-nav navigation-close"
        onClick={onCloseNavigation}
        aria-label={t("sidebar.closeNav")}
      >
        <X size={15} aria-hidden="true" />
      </button>
    </div>
  );
}
