"use client";

import { useId, type ReactNode } from "react";

function cx(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(" ");
}

/** A titled block inside a settings tab. */
export function SettingsSection({
  title,
  description,
  actions,
  className,
  children,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section className={cx("settings-section", className)} aria-labelledby={title ? headingId : undefined}>
      {(title || description || actions) && (
        <div className="settings-section-header">
          <div>
            {title && <h3 id={headingId}>{title}</h3>}
            {description && <p>{description}</p>}
          </div>
          {actions && <div className="settings-section-actions">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  );
}

/** A compact row with a title, optional secondary line, and trailing actions. */
export function SettingsRow({
  title,
  meta,
  actions,
  children,
  className,
}: {
  title: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("settings-row", className)}>
      <div className="settings-row-copy">
        <strong>{title}</strong>
        {meta && <span>{meta}</span>}
      </div>
      {children}
      {actions && <div className="settings-row-actions">{actions}</div>}
    </div>
  );
}

export function SettingsList({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("settings-list", className)}>{children}</div>;
}

/** A bordered card for a grouped editor or a dense sub-section. */
export function SettingsCard({
  title,
  description,
  actions,
  className,
  children,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cx("settings-card", className)}>
      {(title || description || actions) && (
        <div className="settings-card-header">
          <div>
            {title && <div className="settings-card-title">{title}</div>}
            {description && <div className="settings-card-description">{description}</div>}
          </div>
          {actions && <div className="settings-section-actions">{actions}</div>}
        </div>
      )}
      {children}
    </div>
  );
}

export function SettingsActions({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("settings-actions", className)}>{children}</div>;
}
