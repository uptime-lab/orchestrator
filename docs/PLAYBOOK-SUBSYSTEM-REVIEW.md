# Playbook subsystem — format and authoring decisions

**A decision record for the storage format and the authoring model**, written
in answer to a proposal to add YAML storage and a code editor. It records **why
the format is jsonb and not YAML**, **what n8n answered and where we
deliberately differ**, and the properties of playbooks the argument rests on.
A real dangerous-run confirmation in the UI is open work (the privileged-block
half is pinned by `TestEveryDangerousBlockIsPrivileged`). Section numbering is
kept for stable external references.

---

## 1. Verdict

The original request — *move playbooks to YAML, add a code editor beside the
canvas* — should not be built as asked. Two conclusions, each independently
supported:

1. **YAML is rejected** — as storage (it breaks a live abuse control) and as an
   interchange format (it buys one thing, block scalars, and the corpus has
   almost nothing to put in them). §4.
2. **Clipboard paste and a field code editor are the text-authoring path** —
   fragments through the clipboard, code inside one block. Both are built; the
   rules they hold are in §4.2.

---

## 4. Format and authoring decisions, with the reasoning behind them

### 4.0 Prior art: n8n, which this was modelled on, answered the same question

Verified from source and docs, because "n8n does it this way" was the
premise the original request rested on:

- **Workflows are JSON.** `IWorkflowBase` (`packages/workflow/src/interfaces.ts:3439`)
  is `nodes: INode[]` + `connections: IConnections`, and the docs say plainly
  *"n8n saves workflows in JSON format"*. Their 2026 **packages** feature is
  JSON again — a `.n8np` tar with `manifest.json` and one `workflow.json` per
  workflow — so this is a decision they re-took recently, not inertia.
- **They do have branching**: *"Splitting uses the IF or Switch nodes"*, plus
  Merge. So a graph model like ours is not what pushed them to JSON.
- **They ship no whole-document text editor.** Text authoring is clipboard paste
  onto the canvas (*"You can copy and paste a workflow or parts of it… (Ctrl+C)
  … (Ctrl+V) into the Editor UI"*), and code authoring is the per-node **Code
  node**. That is exactly the split this record arrives at independently:
  fragments through the clipboard, code inside one block.

One thing **not** to copy: `IConnections` is keyed by node **name**, so renaming
a node rewrites connections. Our edges reference `id` and `name` is display-only.
Keep it that way — and note the corollary, that our `{{blocks.<id>.outputs.*}}`
references make ids load-bearing in a way n8n's are not.

### 4.1 YAML — rejected

**As canonical storage.** `graph` is not the only representation: `schedule_cron`
is a denormalized indexed column the per-minute scheduler filters on in SQL, and
`status` is a column an out-of-band writer may set to pause a blocked account's
cron playbooks (an operator may set it directly in SQL). Derive either from a document and a
tenant blocked for abuse keeps firing `install_os`/`erase_drive` on a
schedule. No SQL anywhere uses jsonb operators on `graph`, so jsonb is serving
as a *validator at rest*. Migration is AutoMigrate only, with no down
migrations.

**As an interchange format.** Implementable, but it does not pay for itself.

- YAML buys exactly one thing here: block scalars (`|`) for embedded documents.
  Real — the complex values cluster on `unit_content`,
  `http_request.body`, `write_file.content`, `install_packages.packages` — but
  short. JSON5 does not fix it (its multi-line strings are line continuation).
- **Comments do not survive a machine-owned document.** `gopkg.in/yaml.v3`
  documents that re-encoded content does not preserve its original textual
  representation. AWS Step Functions documents the same concession verbatim
  (*"use the `Comment` property"*), as does Renovate for JSON5. The answer here
  is `GraphNode.Description` (`graph.go:43`), which already exists.
- **Measured**: both `yaml.v3` and the browser's `yaml@2` already emit
  `'*/5 * * * *'` and `'{{node.name}}'` correctly quoted, so machine-generated
  YAML is safe. But `mode: 0644` parses as **420 in Go and 644 in the browser** —
  a silent cross-stack differential on a real param.
- The hardest part of the design — expressing a graph in YAML — solves a problem
  the corpus it was checked against did not have: **no playbook branched.**

### 4.2 Clipboard paste and a field code editor

**The clipboard unit** is the selected nodes plus the edges among them; copying
a whole playbook is select-all. The clipboard text is a JSON fragment with its
own `kind`, and text without that kind falls through to an ordinary paste.
Every paste holds these rules (`pages/workflowEditor/graphUtils.ts`, each
pinned by a test in `graphUtils.test.ts`); skipping any one is a bug, not a
style choice:

- **(a) Every entry node is connected to the chain tail.** A pasted node with
  no incoming edge inside the fragment is auto-connected to the free end of
  the existing flow. Reachability from the trigger is checked by the shared
  validation, so an unconnected fragment would make every save fail. With no
  tail to connect to, the paste warns instead.
- **(b) Param values are pasted verbatim, never cleared** — even a reference
  this account cannot resolve. An empty `install_os.image_id` is not inert: it
  falls back to the node's effective unit or account image at dispatch, so a
  cleared reference would silently become a different destructive action. An
  unresolvable value fails at save or run with its own message instead.
- **(c) Every node id is re-minted, and `{{blocks.<id>.…}}` references are
  rewritten structurally**: each `{{…}}` expression is split on `.` as the
  backend resolver splits it, and only the id segment is replaced. A textual
  replace would corrupt values — `block-1` is a prefix of `block-12`, and the
  trigger id `start` is also an ordinary parameter value.
- **(d) Every edge id is re-minted.** The canvas silently drops an edge whose id
  it already holds, while the saved graph would still carry it.
- **(e) Triggers are dropped with their edges** (the receiving canvas already
  has one), and an edge whose `from_port` is not among the source block's
  declared outputs is dropped with a warning.

**The field code editor commits on blur and on a short trailing debounce,
never per keystroke.** The editor's dirty check re-serialises the entire graph
on every change (`pages/workflowEditor/index.tsx`), so committing per keystroke
would make the editor slowest exactly when it is used for a long script. The
draft lives in the field until it commits, an outside change replaces it only
while it is already committed, and closing the inspector flushes it.

---

## 5. What the corpus shape implies

The design choices above were checked against the shape of a small playbook
corpus, and that shape matters only through what it implies:

- **Graphs are small** — single-digit blocks, no `control.*` block, no NULL
  `graph` left to synthesize. Per-graph cost (validation, the synthesized
  chain, re-serialisation) is therefore not the axis that scales; the number
  of playbooks the scheduler scans every minute is. That is why the per-tick
  scan is a narrow projection over the denormalized `schedule_cron` column
  bounded per account in SQL, and graphs are read only for the minute's
  matches — the cost that grows with a fleet is kept off the jsonb.
- **Complex values cluster on four params**: `manage_service.unit_content`,
  `http_request.body`, `write_file.content`, `install_packages.packages`.
  Editor work for long values (§4) pays off there first.
- **Whether that still holds is measurable**: the urgency signals in §4 are
  the queries to re-run; a count copied into this document would only date it.
