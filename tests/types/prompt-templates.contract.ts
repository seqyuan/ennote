// Compile-only contract test: verifies that generated prompt-template types
// form a proper discriminated union on `case` and that required fields are
// present. This file is NOT a test — it is only checked by `tsc` (vitest
// excludes it via the `tests/**/*.test.{ts,tsx}` include pattern).
//
// If this file type-checks:
//   - ExpandPromptTemplateResult can be narrowed by `case`.
//   - ExpandPromptTemplateMatched carries `text`.
//   - ExpandPromptTemplateNotFound carries `name`.
//   - No generator-injected discriminator property (like matched: "SchemaName").

import type { components } from "../../lib/worker-api.gen";

type ExpandMatched = components["schemas"]["ExpandPromptTemplateMatched"];
type ExpandNotFound = components["schemas"]["ExpandPromptTemplateNotFound"];
type ExpandInvalid = components["schemas"]["ExpandPromptTemplateInvalid"];
type ExpandResult = components["schemas"]["ExpandPromptTemplateResult"];

// matched: case is "matched" and text is present.
function assertMatched(r: ExpandMatched): ["matched", string] {
  return [r.case, r.text];
}

// not_found: case is "not_found" and name is present.
function assertNotFound(r: ExpandNotFound): ["not_found", string] {
  return [r.case, r.name];
}

// invalid_invocation: case is "invalid_invocation".
function assertInvalid(r: ExpandInvalid): "invalid_invocation" {
  return r.case;
}

// Narrowing: ExpandResult discriminated union must narrow on case.
function narrowResult(r: ExpandResult): string {
  switch (r.case) {
    case "matched":
      return r.text;
    case "not_found":
      return r.name;
    case "invalid_invocation":
      return "invalid";
    default:
      return "";
  }
}

// Verify all schemas are generated.
type _Summary = components["schemas"]["PromptTemplateSummary"];
type _Diag = components["schemas"]["PromptTemplateDiagnostic"];
type _Entry = components["schemas"]["GlobalPromptTemplateEntry"];
type _Catalog = components["schemas"]["PromptTemplateCatalog"];
type _MgmtCatalog = components["schemas"]["PromptTemplateManagementCatalog"];
type _Create = components["schemas"]["CreatePromptTemplateInput"];
type _Update = components["schemas"]["UpdatePromptTemplateInput"];

void assertMatched;
void assertNotFound;
void assertInvalid;
void narrowResult;
void "" as unknown as _Summary;
void "" as unknown as _Diag;
void "" as unknown as _Entry;
void "" as unknown as _Catalog;
void "" as unknown as _MgmtCatalog;
void "" as unknown as _Create;
void "" as unknown as _Update;
