"use client";

import {
  forwardRef,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type ReactNode,
} from "react";
import styles from "./primitives.module.css";

type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

function cx(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(" ");
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "sm" | "md";
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", className, type = "button", ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      className={cx(styles.button, styles[variant], styles[size], className)}
      {...props}
    />
  );
});

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Required accessible name for icon-only controls. */
  label: string;
  title?: string;
  size?: "sm" | "md";
  active?: boolean;
  children: ReactNode;
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, title, size = "md", active = false, className, type = "button", children, ...props },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      title={title ?? label}
      data-active={active || undefined}
      className={cx(styles.iconButton, size === "sm" && styles.iconButtonSm, className)}
      {...props}
    >
      {children}
    </button>
  );
});

export function Badge({
  tone = "neutral",
  className,
  children,
  ...props
}: HTMLAttributes<HTMLSpanElement> & { tone?: Tone }) {
  const toneClass = {
    neutral: styles.badgeNeutral,
    accent: styles.badgeAccent,
    success: styles.badgeSuccess,
    warning: styles.badgeWarning,
    danger: styles.badgeDanger,
  }[tone];
  return <span className={cx(styles.badge, toneClass, className)} {...props}>{children}</span>;
}

export function Card({
  elevated = false,
  padded = true,
  className,
  children,
  ...props
}: HTMLAttributes<HTMLDivElement> & { elevated?: boolean; padded?: boolean }) {
  return (
    <div
      className={cx(styles.card, padded && styles.cardPad, elevated && styles.cardElevated, className)}
      {...props}
    >
      {children}
    </div>
  );
}

export function Banner({
  tone = "neutral",
  className,
  children,
  ...props
}: HTMLAttributes<HTMLDivElement> & { tone?: Tone }) {
  const toneClass = {
    neutral: styles.bannerNeutral,
    accent: styles.bannerNeutral,
    success: styles.bannerSuccess,
    warning: styles.bannerWarning,
    danger: styles.bannerDanger,
  }[tone];
  return <div className={cx(styles.banner, toneClass, className)} {...props}>{children}</div>;
}

export function EmptyState({
  title,
  description,
  action,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx(styles.emptyState, className)}>
      <p className={styles.emptyTitle}>{title}</p>
      {description && <p className={styles.emptyDescription}>{description}</p>}
      {action && <div className={styles.emptyAction}>{action}</div>}
    </div>
  );
}
