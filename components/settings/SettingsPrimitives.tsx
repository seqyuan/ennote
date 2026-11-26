"use client";

import { Loader2 } from "lucide-react";
import { useId, type ReactNode } from "react";

function cx(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(" ");
}

/** A titled block inside a settings tab. The third heading level: a tab's own
 *  title is its h2 and a card sits below this one as an h4. */
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

/** A bordered card for a grouped editor or a dense sub-section. Its title is
 *  an h4 rather than a fifth level chosen by the caller: a card only ever sits
 *  inside a section, so the level is known here and cannot drift out of order. */
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
            {title && <h4 className="settings-card-title">{title}</h4>}
            {description && <p className="settings-card-description">{description}</p>}
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

/** A block that is still fetching. Announced once as a status; the label is the
 *  only text, so a caller that already says "Loading …" keeps its wording. */
export function SettingsLoading({ label, className }: { label: ReactNode; className?: string }) {
  return (
    <div className={cx("settings-loading", className)} role="status">
      <Loader2 className="spin" size={14} aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

/** A block that has nothing to list. Plain text, no extra semantics. */
export function SettingsEmpty({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("settings-empty", className)}>{children}</div>;
}
