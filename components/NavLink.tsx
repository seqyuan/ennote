"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

export function NavLink({ href, label, icon }: { href: string; label: string; icon: React.ReactNode }) {
  const pathname = usePathname();
  const active = pathname === href;
  return (
    <Link href={href} className={`sidebar-nav-item ${active ? "active" : ""}`} aria-current={active ? "page" : undefined}>
      <span className="nav-icon">{icon}</span>
      <span>{label}</span>
    </Link>
  );
}
