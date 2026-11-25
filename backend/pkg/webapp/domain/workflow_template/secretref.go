package workflow_template

import (
	"regexp"
	"sort"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
)

// ReferencePattern matches {{ ... }} references: the expression is everything
// up to the first "}", and is read trimmed. It is the one grammar both the
// run-time resolver and every authoring-time reader use, so a reference the
// export or the authoring gate sees is exactly one a run resolves.
var ReferencePattern = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// ReferenceExpression returns the trimmed expression of one ReferencePattern
// submatch.
func ReferenceExpression(submatch []string) string {
	return strings.TrimSpace(submatch[1])
}

// SecretReferenceName returns the secret name a reference expression names
// ("secrets.<name>": everything after the prefix, dots and spaces included)
// and whether it names one at all.
func SecretReferenceName(expr string) (string, bool) {
	name, ok := strings.CutPrefix(expr, "secrets.")
	return name, ok && name != ""
}

// secretRefPrefixes are the {{...}} namespaces whose value is credential
// material. node.attributes.* counts: a node attribute IS an env-kind secret
// attached to that one node (per-node CI tokens and the like), so it deserves
// the same treatment as {{secrets.*}}.
var secretRefPrefixes = []string{"secrets.", "node.attributes."}

// SecretDerivedParams returns the top-level param names whose value references
// secret material, read off the UNRESOLVED params. It must run before params
// are resolved: once a reference is substituted, the resolved string is
// indistinguishable from an operator-typed literal, and nothing downstream can
// tell that download_file.dest_path holds a token.
//
// The names — not the values — travel with the command so the API can redact
// exactly what a run put secrets into, instead of a fixed guess at which params
// "can" carry them.
//
// It lives in the domain because two very different callers need the same
// answer: the engine, deciding what to mark on a dispatched command, and the
// authoring gate, deciding whether feeding a secret into this block is a
// privileged act. A second copy of the pattern is how the two would drift.
func SecretDerivedParams(params map[string]interface{}) []string {
	var keys []string
	for k, v := range params {
		if ValueReferencesSecret(v) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // stable payload for tests and diffs
	return keys
}

// ValueReferencesSecret reports whether a param value (recursively) contains a
// reference to secret material.
func ValueReferencesSecret(v interface{}) bool {
	switch val := v.(type) {
	case string:
		for _, match := range ReferencePattern.FindAllStringSubmatch(val, -1) {
			expr := ReferenceExpression(match)
			for _, p := range secretRefPrefixes {
				if strings.HasPrefix(expr, p) {
					return true
				}
			}
		}
	case []interface{}:
		for i := range val {
			if ValueReferencesSecret(val[i]) {
				return true
			}
		}
	case map[string]interface{}:
		for _, item := range val {
			if ValueReferencesSecret(item) {
				return true
			}
		}
	}
	return false
}

// SecretFedUnprivilegedBlocks returns the ids of graph blocks that reference
// secret material but are NOT themselves privileged.
//
// Feeding a secret into a block the platform considers unprivileged is how a
// caller reads a value they may not read. The live example is
// `control.condition`: comparing {{secrets.api_key}} to a literal answers "did
// it match?" through the port it routes to, so the run's own block statuses —
// `completed` on one branch, `skipped` on the other — spell the secret out one
// comparison at a time, with `contains` making it character by character. The
// probe graph (trigger → condition → wait | wait) touches no device and needs
// no privileged block, so nothing else in the system objects.
//
// The same reasoning already made `command.set_hostname` privileged inside a
// playbook: the engine resolves a value the author cannot read into a field the
// author can then read back. This generalises it from one block to the rule.
//
// Every block that legitimately consumes secrets today — run_script,
// http_request, secret_upsert, write_file, install_packages — is already
// privileged, so in practice this refuses exactly the side channel and nothing
// an operator does on purpose.
func (g *WorkflowGraph) SecretFedUnprivilegedBlocks() []string {
	if g == nil {
		return nil
	}
	var ids []string
	for _, node := range g.Nodes {
		if authz.IsPrivilegedBlock(node.Type) {
			continue
		}
		if len(SecretDerivedParams(node.Params)) > 0 {
			ids = append(ids, node.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// SecretFedBlocks returns every block whose params reference secret material,
// privileged or not.
//
// SecretFedUnprivilegedBlocks answers a different question — "is this graph a
// side channel for reading a secret" — and deliberately excludes the privileged
// blocks, because those consume secrets on purpose. This one includes them,
// because it answers "could this block's ERROR TEXT contain a resolved secret",
// and a run_script that fails while echoing its own arguments is the commonest
// way that happens.
//
// The rule it feeds is the one PublicCommandDetails already applies to a
// command's details: redact the free text only for the things that carried
// secret material, so a "node offline" on an ordinary block stays readable by
// the manager expected to act on it.
func (g *WorkflowGraph) SecretFedBlocks() map[string]bool {
	if g == nil {
		return nil
	}
	var fed map[string]bool
	for _, node := range g.Nodes {
		if len(SecretDerivedParams(node.Params)) == 0 {
			continue
		}
		if fed == nil {
			fed = make(map[string]bool)
		}
		fed[node.ID] = true
	}
	return fed
}

// BlocksDownstreamOfSecretConditions returns the ids of every block reachable
// from a secret-fed unprivileged block, plus those blocks themselves.
//
// This is what the API must hide from a caller without secret.read_value: the
// branch a secret-derived condition took is visible in the STATUS of the blocks
// below it (`completed` vs `skipped`) even when outputs and chosen_port are
// redacted, which is the leak the redaction was written to stop.
//
// It is computed rather than stored so it stays correct for runs pinned before
// this existed — the snapshot carries the graph.
func (g *WorkflowGraph) BlocksDownstreamOfSecretConditions() map[string]bool {
	seeds := g.SecretFedUnprivilegedBlocks()
	if len(seeds) == 0 {
		return nil
	}
	adjacency := make(map[string][]string, len(g.Edges))
	for _, edge := range g.Edges {
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	hidden := make(map[string]bool, len(seeds))
	queue := make([]string, 0, len(seeds))
	for _, id := range seeds {
		hidden[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if !hidden[next] {
				hidden[next] = true
				queue = append(queue, next)
			}
		}
	}
	return hidden
}
