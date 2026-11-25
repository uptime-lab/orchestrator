package organizational_unit

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// TestMergeEffectiveSecretsPrecedence verifies the node > OU > root precedence
// for env vars, the union of the full secret set, and de-duplication by secret
// ID when a secret is attached at multiple levels.
func TestMergeEffectiveSecretsPrecedence(t *testing.T) {
	// Three env secrets all writing the "DB_HOST" env name, one per level.
	rootEnv := uuid.New()
	ouEnv := uuid.New()
	nodeEnv := uuid.New()
	// A credential attached only at the root level (should be in the union).
	rootCred := uuid.New()
	// An opaque secret attached at both OU and node level (de-dupe to one).
	sharedOpaque := uuid.New()

	byID := map[uuid.UUID]secret.Secret{
		rootEnv:      {ID: rootEnv, Name: "root-db", Kind: secret.KindEnv, Key: "DB_HOST", Value: "root-host"},
		ouEnv:        {ID: ouEnv, Name: "ou-db", Kind: secret.KindEnv, Key: "DB_HOST", Value: "ou-host"},
		nodeEnv:      {ID: nodeEnv, Name: "node-db", Kind: secret.KindEnv, Key: "DB_HOST", Value: "node-host"},
		rootCred:     {ID: rootCred, Name: "registry", Kind: secret.KindCredential, Key: "admin", Value: "pw"},
		sharedOpaque: {ID: sharedOpaque, Name: "license", Kind: secret.KindOpaque, Key: "", Value: "abc"},
	}

	rootOU, nodeOU := uuid.New(), uuid.New()
	levels := []secretLevel{
		{name: "root", ouID: &rootOU, refs: []secretRef{
			{secretID: rootEnv, via: "direct"},
			{secretID: rootCred, via: "direct"},
		}},
		{name: "ou", ouID: &nodeOU, refs: []secretRef{
			{secretID: ouEnv, via: "direct"},
			{secretID: sharedOpaque, via: "direct"},
		}},
		{name: "node", refs: []secretRef{
			{secretID: nodeEnv, via: "direct"},
			{secretID: sharedOpaque, via: "direct"},
		}},
	}

	env, secrets := mergeEffectiveSecrets(levels, byID)

	// Node env wins for the conflicting key.
	if got := env["DB_HOST"]; got != "node-host" {
		t.Fatalf("expected DB_HOST=node-host (node wins), got %q", got)
	}
	if len(env) != 1 {
		t.Fatalf("expected exactly one env var, got %d: %v", len(env), env)
	}

	// Union of secrets, de-duped by ID: rootEnv, rootCred, ouEnv, nodeEnv, sharedOpaque = 5.
	if len(secrets) != 5 {
		t.Fatalf("expected 5 unique secrets in the effective set, got %d", len(secrets))
	}
	byName := map[string]int{}
	for i, s := range secrets {
		byName[s.Name] = i
	}
	for _, want := range []string{"root-db", "ou-db", "node-db", "registry", "license"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("expected secret %q in the effective set, missing", want)
		}
	}

	// The shared opaque secret appears once and reports its highest-precedence
	// source (node beats OU).
	shared := secrets[byName["license"]]
	if shared.Source.Level != "node" || shared.Source.Via != "direct" {
		t.Fatalf("expected shared secret source node/direct, got %+v", shared.Source)
	}
	// A root-only secret keeps its root provenance.
	cred := secrets[byName["registry"]]
	if cred.Source.Level != "root" || cred.Source.OUID == nil || *cred.Source.OUID != rootOU {
		t.Fatalf("expected registry source root/%s, got %+v", rootOU, cred.Source)
	}
}

// TestMergeEffectiveSecretsOUOverridesRoot verifies OU beats root when the node
// has no attachment for that env key.
func TestMergeEffectiveSecretsOUOverridesRoot(t *testing.T) {
	rootEnv := uuid.New()
	ouEnv := uuid.New()
	byID := map[uuid.UUID]secret.Secret{
		rootEnv: {ID: rootEnv, Name: "root", Kind: secret.KindEnv, Key: "TOKEN", Value: "root-val"},
		ouEnv:   {ID: ouEnv, Name: "ou", Kind: secret.KindEnv, Key: "TOKEN", Value: "ou-val"},
	}
	levels := []secretLevel{
		{name: "root", refs: []secretRef{{secretID: rootEnv, via: "direct"}}},
		{name: "ou", refs: []secretRef{{secretID: ouEnv, via: "direct"}}},
	}
	env, secrets := mergeEffectiveSecrets(levels, byID)
	if got := env["TOKEN"]; got != "ou-val" {
		t.Fatalf("expected TOKEN=ou-val (OU beats root), got %q", got)
	}
	if len(secrets) != 2 {
		t.Fatalf("expected 2 secrets in union, got %d", len(secrets))
	}
}

// TestMergeEffectiveSecretsSkipsMissing verifies referenced-but-missing secrets
// (deleted) are skipped without error.
func TestMergeEffectiveSecretsSkipsMissing(t *testing.T) {
	present := uuid.New()
	missing := uuid.New()
	byID := map[uuid.UUID]secret.Secret{
		present: {ID: present, Name: "kept", Kind: secret.KindEnv, Key: "K", Value: "v"},
	}
	levels := []secretLevel{
		{name: "root", refs: []secretRef{{secretID: missing, via: "direct"}}},
		{name: "ou", refs: []secretRef{{secretID: present, via: "direct"}}},
	}
	env, secrets := mergeEffectiveSecrets(levels, byID)
	if len(secrets) != 1 || secrets[0].Name != "kept" {
		t.Fatalf("expected only the present secret, got %v", secrets)
	}
	if env["K"] != "v" {
		t.Fatalf("expected env K=v, got %v", env)
	}
}

// TestMergeEffectiveSecretsDirectBeatsTagWithinLevel verifies that inside a
// single level a direct attachment overrides a tag-provided env value for
// the same key, and that tag provenance is reported for tag-only members.
func TestMergeEffectiveSecretsDirectBeatsTagWithinLevel(t *testing.T) {
	tagEnv := uuid.New()
	directEnv := uuid.New()
	tagOnly := uuid.New()

	byID := map[uuid.UUID]secret.Secret{
		tagEnv:    {ID: tagEnv, Name: "tag-db", Kind: secret.KindEnv, Key: "DB_HOST", Value: "tag-host"},
		directEnv: {ID: directEnv, Name: "direct-db", Kind: secret.KindEnv, Key: "DB_HOST", Value: "direct-host"},
		tagOnly:   {ID: tagOnly, Name: "token", Kind: secret.KindOpaque, Value: "t"},
	}

	ouID := uuid.New()
	// Tag refs come before direct refs within a level (expandTagRefs
	// guarantees that ordering).
	levels := []secretLevel{
		{name: "ou", ouID: &ouID, refs: []secretRef{
			{secretID: tagEnv, via: "tag", tag: "infra"},
			{secretID: tagOnly, via: "tag", tag: "infra"},
			{secretID: directEnv, via: "direct"},
		}},
	}

	env, secrets := mergeEffectiveSecrets(levels, byID)
	if got := env["DB_HOST"]; got != "direct-host" {
		t.Fatalf("expected direct attachment to win within the level, got %q", got)
	}
	for _, s := range secrets {
		if s.Name == "token" {
			if s.Source.Via != "tag" || s.Source.Tag != "infra" {
				t.Fatalf("expected tag provenance for token, got %+v", s.Source)
			}
		}
	}
}

// TestMergeEffectiveSecretsLowerLevelTagLosesToHigherDirect verifies the
// cross-level precedence with mixed subjects: a node-level direct attachment
// overrides an OU-level tag value.
func TestMergeEffectiveSecretsLowerLevelTagLosesToHigherDirect(t *testing.T) {
	ouTagEnv := uuid.New()
	nodeDirectEnv := uuid.New()
	ouID := uuid.New()

	byID := map[uuid.UUID]secret.Secret{
		ouTagEnv:      {ID: ouTagEnv, Name: "a", Kind: secret.KindEnv, Key: "K", Value: "ou-tag"},
		nodeDirectEnv: {ID: nodeDirectEnv, Name: "b", Kind: secret.KindEnv, Key: "K", Value: "node-direct"},
	}
	levels := []secretLevel{
		{name: "ou", ouID: &ouID, refs: []secretRef{{secretID: ouTagEnv, via: "tag", tag: "infra"}}},
		{name: "node", refs: []secretRef{{secretID: nodeDirectEnv, via: "direct"}}},
	}
	env, _ := mergeEffectiveSecrets(levels, byID)
	if got := env["K"]; got != "node-direct" {
		t.Fatalf("expected node-direct to win, got %q", got)
	}
}
