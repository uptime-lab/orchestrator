# Organization page — decisions in force

**Shipped**: phases 1–4 and the companion IA change. The plan text is gone;
what is left is the set of decisions the shipped code is built on, because
four code comments cite this path and reach for these rules —
`frontend/src/pages/organization/index.tsx`,
`frontend/src/pages/organization/unitDetail.tsx`,
`frontend/src/routes/_mainLayout/workflows.tsx` and
`backend/pkg/webapp/domain/audit_event/actions.go`.

The page is `/organization`: the OU rail on the left, a unit's passport on the
right. Selecting a unit shows its policies (the **Policies** tab) and everything
the unit delivers to its nodes (the **Contents** tab, `?tab=contents`); the
tree's top pseudo-row, **Account defaults**, is the account policy's home (it was
previously reachable only from a pencil on the Nodes rail's "Unassigned" row).
This rail is the only one that edits units, and it does so
without a toggle: for `ou.manage` holders it is permanently in edit mode
(delete, drag-to-reparent, reorder, "+ New OU"), while every other page's rail
is a filter tree plus assignment drops. Selecting a row is the editor, so the
per-row pencil is gone everywhere. The rail draws **Account defaults as the
tree's top node** with the top-level units indented under it — the account
policy is the one root of the chain (it reaches every unit and the unassigned
nodes), and drawn as a sibling row it read as one more unit; dropping a nested
unit on it makes the unit top-level. The copy says **top-level unit**, never
"root unit", and a policy source is "the parent unit": two things called root
on one screen was the confusion the row order had been feeding.

## Rail = navigate/assign, page = view/edit

**Never render a second OU tree in the content area.** This was bought by
building a standalone OU page once and deleting it because it duplicated the
rail — `routes/_mainLayout/ou.tsx` was the redirect left behind, and `/ou` is
now removed outright. The rail keeps navigation, drag-to-assign, reorder and
reparent on every list page; the Organization page is the **detail** surface and
nothing else. A change that starts to grow a tree, a picker or a second
assignment gesture into the page is undoing the reason the page exists.

## The anti-duplication rule

**A section exists on the unit page only if each of its rows carries
OU-perspective information the original page cannot show — provenance,
inheritance, or effect.** A section that could only list names and links is not
a section; it is a line in the counts strip. This is the rule
`pages/organization/unitDetail.tsx` calls a section's "licence to exist".

What it decided in practice, so the demotions are not re-read as omissions:

- **Utilities is a counts line**, not a section: no endpoint serves "applied on
  X of Y nodes" per unit, so the rows would carry nothing the Utilities page
  does not already show. Promote it when that endpoint exists.
- **The OS image is a policy field**, not a section: a unit holds exactly one
  `os_image_id`, rendered with its effective value and source, deep-linking into
  the Operations section's Install Images tab.
- **Master tokens with this adoption OU** are a counts-strip line.
- Every real section is a **short summary** — first N rows, true total, deep
  link into the page that manages the resource. Management stays on the original
  pages; the deep links are cheap because every list page already keeps its OU
  filter in URL search params.

## The dual-surface tax

Every OU-attachable feature now needs **two** things: a rail drag semantic and a
unit-page section (or a deliberate counts line under the rule above). That cost
was accepted knowingly. **A new OU-attachable feature that ships without its
Organization-page surface is dangling work** in the same way an unmerged branch
is — nothing fails, and the unit's passport quietly stops being complete.

## Non-goals

- **No per-node role bindings.** Grants stay targeted at the account or an OU.
  "Nodes" on the unit page means *showing* which devices the unit's policies
  reach — never granting rights on an individual node.
- **The tree stays 2-level** (domain-enforced). The page makes the limit more
  visible; the domain refusal is the answer, not a UI workaround.
- The Nodes page behaviour is unchanged: selecting a unit in its rail still
  filters the node list. Its rail no longer edits units at all —
  the Edit toggle and the pencils are gone, and a node is draggable onto a unit
  only in the page's Select mode.
- Utilities and OU policies keep **separate resolution algebras**; read
  `docs/UTILITIES-RESEARCH.md` before proposing a merge.

## Audit

An OU update and an account-policy update can dispatch `apply_access_config` to
every node the level reaches, which made them the largest unrecorded mutations
in the product before this shipped. `ou.create` / `ou.update` / `ou.delete` /
`account_policy.update` are recorded through `audit.Recorder`; the detail
carries names, the parent, changed field **names** and the pushed-node count —
never a password or any other field value.

## Deviations from the plan as written

- **Phase 3 (platform access) shipped through the members API**, not through a
  new route. `frontend/src/pages/organization/platformAccess.tsx` reads
  `apiMembers.listMembers` and grants/revokes through the existing member
  mutations, which already validate `ou_id` and write audit rows; the proposed
  `GET /organizational-units/:id/role-bindings` and the repository's `ListByOUID`
  were never built. The rows are the OU-perspective view of bindings the Team
  page already owns. `super_admin` is deliberately not grantable per unit.
- **No separate `ou.reparent` audit action** — a reparent is an `ou.update`
  whose detail names the changed field.
- **"Platform access" is one section, not a tab of its own.** The unit page did
  ship as sections on one scrolling page (an inner tab row was judged to mirror
  the main nav); that was reversed when the policy form had grown
  to several screens and pushed the contents out of sight: the page now has TWO
  tabs, **Policies** and **Contents**, the same bar as Secrets and Operations,
  and every section lives under Contents. What did not change: no section gets
  its own tab, and the Account defaults page has no tab bar (it has nothing but
  the form).
- **No redirects.** `/os-images` and `/ou` were removed rather than redirected —
  both pages were too young to have shared links — and every internal reference
  was updated in the same change.
- The companion IA change shipped: OS Images left the top nav, the section was
  renamed **Operations** with five tabs (Playbooks · Runs · Install Images ·
  Backups · Gold Images), and the **route stays `/workflows`**, as do the API and
  database names.

## Follow-ups

Remaining follow-ups are tracked in the issue tracker, not here.
