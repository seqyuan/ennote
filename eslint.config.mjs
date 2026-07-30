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
    ignores: ["ennoworker/**", "playwright-report/**", "test-results/**"],
  },
];

export default eslintConfig;
