"use client";

import { Plus } from "lucide-react";

export function NewSessionButton(props: {
  disabled: boolean;
  collapsed: boolean;
  onClick: () => void;
}) {
  const { disabled, collapsed, onClick } = props;
  return (
    <button
      type="button"
      className="sidebar-new-session"
      disabled={disabled}
      onClick={onClick}
      aria-label="New chat"
      title={disabled ? "Select a project first" : "New chat"}
    >
      <Plus size={14} aria-hidden="true" />
      {!collapsed && <span>New Chat</span>}
    </button>
  );
}
