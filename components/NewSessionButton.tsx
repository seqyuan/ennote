"use client";

import { Plus } from "lucide-react";
import { useT } from "@/components/LocaleProvider";

export function NewSessionButton(props: {
  disabled: boolean;
  collapsed: boolean;
  onClick: () => void;
}) {
  const { disabled, collapsed, onClick } = props;
  const t = useT();
  return (
    <button
      type="button"
      className="sidebar-new-session"
      disabled={disabled}
      onClick={onClick}
      aria-label={t("sidebar.newChatAria")}
      title={disabled ? t("sidebar.selectProjectFirst") : t("sidebar.newChatAria")}
    >
      <Plus size={14} aria-hidden="true" />
      {!collapsed && <span>{t("sidebar.newChat")}</span>}
    </button>
  );
}
