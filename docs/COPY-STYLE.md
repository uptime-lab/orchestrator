# User-facing copy style guide

Rules for every string an operator reads: utility registry YAMLs, the
workflow-block registry, preset catalogs, `en.json`, hardcoded frontend
strings, and operator-facing error messages. Distilled from a series of
plain-language copy passes. `AGENTS.md` carries a one-line pointer here; this file is the authority.

## Voice

- One to three short, plain sentences. Sentence case everywhere, including
  buttons ("Create token", not "CREATE TOKEN" — CSS may uppercase a heading,
  but the source string stays sentence case).
- No CAPITALIZED emphasis (NOT, IS, REBOOTS). If a fact needs weight, give it
  its own short sentence, first.
- No em-dash chains, no stacked parentheticals, no colon-inside-dash
  constructions. Split into sentences instead.
- Instruction-first: lead with what the operator should do; the rationale, if
  it earns its place, follows ("Use a single-use or short-lived pre-auth key:
  any operator who can view the node can read the key's value.").
- No internal jargon in user-visible text: command slugs, table/column names,
  Go identifiers, env var names — a plain word instead. An error that must
  name a slug explains it.

## The echo test

Before shipping a sentence, ask: **would an operator who never read our design
discussions be helped by it at this moment — or does it presuppose a debate
they never saw?** Delete or restate anything that only answers a question we
asked ourselves in a design chat:

- classification verdicts ("The network ID is not a secret: …");
- comparisons to the road not taken ("Unlike a ZeroTier network ID…",
  "…rather than waited on forever");
- justifications ("this is deliberate", "by design");
- internal-taxonomy disambiguations ("not a particular boot mode").

A negation stays ONLY when it guards a real wrong action ("Removing the
assignment keeps Tailscale installed and logged in" answers "is remove
safe?"), and even then prefer the positive statement of what happens. This
class of defect self-propagates — once one such phrase ships, later passes
read it as house style — so it is worth a dedicated hunt in any copy pass.

## Two tiers: visible text + info mark

Long texts split into a short **visible** line and a hover **tooltip** behind
the ⓘ mark (`shared/ui/infoHint`):

- **Visible** = what this is / what happens, plus anything decision-critical.
- **Tooltip** = read-once background: mechanics (package names, how it is
  applied), edge cases, output/reference syntax, examples.
- **Never behind hover**: destructive or irreversible consequences (wipes,
  reboots, data loss, lockout), credential warnings, hard prerequisites,
  agent version floors. A warning hidden behind hover is a warning unread.
- **Error messages never split** — an error is read in full, and it names the
  operator's next step, not the platform's internals.
- Frequently opened panels (node sidebar tabs) are instruments, not manuals:
  labels, states and warnings inline; explanations behind ⓘ.

## Mechanics

- Backend-served texts carry the second tier in an optional `details` field:
  `BlockDefinition.Details` / `ParamSpec.Details` (workflow_block.go),
  `details:` at utility and param level in the utility registry YAMLs. The
  field must be COPIED in the DTO mappers (`results/workflow_block_result.go`,
  `results/utility_result.go`, utility loader `toDefinition`) — the converters
  are field-by-field, nothing appears in JSON automatically.
- i18n strings render through i18next, which swallows literal `{{…}}`. A hint
  that must SHOW reference syntax passes it as an interpolation value:
  `t('key', { placeholder: '{{secrets.<name>}}' })`.
- An InfoHint that follows flowing text is glued to the last word:
  `{text}<span className="whitespace-nowrap">&nbsp;<InfoHint text={…} /></span>`
  — a plain space lets the icon wrap alone onto its own line.
- A test may assert copy as a contract (e.g. the http_request block's outputs
  must be named in its copy). Such contracts read Description **and** Details
  combined — see `http_outputs_test.go`.

## Terminology

| Say | Not |
|---|---|
| Playbook (definition), Run (execution) | workflow, template |
| node | device (except when meaning the physical board) |
| Recovery (netboot) | maintenance mode, netboot install |
| Local OS | boot to OS |
| Credentials-page users / device login users | access entries |

## Keeping it honest

Copy defects survive review when a rule protects them ("keep security
facts" shields classification echoes). When checking copy, run
separate passes with separate briefs: factual accuracy, brevity, echo hunt,
seam/consistency check between adjacent strings, and a live-UI walk — each
catches what the previous one is instructed to preserve. Verify on the
rendered page, not only in the source: some defects (swallowed `{{…}}`
placeholders, an orphaned ⓘ) are invisible in code review.
