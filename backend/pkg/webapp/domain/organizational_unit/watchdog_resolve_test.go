package organizational_unit

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The walk is node -> OU -> root -> account, one block, first declaration
// wins. A node's own DISABLED config is a real declaration that shields it
// from inheritance — disarm at the node must not resurrect an OU's armed
// config as the desired state.
func TestResolveWatchdogConfigWalk(t *testing.T) {
	armed := func(timeout int) *node.WatchdogConfig {
		return &node.WatchdogConfig{Enabled: true, AgentHealth: true, WatchdogTimeoutSec: timeout}
	}

	ou := &OrganizationalUnit{WatchdogConfig: armed(10)}
	root := &OrganizationalUnit{WatchdogConfig: armed(11)}
	account := &AccountPolicy{WatchdogConfig: armed(12)}

	if cfg, source, ok := ResolveWatchdogConfig(nil, ou, root, account); !ok || source != SourceOU || cfg.WatchdogTimeoutSec != 10 {
		t.Fatalf("ou level: cfg=%+v source=%s ok=%v", cfg, source, ok)
	}
	if cfg, source, ok := ResolveWatchdogConfig(nil, nil, root, account); !ok || source != SourceRootOU || cfg.WatchdogTimeoutSec != 11 {
		t.Fatalf("root level: cfg=%+v source=%s ok=%v", cfg, source, ok)
	}
	if cfg, source, ok := ResolveWatchdogConfig(nil, nil, nil, account); !ok || source != SourceAccount || cfg.WatchdogTimeoutSec != 12 {
		t.Fatalf("account level: cfg=%+v source=%s ok=%v", cfg, source, ok)
	}
	if _, source, ok := ResolveWatchdogConfig(nil, nil, nil, nil); ok || source != SourceDefault {
		t.Fatalf("nothing declared must be ok=false, got source=%s ok=%v", source, ok)
	}

	disarmed := &node.WatchdogConfig{Enabled: false}
	if cfg, source, ok := ResolveWatchdogConfig(disarmed, ou, root, account); !ok || source != SourceNode || cfg.Enabled {
		t.Fatalf("a node's disabled config must shield inheritance: cfg=%+v source=%s ok=%v", cfg, source, ok)
	}
}
