package utility

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

type fakeResolver struct {
	eff services.EffectiveProvisioning
}

func (f fakeResolver) ResolveEffectiveProvisioning(context.Context, uuid.UUID, uuid.UUID) (services.EffectiveProvisioning, error) {
	return f.eff, nil
}

func resolvedSet(param string) []utility.Resolved {
	return []utility.Resolved{{
		Definition: utility.Definition{ID: "kiosk"},
		Config:     map[string]interface{}{"url": param},
	}}
}

// A SENSITIVE secret must be refused, and a SENSITIVE node attribute must be
// refused the same way: an attribute IS an
// env-kind secret, and a param an operator reads back off the device is a
// read oracle for it.
func TestResolveParamRefsRefusesSensitiveValues(t *testing.T) {
	nodeID := uuid.New()
	svc := &Service{secretsResolver: fakeResolver{eff: services.EffectiveProvisioning{
		Secrets: []services.ResolvedSecret{
			{Name: "api_key", Value: "s3cret", Sensitive: true},
			{Name: secret_domain.NodeAttributeName(nodeID, "ci_token"), Key: "ci_token", Value: "t0ken", Sensitive: true},
		},
	}}}

	if _, err := svc.resolveParamRefs(context.Background(), uuid.New(), nodeID, resolvedSet("x {{secrets.api_key}}")); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("sensitive secret: got %v, want a refusal naming the sensitive value", err)
	}
	if _, err := svc.resolveParamRefs(context.Background(), uuid.New(), nodeID, resolvedSet("x {{node.attributes.ci_token}}")); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("sensitive attribute: got %v, want a refusal naming the sensitive value", err)
	}
}

// Non-sensitive values resolve, and the per-utility hash follows the RESOLVED
// config so a changed attribute honestly shows update_pending.
func TestResolveParamRefsSubstitutesAndRehashes(t *testing.T) {
	nodeID := uuid.New()
	svc := &Service{secretsResolver: fakeResolver{eff: services.EffectiveProvisioning{
		Secrets: []services.ResolvedSecret{
			{Name: secret_domain.NodeAttributeName(nodeID, "url"), Key: "url", Value: "http://a", Sensitive: false},
		},
	}}}
	in := resolvedSet("{{node.attributes.url}}/dash")
	before := in[0].ConfigHash
	out, err := svc.resolveParamRefs(context.Background(), uuid.New(), nodeID, in)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if got := out[0].Config["url"]; got != "http://a/dash" {
		t.Fatalf("substitution: got %v", got)
	}
	if out[0].ConfigHash == before {
		t.Fatal("hash must be recomputed over resolved values")
	}
}

// An unresolvable reference FAILS the apply — substituting empty is the
// engine's documented worst outcome.
func TestResolveParamRefsFailsOnUnresolvable(t *testing.T) {
	svc := &Service{secretsResolver: fakeResolver{}}
	if _, err := svc.resolveParamRefs(context.Background(), uuid.New(), uuid.New(), resolvedSet("{{node.attributes.missing}}")); err == nil {
		t.Fatal("unresolvable attribute must fail, not substitute empty")
	}
}
