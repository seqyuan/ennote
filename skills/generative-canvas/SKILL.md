# Generative Canvas

A **widget** is an HTML page the reader sees live in the conversation: an SVG
diagram, an interactive chart, a calculator, a parameter explorer. Unlike a
`static_html` artifact it may run scripts — inside an isolated frame with no
network, no cookies and no access to the Workspace.

Use it when the answer is a picture the user should be able to poke at. Do not use
it for ordinary prose, tables, or files.

## How to publish one

1. Write the page into the Workspace with `write` (or generate it with a script and
   `publish_artifact` the result):

   ```
   write({ path: "/workspace/canvas.html", content: "<!doctype html>…" })
   ```

2. Publish it, naming the kind:

   ```
   publish_artifact({ path: "/workspace/canvas.html", kind: "widget" })
   ```

`kind: "widget"` is required. Without it an `.html` file is published as
`static_html`, which is sanitized into a script-free document — your SVG and your
script would both disappear.

## Hard limits

| Limit | Value | Why |
|---|---|---|
| Size | **128 KiB** | it travels to phones over a bounded channel; this is not a document download |
| Extension | `.html` / `.htm`, UTF-8, starts with a tag | classification |
| Scripts | inline, or from `cdnjs.cloudflare.com`, `cdn.jsdelivr.net`, `unpkg.com`, `esm.sh` | the only allowed hosts |
| Network | **none** — no `fetch`, no `XHR`, no WebSocket, no remote images | the frame is sandboxed |
| Images | inline SVG, `data:` or `blob:` | remote `img` is blocked on purpose |
| `eval` / `new Function` | blocked | no `unsafe-eval` |
| Width | 320–900 px, fluid | the frame is a message, not a screen |
| Height | measured and reported; capped at 900 px in the timeline | beyond that it scrolls inside the frame |

If a widget needs data, **you** read it and inline a bounded snapshot:

```
read({ path: "/workspace/markers.csv" })   →   embed at most a few hundred rows
```

A widget cannot read anything itself. Inlining is also what makes it auditable: the
published bytes carry the data that produced the picture.

## Structure

```html
<!doctype html>
<style>
  .card { background: var(--color-background-secondary); color: var(--color-text-primary);
          border: 1px solid var(--color-border-secondary); border-radius: var(--radius-md); }
</style>
<div class="card">
  <svg viewBox="0 0 640 320" width="100%" height="320">…</svg>
</div>
<script>
  // runs after the markup; keep it at the end
</script>
```

Rules of thumb:

- **Self-contained first.** Inline SVG and inline CSS beat a CDN. Use Chart.js (from
  `cdnjs.cloudflare.com`) only when a real chart is needed, and never a chart library
  for something four `<rect>`s can draw.
- **Never fix a pixel width.** Use `width="100%"` and a `viewBox`; the frame is
  narrow on a phone.
- **Put `<script>` at the very end.** Scripts before the content delay the first
  paint and the height report.
- **Stay under a few hundred DOM nodes.** A canvas that repaints constantly will
  visibly cost a phone battery.

## Theme

The frame receives these CSS custom properties, already resolved for the current
light/dark theme. Use them instead of literal colors so the widget belongs to the
app:

| Property | Use |
|---|---|
| `--color-background-primary` / `-secondary` / `-tertiary` | page, card, inset |
| `--color-text-primary` / `-secondary` / `-tertiary` | headings, body, captions |
| `--color-border-primary` / `-secondary` / `-tertiary` | box edges |
| `--color-accent` | the one thing to look at |
| `--color-background-success` / `-warning` / `-danger` / `-info` | status fills |
| `--color-text-success` / `-warning` / `-danger` / `-info` | status text |
| `--color-chart-1` … `--color-chart-6` | categorical series |
| `--font-sans` / `--font-mono` | typography |
| `--radius-sm` / `-md` / `-lg` | corners |

Body text at 14 px, captions no smaller than 11 px. The theme can change while the
widget is on screen; anything styled with these properties follows automatically.

## Picking a form

| The question is about | Draw |
|---|---|
| a process, pipeline, or decision path | flowchart: boxes, arrows, one decision shape |
| change over time | line chart, or a timeline with labelled events |
| composition, proportions | stacked bar or a small multiple; a pie only for two or three parts |
| ranking | sorted horizontal bars, labels at the left |
| relationships | node-link diagram, at most ~15 nodes |
| a comparison of two things | two matching columns with the same rows |

Every widget needs a caption inside the page saying what it shows and in what
units. A picture with no units is decoration.

## Failure is visible

A script that throws, or a dependency that cannot load, degrades the widget in
place: the reader sees the drawing plus a notice and a link to the source. Nothing
you publish can break the conversation, the panel, or a later message. That is a
safety net, not a licence — publish a page you have reasoned about, not one that
might hang.
