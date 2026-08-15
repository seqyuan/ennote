"use client";

import { Menu, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { WorkspaceNav } from "@/components/WorkspaceNav";

export function WorkspacePageShell({ title, description, error, onDismissError, children, testId }: {
  title: string;
  description: string;
  error: string | null;
  onDismissError: () => void;
  children: ReactNode;
  testId: string;
}) {
  const [navigationOpen, setNavigationOpen] = useState(false);
  return (
    <div className="workspace-page-shell" data-testid={testId}>
      <button type="button" className="workspace-mobile-menu" onClick={() => setNavigationOpen(true)} aria-label="Open navigation"><Menu size={20} /></button>
      {navigationOpen && <button type="button" className="workspace-nav-backdrop" onClick={() => setNavigationOpen(false)} aria-label="Close navigation" />}
      <div className={`workspace-nav-container ${navigationOpen ? "is-open" : ""}`}>
        <button type="button" className="workspace-nav-close" onClick={() => setNavigationOpen(false)} aria-label="Close navigation"><X size={20} /></button>
        <WorkspaceNav onNavigate={() => setNavigationOpen(false)} />
      </div>
      <section className="workspace-page-content">
        <header className="workspace-page-header"><div><h1>{title}</h1><p>{description}</p></div></header>
        {error && <div className="error-bar workspace-page-error" role="alert"><span>{error}</span><button type="button" onClick={onDismissError} aria-label="Dismiss error"><X size={16} /></button></div>}
        <div className="workspace-page-body">{children}</div>
      </section>
    </div>
  );
}
