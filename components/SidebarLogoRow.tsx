"use client";

import { X } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

export function SidebarLogoRow(props: {
  collapsed: boolean;
  onToggleSidebar: () => void;
  onCloseNavigation: () => void;
}) {
  const { collapsed, onToggleSidebar, onCloseNavigation } = props;
  const t = useT();
  return (
    <div className="sidebar-logo-row">
      <div className="sidebar-brand">
        <span className="brand-mark">E</span>
        {!collapsed && <strong>Ennote</strong>}
      </div>

      {/* Desktop collapse/expand (dsh panel-left toggle). Mobile drawer has its
          own close X; this button is hidden below 641px via CSS. */}
      <button
        type="button"
        className="sidebar-collapse-toggle"
        onClick={onToggleSidebar}
        title={collapsed ? t("sidebar.expand") : t("sidebar.collapse")}
        aria-label={collapsed ? t("sidebar.openNav") : t("sidebar.collapseNav")}
        aria-expanded={!collapsed}
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
