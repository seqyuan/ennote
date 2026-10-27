#!/usr/bin/env node
/**
 * Design-token guard.
 *
 * Feature CSS must consume app/tokens.css instead of introducing another
 * palette or radius scale. This check is intentionally narrow: it catches the
 * regressions that made the UI drift (off-scale radii, literal colors, and
 * sub-11px UI text) without trying to police every layout value.
 *
 * Allowed literals:
 *   - 1–2px radii for scrollbars, swatches, and hairline tracks
 *   - #fff / #000 for document surfaces and masks
 *   - percentages (circles) and var(--…) values
 */

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = process.cwd();
const TOKEN_FILE = join(ROOT, "app", "tokens.css");
const SCAN_ROOTS = ["app", "components"];
const ALLOWED_RADII = new Set([0, 1, 2]);
const ALLOWED_COLORS = new Set(["#fff", "#ffffff", "#000", "#000000"]);

// Neutral scrim/shadow ink may stay literal; everything else must come from a
// semantic token so the data-viz palette cannot drift back into components.
function isNeutralRgb(value) {
  const match = value.match(/^rgba?\(\s*(\d+)\s*[\s,/]\s*(\d+)\s*[\s,/]\s*(\d+)/i);
  if (!match) return false;
  const [r, g, b] = match.slice(1, 4).map(Number);
  return (r === 0 && g === 0 && b === 0)
    || (r === 255 && g === 255 && b === 255)
    || (r === 24 && g === 24 && b === 27);
}

function* walk(dir, extension) {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    const stat = statSync(path);
    if (stat.isDirectory()) {
      if (entry === "node_modules" || entry.startsWith(".")) continue;
      yield* walk(path, extension);
    } else if (entry.endsWith(extension)) {
      yield path;
    }
  }
}

const errors = [];

for (const root of SCAN_ROOTS) {
  for (const file of walk(join(ROOT, root), ".css")) {
    if (file === TOKEN_FILE) continue;
    const rel = relative(ROOT, file);
    const text = readFileSync(file, "utf8");
    const lines = text.split("\n");

    lines.forEach((line, index) => {
      const where = `${rel}:${index + 1}`;

      for (const match of line.matchAll(/border-radius:\s*(-?\d+)px/g)) {
        const value = Number(match[1]);
        if (!ALLOWED_RADII.has(value)) {
          errors.push(`${where} off-scale border-radius ${value}px; use var(--radius-*)`);
        }
      }

      for (const match of line.matchAll(/font-size:\s*(\d+)px/g)) {
        const value = Number(match[1]);
        if (value > 0 && value < 11) {
          errors.push(`${where} font-size ${value}px is below the 11px UI floor; use var(--text-*)`);
        }
      }

      for (const match of line.matchAll(/#[0-9a-fA-F]{3,8}\b/g)) {
        const value = match[0].toLowerCase();
        if (!ALLOWED_COLORS.has(value)) {
          errors.push(`${where} literal color ${match[0]}; use a semantic token`);
        }
      }

      for (const match of line.matchAll(/rgba?\([^)]*\)/gi)) {
        if (!isNeutralRgb(match[0])) {
          errors.push(`${where} literal color ${match[0]}; use a semantic token`);
        }
      }
    });
  }
}

// Inline styles in components follow the same scale. Keep the checks narrow:
// font sizes, numeric radii, and literal colors that bypass the token layer.
// The same pass rejects Unicode glyphs used as icon buttons; SVG icons are the
// only allowed icon source.
for (const root of SCAN_ROOTS) for (const file of walk(join(ROOT, root), ".tsx")) {
  const rel = relative(ROOT, file);
  const text = readFileSync(file, "utf8");
  const lines = text.split("\n");

  lines.forEach((line, index) => {
    const where = `${rel}:${index + 1}`;

    for (const match of line.matchAll(/fontSize:\s*(\d+(?:\.\d+)?)/g)) {
      const value = Number(match[1]);
      if (value > 0 && value < 11) {
        errors.push(`${where} inline font-size ${value} is below the 11px UI floor; use a token or 11+`);
      }
    }

    for (const match of line.matchAll(/borderRadius:\s*(\d+)\b/g)) {
      const value = Number(match[1]);
      if (!ALLOWED_RADII.has(value)) {
        errors.push(`${where} inline border-radius ${value}; use var(--radius-*)`);
      }
    }

    for (const match of line.matchAll(/#[0-9a-fA-F]{3,8}\b/g)) {
      const value = match[0].toLowerCase();
      if (!ALLOWED_COLORS.has(value)) {
        errors.push(`${where} literal color ${match[0]} in a component; use a semantic token`);
      }
    }

    if (/>\s*[✕×]\s*</.test(line)) {
      errors.push(`${where} uses a text glyph as an icon; render an SVG icon instead`);
    }
  });
}

if (errors.length > 0) {
  console.error("Design token guard failed:\n");
  for (const error of errors) console.error(`  - ${error}`);
  console.error(`\n${errors.length} issue(s). Shared values live in app/tokens.css.`);
  process.exit(1);
}

console.log("Design token guard passed.");
