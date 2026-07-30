"use client";

import { useEffect, useRef, type ReactNode, type RefObject } from "react";
import { useMediaQuery } from "@/hooks/useMediaQuery";

export function WorkspaceShell({ sidebar, children, mobileNavigationOpen, onMobileNavigationChange, navigationTriggerRef }: {
  sidebar: ReactNode;
  children: ReactNode;
  mobileNavigationOpen: boolean;
  onMobileNavigationChange: (open: boolean) => void;
  navigationTriggerRef: RefObject<HTMLButtonElement | null>;
}) {
  const mobile = useMediaQuery("(max-width: 640px)");
  const navigationRef = useRef<HTMLDivElement>(null);
  const wasOpen = useRef(false);

  useEffect(() => {
    if (!mobile || !mobileNavigationOpen) {
      if (wasOpen.current) requestAnimationFrame(() => navigationTriggerRef.current?.focus());
      wasOpen.current = false;
      return;
    }
    wasOpen.current = true;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    requestAnimationFrame(() => {
      const initial = navigationRef.current?.querySelector<HTMLElement>("[data-navigation-initial-focus]");
      (initial ?? navigationRef.current?.querySelector<HTMLElement>("button"))?.focus();
    });
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onMobileNavigationChange(false);
        return;
      }
      if (event.key !== "Tab" || !navigationRef.current) return;
      const focusable = focusableElements(navigationRef.current);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      document.body.style.overflow = previousOverflow;
    };
  }, [mobile, mobileNavigationOpen, navigationTriggerRef, onMobileNavigationChange]);

  return <div className="workspace">
    <div className={`navigation-layer ${mobileNavigationOpen ? "is-open" : ""}`}
      role={mobile ? "dialog" : undefined} aria-modal={mobile && mobileNavigationOpen ? true : undefined}
      aria-label={mobile ? "Navigation" : undefined} aria-hidden={mobile && !mobileNavigationOpen ? true : undefined}>
      <div className="navigation-backdrop" onMouseDown={event => {
        if (event.target === event.currentTarget) onMobileNavigationChange(false);
      }} />
      <div className="navigation-panel" id="workspace-navigation" ref={navigationRef}>{sidebar}</div>
    </div>
    <div className="workspace-content" inert={mobile && mobileNavigationOpen ? true : undefined}
      aria-hidden={mobile && mobileNavigationOpen ? true : undefined}>
      {children}
    </div>
  </div>;
}

function focusableElements(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
  )).filter(element => element.getClientRects().length > 0);
}
