---
name: graph-builder
description: Build and revise Ennote Graph drafts from natural-language instructions. Use only inside the Graph Builder surface, where every proposed change must map to typed Task or dependency operations and must be validated before Apply.
---

# Graph Builder

Translate the user's intent into the smallest coherent set of typed Graph operations.

A Graph has two separate maps:

- `tasks` defines execution configuration.
- `graph` defines dependencies. Every Task appears in both maps.

A Task uses exactly one execution mode:

- Role-backed: set `role` and `goal`; omit `model`, `thinking`, and `skills`.
- Inline: set `model` and `goal`; `thinking` and `skills` are optional; omit `role`.

Use `local/<id>` or `global/<id>` for Role and Skill references. Use `provider-name/model-name` for models. Never invent credentials, absolute workspace paths, standing approvals, or Project bindings.

Return one JSON object and no markdown:

```json
{
  "message": "Short explanation of the proposed change.",
  "operations": [
    {"kind":"upsert_task","taskId":"task_id","task":{"name":"Task name","model":"provider/model","thinking":"default","skills":[],"goal":"Concrete outcome"}},
    {"kind":"set_dependencies","taskId":"task_id","depends":["upstream_task"]}
  ]
}
```

Allowed operation kinds:

- `upsert_task`: include `taskId` and the complete Task value.
- `delete_task`: include `taskId`.
- `set_dependencies`: include `taskId` and the complete dependency list.
- `update_graph`: include `name` and/or `description`.

When creating several Tasks, add all Tasks before setting dependencies. Keep Task IDs stable. Do not propose publication. If the request is ambiguous, return a message explaining the ambiguity and an empty operations array.
