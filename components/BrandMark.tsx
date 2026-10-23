/**
 * Ennote brand mark. One SVG source for the sidebar and the empty hero; do not
 * render a text glyph as the logo (font fallback changes its shape and weight).
 */
export function BrandMark({ size = 24 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      <rect x="1" y="1" width="30" height="30" rx="9" fill="currentColor" opacity="0.10" />
      <path
        d="M11 9.5h10.5M11 16h8M11 22.5h10.5M11 9.5v13"
        stroke="currentColor"
        strokeWidth="2.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
