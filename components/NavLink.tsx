"use client";

/**
 * Sidebar nav item for view switching (Roles / Graphs). Rendered as a
 * button instead of a Link so activating it only swaps the main-area
 * view inside AppShell — the sidebar stays mounted and the URL is
 * updated via history.replaceState (no page navigation).
 */
export function NavLink({ active, label, icon, onClick }: {
  active: boolean;
  label: string;
  icon: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`sidebar-nav-item ${active ? "active" : ""}`}
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      <span className="nav-icon">{icon}</span>
      <span>{label}</span>
    </button>
  );
}
