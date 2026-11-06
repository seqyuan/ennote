import coreWebVitals from "eslint-config-next/core-web-vitals";
import typescript from "eslint-config-next/typescript";
import globals from "globals";

const eslintConfig = [
  ...coreWebVitals,
  ...typescript,
  {
    files: ["bin/**/*.js", "scripts/**/*.js", "scripts/**/*.mjs"],
    languageOptions: {
      globals: globals.node,
      sourceType: "commonjs",
    },
    rules: {
      "@typescript-eslint/no-require-imports": "off",
      "no-empty": ["error", { allowEmptyCatch: true }],
    },
  },
  {
    files: ["app/**/*.tsx", "components/**/*.tsx"],
    rules: {
      "jsx-a11y/control-has-associated-label": ["error", {
        labelAttributes: ["aria-label", "title"],
        ignoreElements: ["audio", "canvas", "embed", "input", "select", "textarea"],
      }],
    },
  },
  {
    // _site is the MkDocs output produced by `mkdocs build` (pages.yml); it
    // ships vendored theme JavaScript that the source rules do not apply to.
    ignores: ["ennoworker/**", "playwright-report/**", "test-results/**", "_site/**", "out/**", ".next/**"],
  },
];

export default eslintConfig;
