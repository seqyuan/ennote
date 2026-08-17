"use client";

import { Menu } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

/**
 * Floating top-left expand button shown while the desktop sidebar is fully
 * collapsed (0 width — no rail). Sits where the topbar hamburger would be;
 * the collapsed shell reserves room so titles don't slide under it.
 */
export function SidebarExpandFab({ onClick }: { onClick: () => void }) {
  const t = useT();
  return (
    <button
      type="button"
      className="sidebar-expand-fab"
      aria-label={t("sidebar.openNav")}
      title={t("sidebar.expand")}
      aria-expanded="false"
      aria-controls="workspace-navigation"
      onClick={onClick}
    >
      <Menu size={18} aria-hidden="true" />
    </button>
  );
}
