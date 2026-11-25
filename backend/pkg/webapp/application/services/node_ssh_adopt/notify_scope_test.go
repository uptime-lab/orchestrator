package node_ssh_adopt

import (
	"testing"

	"github.com/google/uuid"
	portsws "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
)

type capturingJobSocket struct{ sent []portsws.Message }

func (c *capturingJobSocket) Send(m portsws.Message) error {
	c.sent = append(c.sent, m)
	return nil
}

func (c *capturingJobSocket) GetChannel() chan portsws.Message { return nil }

// The job frame carries the target host, SSH username and host-key
// fingerprint; it must reach only those GET /node-adoption-jobs/:id serves,
// i.e. account-wide holders of node.ssh_adopt — not every node.read holder.
func TestJobFrameRequiresSSHAdopt(t *testing.T) {
	sock := &capturingJobSocket{}
	s := &Service{ws: sock}
	s.notify(&node_adoption_job.Job{ID: uuid.New(), AccountID: uuid.New()})

	if len(sock.sent) != 1 {
		t.Fatalf("sent %d frames, want 1", len(sock.sent))
	}
	msg := sock.sent[0]
	if msg.RequiredAction != authz.ActionNodeSSHAdopt {
		t.Fatalf("RequiredAction = %q, want %q", msg.RequiredAction, authz.ActionNodeSSHAdopt)
	}
	if msg.ScopeOU != nil || msg.ScopeOUUnassigned {
		t.Fatal("the frame must take the account-global branch, which enforces RequiredAction")
	}
}
