package grpc

import (
	"fmt"
	"sync"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"github.com/google/uuid"
)

// terminalOutBuffer bounds the per-session output channel. A runaway process
// that outpaces the browser is throttled here rather than exhausting memory.
const terminalOutBuffer = 256

// terminalSession is one browser↔device bridge, keyed by session id.
type terminalSession struct {
	id     string
	nodeID string
	out    chan []byte
	closed chan services.TerminalExit
	once   sync.Once
}

func (t *terminalSession) SessionID() string                    { return t.id }
func (t *terminalSession) Output() <-chan []byte                { return t.out }
func (t *terminalSession) Closed() <-chan services.TerminalExit { return t.closed }

// SendToNode sends any platform→node frame over the node's existing stream.
func (s *Server) SendToNode(nodeID string, msg *proto.PlatformToNode) error {
	s.mu.RLock()
	connection, exists := s.connections[nodeID]
	s.mu.RUnlock()
	if !exists {
		return fmt.Errorf("node %s not connected", nodeID)
	}
	// Through the connection's writer, not the stream: these calls come from HTTP
	// and WebSocket handler goroutines, so an inline Send here raced the command
	// dispatcher on one gRPC stream — undefined in grpc-go and fatal in practice.
	// Opening a terminal on a node while a command goes out to it is an ordinary
	// debugging sequence.
	return connection.Send(msg)
}

// OpenTerminal starts a terminal session on a connected node.
func (s *Server) OpenTerminal(nodeID string, cols, rows uint32) (services.TerminalSession, error) {
	if !s.IsNodeConnected(nodeID) {
		return nil, fmt.Errorf("node %s not connected", nodeID)
	}
	session := &terminalSession{
		id:     uuid.NewString(),
		nodeID: nodeID,
		out:    make(chan []byte, terminalOutBuffer),
		closed: make(chan services.TerminalExit, 1),
	}

	s.terminalsMu.Lock()
	if s.terminals == nil {
		s.terminals = make(map[string]*terminalSession)
	}
	s.terminals[session.id] = session
	s.terminalsMu.Unlock()

	if err := s.SendToNode(nodeID, &proto.PlatformToNode{
		Payload: &proto.PlatformToNode_TerminalOpen{
			TerminalOpen: &proto.TerminalOpen{SessionId: session.id, Cols: cols, Rows: rows, Mode: "shell"},
		},
	}); err != nil {
		s.dropTerminal(session.id)
		return nil, err
	}
	s.logger.Info("terminal session opened", "session", session.id, "node_id", nodeID)
	return session, nil
}

// SendTerminalInput forwards keystrokes or a resize to the device.
func (s *Server) SendTerminalInput(sessionID string, data []byte, isResize bool, cols, rows uint32) {
	s.terminalsMu.RLock()
	session := s.terminals[sessionID]
	s.terminalsMu.RUnlock()
	if session == nil {
		return
	}
	_ = s.SendToNode(session.nodeID, &proto.PlatformToNode{
		Payload: &proto.PlatformToNode_TerminalInput{
			TerminalInput: &proto.TerminalInput{
				SessionId: sessionID, Data: data, IsResize: isResize, Cols: cols, Rows: rows,
			},
		},
	})
}

// CloseTerminal tells the device to end the PTY and tears down the session.
func (s *Server) CloseTerminal(sessionID string) {
	s.terminalsMu.RLock()
	session := s.terminals[sessionID]
	s.terminalsMu.RUnlock()
	if session == nil {
		return
	}
	_ = s.SendToNode(session.nodeID, &proto.PlatformToNode{
		Payload: &proto.PlatformToNode_TerminalClose{
			TerminalClose: &proto.TerminalClose{SessionId: sessionID},
		},
	})
	s.endTerminal(sessionID, services.TerminalExit{})
}

// deliverTerminalOutput routes device PTY output to the browser side. Drops the
// chunk if the browser can't keep up (bounded channel) rather than blocking the
// gRPC receive loop.
func (s *Server) deliverTerminalOutput(sessionID string, data []byte) {
	s.terminalsMu.RLock()
	session := s.terminals[sessionID]
	s.terminalsMu.RUnlock()
	if session == nil {
		return
	}
	select {
	case session.out <- data:
	default:
		s.logger.Warn("terminal output dropped (browser too slow)", "session", sessionID)
	}
}

// terminalOwnedBy reports whether the session belongs to nodeID. Terminal frames
// (output/closed) carry only a session id; this binds them to the authenticated
// stream they arrive on, so a compromised/foreign agent cannot inject bytes into
// or tear down a terminal session belonging to another node (mirrors the node-scope
// check on CommandStatusUpdate). An empty nodeID (pre-auth stream) never matches.
func (s *Server) terminalOwnedBy(sessionID, nodeID string) bool {
	if nodeID == "" {
		return false
	}
	s.terminalsMu.RLock()
	session := s.terminals[sessionID]
	s.terminalsMu.RUnlock()
	return session != nil && session.nodeID == nodeID
}

// endTerminal signals session end to the browser side once and removes it.
func (s *Server) endTerminal(sessionID string, exit services.TerminalExit) {
	s.terminalsMu.Lock()
	session := s.terminals[sessionID]
	delete(s.terminals, sessionID)
	s.terminalsMu.Unlock()
	if session == nil {
		return
	}
	session.once.Do(func() {
		session.closed <- exit
		close(session.closed)
	})
	s.logger.Info("terminal session closed", "session", sessionID, "exit_code", exit.Code, "error", exit.Error)
}

// dropTerminal removes a session without emitting a close event (used when open
// fails before the browser is listening).
func (s *Server) dropTerminal(sessionID string) {
	s.terminalsMu.Lock()
	delete(s.terminals, sessionID)
	s.terminalsMu.Unlock()
}

// closeNodeTerminals ends every session belonging to a node (on disconnect).
func (s *Server) closeNodeTerminals(nodeID string) {
	s.terminalsMu.Lock()
	var ids []string
	for id, sess := range s.terminals {
		if sess.nodeID == nodeID {
			ids = append(ids, id)
		}
	}
	s.terminalsMu.Unlock()
	for _, id := range ids {
		s.endTerminal(id, services.TerminalExit{Error: "node disconnected"})
	}
}

var _ services.TerminalService = (*Server)(nil)
