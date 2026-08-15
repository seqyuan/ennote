"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

export function NavLink({ href, label, icon }: { href: string; label: string; icon: React.ReactNode }) {
  const pathname = usePathname();
  const active = pathname === href;
  return (
    <Link
      href={href}
      className={`sidebar-item ${active ? "active" : ""}`}
      style={{
        display: "flex", alignItems: "center", gap: 8,
        padding: "6px 10px", borderRadius: 7,
        color: active ? "var(--text)" : "var(--text-muted)",
        fontSize: 12, fontWeight: active ? 600 : 400,
        textDecoration: "none",
      }}
    >
      <span style={{ color: active ? "var(--accent)" : "inherit", display: "flex", flexShrink: 0 }}>{icon}</span>
      {label}
    </Link>
  );
}
