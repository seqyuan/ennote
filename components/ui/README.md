# UI primitives and design tokens

The shell has **one** visual owner:

- `app/tokens.css` — semantic surfaces, content colors, strokes, states,
  radius/spacing/type scales, elevation, and motion. Legacy aliases
  (`--bg`, `--text`, `--border`, …) resolve to the same layer.
- `components/ui/` — interaction atoms (`Button`, `IconButton`, `Badge`,
  `Card`, `Banner`, `EmptyState`) built from those tokens.
- `app/*.css` — feature layout. It consumes tokens; it must not define a
  second palette, radius scale, or sub-11px UI type.

## Rules

1. Use `var(--surface-*)`, `var(--content-*)`, `var(--stroke-*)`,
   `var(--radius-*)`, `var(--text-*)`, `var(--space-*)`, `var(--elevation-*)`,
   and `var(--motion-*)`.
2. Interactive icons are SVG components (Lucide or one shared brand mark).
   Do not use emoji, text glyphs, or one-off inline SVG as controls.
3. Icon-only controls need an accessible label and a ≥24px hit target.
4. Menus and dialogs restore focus, close on Escape, and use roving focus for
   arrow-key navigation.
5. Elevated surfaces use an `--elevation-*` shadow and no additional neutral
   border; flat surfaces use `--stroke-hairline`.
6. `npm run lint` runs `scripts/check-design-tokens.mjs`, which rejects
   off-scale radii, literal colors, and UI text below 11px. Add a token when a
   value is genuinely shared; do not add a local exception.
