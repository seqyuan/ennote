import type { operations } from "@/lib/worker-api.gen";

/**
 * The response body an operation declares for a status, as the contract spells it.
 *
 * tests/contract-samples.gen.ts annotates one stored Worker answer per constant
 * with this type, which is where a handler that stopped matching its own contract
 * fails `tsc` instead of a client that reads `undefined`.
 *
 * The two lookups are constrained so a typo in a sample's name is a compile error
 * rather than a silent `any`: the operation has to exist in the contract, and the
 * status has to be one it declares.
 *
 * A status that declares no application/json body resolves to `never`, so
 * assigning a sample to it fails loudly. That is intended: only a status with a
 * body can be sampled, and `never` says so at the assignment rather than letting
 * the sample validate against nothing.
 */
export type WorkerOperationResponse<
  Operation extends keyof operations,
  Status extends keyof operations[Operation]["responses"],
> = operations[Operation]["responses"][Status] extends {
  content: { "application/json": infer Body };
}
  ? Body
  : never;
