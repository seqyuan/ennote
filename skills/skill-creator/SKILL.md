# Skill Creator

Turn a workflow the user already understands into a reusable Ennote Skill.

Use this skill when the user says things like "make this a skill", "create a
skill for X", "save this workflow", or "teach the agent to do X next time".
Do not use it for one-off tasks that should just be completed in the current
conversation.

## Workflow

1. **Capture intent.** Establish:
   - what the Skill should enable the agent to do;
   - the user phrases or situations that should trigger it;
   - the expected inputs and output format;
   - any tools, files, or external services it needs.
   Ask only for information that changes the Skill. If the conversation already
   contains the workflow, extract it from there and confirm the gaps.

2. **Draft the Skill body.** Write concise operating instructions for a future
   agent, not a narrative about this conversation:
   - start with the goal and the trigger conditions;
   - give the smallest reliable procedure, including checks and failure handling;
   - state hard constraints (no secrets, no writes outside the workspace, no
     unapproved external calls);
   - include one concrete example when the format is non-obvious.
   The first line should tell the agent what to do, not why the Skill exists.

3. **Write the description for triggering.** The `description` field is the
   primary discovery signal. Include both what the Skill does and when to use
   it, with the user's likely phrases. Be specific; avoid generic words such as
   "helps with tasks".

4. **Create it with the `create_skill` tool.** Do not write into `~/.ennote`
   with `bash`; that path is not writable in sandboxed Runs and bypasses
   validation. Call `create_skill` with:
   - `id`: lowercase slug, e.g. `pdf-report` (letters, digits, `-`, `_`);
   - `name`: optional display name;
   - `description`: the trigger-oriented description;
   - `prompt`: the SKILL.md body;
   - `allowedTools`: optional list of tools the Skill needs.

5. **Verify.** The tool returns the Skill ID and host path. Confirm to the user
   that the Skill is available immediately to Roles and Flows, and suggest how
   to bind it in a Role or a Flow. Do not claim it was tested unless you
   actually ran it.

## Writing rules

- Prefer short, imperative instructions over long explanations.
- Put "when to use this" in the description, not buried in the body.
- Keep the Skill self-contained: no references to this conversation.
- Never include API keys, tokens, passwords, or private user data.
- Do not grant tools the Skill does not need.
- If the user wants to change an existing Skill, explain that this version
  creates new Skills; ask them to edit the existing files in Settings first
  (editing is not yet exposed through `create_skill`).

## Example

User: "Every time I get a CSV, I want a short data-quality report."

Description: "Produce a concise data-quality report for a CSV. Use whenever the
user asks to inspect, validate, or summarize a CSV before analysis."

Body:

```markdown
# CSV Data Quality Report

Read the CSV header and a bounded sample first. Report:
1. row and column counts;
2. missing values per column;
3. duplicate rows;
4. type/range anomalies worth flagging;
5. the three most important next checks.

Do not modify the input file. Keep the report under 300 words.
```

Then call `create_skill` with id `csv-data-quality`, the description above, and
the body as the prompt.
