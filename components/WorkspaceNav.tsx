"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Bot, MessageSquare, Workflow } from "lucide-react";

export function WorkspaceNav({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  const navItems = [
    { href: "/", label: "Chat", icon: MessageSquare, active: pathname === "/" },
    { href: "/roles", label: "Roles", icon: Bot, active: pathname === "/roles" },
    { href: "/graphs", label: "Graphs", icon: Workflow, active: pathname === "/graphs" },
  ];

  return (
    <aside className="workspace-nav" aria-label="Workspace navigation">
      <div className="workspace-nav-brand"><span className="brand-mark">E</span><strong>Ennote</strong></div>
      <nav aria-label="Primary">
        {navItems.map(({ href, label, icon: Icon, active }) => (
          <Link key={href} href={href} className={active ? "active" : ""} aria-current={active ? "page" : undefined} onClick={onNavigate}>
            <Icon size={18} /><span>{label}</span>
          </Link>
        ))}
      </nav>
      <div className="workspace-nav-foot">Worker-global tools</div>
    </aside>
  );
}
