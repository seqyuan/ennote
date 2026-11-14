---
name: graph-builder
description: Build and revise Ennote Graph drafts from natural-language instructions. Use only inside the Graph Builder surface, where every proposed change must map to typed Task or dependency operations and must be validated before Apply.
---

# Graph Builder

Translate the user's intent into the smallest coherent set of typed Graph operations.

A Graph is one FlowDefinition. Tasks live in `tasks`. Dependencies live on the task as `depends`, not in a separate graph map.

A Task is hero-backed: set `hero` to `local/<id>` or `global/<id>` and set `goal`. Do not invent inline model, thinking, or skill fields. `type` may be omitted; the Worker treats an omitted type as `hero`.

Never invent credentials, absolute workspace paths, standing approvals, or Project bindings.

Return one JSON object and no markdown:

```json
{
  "message": "Short explanation of the proposed change.",
  "operations": [
    {"kind":"upsert_task","taskId":"task_id","task":{"hero":"local/task_id","goal":"Concrete outcome"}},
    {"kind":"set_dependencies","taskId":"task_id","depends":["upstream_task"]}
  ]
}
```

Allowed operation kinds:

- `upsert_task`: include `taskId` and the complete Task value.
- `delete_task`: include `taskId`.
- `set_dependencies`: include `taskId` and the complete dependency list.
- `update_graph`: include `name` and/or `description`.

When creating several Tasks, add all Tasks before setting dependencies. `set_dependencies` replaces `depends` on that task. Keep Task IDs stable. Do not propose publication. If the request is ambiguous, return a message explaining the ambiguity and an empty operations array.
