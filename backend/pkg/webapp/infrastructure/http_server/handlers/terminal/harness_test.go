package terminal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The handler has no WebSocket harness of its own: its two older test files
// exercise the audit pair and the per-user cap as plain functions. The
// admission bounds and the live-session rule live in the handshake and the
// bridge, so these stubs drive the REAL route through a real upgrade.

// goodToken is the one token stubAuth accepts.
const goodToken = "good"

type stubAuth struct {
	services.AuthService
	claims        *services.TokenClaims
	live          atomic.Bool
	validateCalls atomic.Int32
}

func (s *stubAuth) ValidateAccessToken(token string) (*services.TokenClaims, error) {
	s.validateCalls.Add(1)
	if token != goodToken {
		return nil, app_errors.ErrTokenInvalid
	}
	return s.claims, nil
}

func (s *stubAuth) RequireLiveSession(context.Context, uuid.UUID, services.SessionProof) error {
	if !s.live.Load() {
		return app_errors.ErrTokenInvalid
	}
	return nil
}

type stubNodes struct {
	services.NodeService
	mu   sync.Mutex
	node *node.Node
	err  error
}

func (s *stubNodes) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.node == nil || s.node.ID != nodeID || s.node.AccountID != accountID {
		return nil, app_errors.ErrNodeNotFound
	}
	copied := *s.node
	return &copied, nil
}

func (s *stubNodes) moveTo(ou *uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.node.OUID = ou
}

func (s *stubNodes) vanish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = app_errors.ErrNodeNotFound
}

type fakeSession struct {
	id     string
	out    chan []byte
	closed chan services.TerminalExit
}

func (f *fakeSession) SessionID() string                    { return f.id }
func (f *fakeSession) Output() <-chan []byte                { return f.out }
func (f *fakeSession) Closed() <-chan services.TerminalExit { return f.closed }

type stubTerminal struct {
	services.TerminalService
	mu      sync.Mutex
	opened  int
	closed  []string
	inputs  [][]byte
	session *fakeSession
}

func (s *stubTerminal) OpenTerminal(string, uint32, uint32) (services.TerminalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened++
	s.session = &fakeSession{id: "sess-" + uuid.NewString()[:8], out: make(chan []byte), closed: make(chan services.TerminalExit)}
	return s.session, nil
}

func (s *stubTerminal) SendTerminalInput(_ string, data []byte, isResize bool, _, _ uint32) {
	if isResize {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, data)
}

func (s *stubTerminal) CloseTerminal(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = append(s.closed, sessionID)
}

func (s *stubTerminal) openedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened
}

func (s *stubTerminal) closedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.closed)
}

func (s *stubTerminal) inputCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inputs)
}

type harness struct {
	h         *Handler
	cfg       *config.Config
	srv       *httptest.Server
	auth      *stubAuth
	nodes     *stubNodes
	term      *stubTerminal
	sink      *audittest.Sink
	accountID uuid.UUID
	userID    uuid.UUID
	ouA       uuid.UUID
	nodeID    uuid.UUID
}

// newHarness builds the real handler around stubs: an admin of unit A, a node
// in unit A, a live session, and the terminal enabled. Each test then breaks
// exactly one of those.
func newHarness(t *testing.T) *harness {
	t.Helper()
	hs := &harness{
		cfg:       &config.Config{},
		auth:      &stubAuth{},
		nodes:     &stubNodes{},
		term:      &stubTerminal{},
		accountID: uuid.New(),
		userID:    uuid.New(),
		ouA:       uuid.New(),
		nodeID:    uuid.New(),
	}
	hs.cfg.Terminal.Enabled = true
	hs.auth.live.Store(true)
	hs.auth.claims = &services.TokenClaims{
		UserID:    hs.userID,
		AccountID: hs.accountID,
		TokenType: services.TokenTypeAccess,
		FamilyID:  uuid.New(),
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	ou := hs.ouA
	hs.nodes.node = &node.Node{ID: hs.nodeID, AccountID: hs.accountID, OUID: &ou}

	recorder, sink := audittest.New()
	hs.sink = sink
	hs.h = NewHandler(hs.term, hs.nodes, hs.auth, hs.enforcerFor(authz.RoleAdmin), recorder, hs.cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	e := echo.New()
	// The same extractor server.go installs: the httptest server is reached
	// over loopback, which the production configuration trusts as the proxy,
	// so an X-Forwarded-For set by a test IS the client address the handler
	// keys its per-source bounds on — exactly a reverse proxy's position in the
	// shipped composes. Without it every socket here is 127.0.0.1.
	e.IPExtractor = echo.ExtractIPFromXFFHeader(
		echo.TrustLoopback(true),
		echo.TrustLinkLocal(true),
		echo.TrustPrivateNet(true),
	)
	hs.h.Register(e)
	hs.srv = httptest.NewServer(e)
	t.Cleanup(hs.srv.Close)
	return hs
}

func (hs *harness) enforcerFor(role authz.Role) *authz_service.Enforcer {
	return authztest.ActiveMemberWithOURole(hs.accountID, hs.userID, role, hs.ouA)
}

func (hs *harness) url() string {
	return "ws" + strings.TrimPrefix(hs.srv.URL, "http") + "/api/v1/nodes/" + hs.nodeID.String() + "/terminal"
}

// dial opens the socket with no Origin header, which the origin policy admits
// as a non-browser client.
func (hs *harness) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(hs.url(), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial: %v (status %d)", err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// dialStatus returns the HTTP status of a refused handshake, or 101.
func (hs *harness) dialStatus(t *testing.T) (*websocket.Conn, int) {
	t.Helper()
	conn, status, _ := hs.dialFrom(t, "")
	return conn, status
}

// dialFrom opens the socket as a client behind the trusted proxy at the given
// address (an empty source leaves the loopback remote address as the client),
// returning the HTTP status of a refused handshake and its body, or 101.
func (hs *harness) dialFrom(t *testing.T, source string) (*websocket.Conn, int, string) {
	t.Helper()
	var header http.Header
	if source != "" {
		header = http.Header{"X-Forwarded-For": {source}}
	}
	conn, resp, err := websocket.DefaultDialer.Dial(hs.url(), header)
	if err != nil {
		if !errors.Is(err, websocket.ErrBadHandshake) || resp == nil {
			t.Fatalf("dial: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		return nil, resp.StatusCode, string(body)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, http.StatusSwitchingProtocols, ""
}

func (hs *harness) authenticate(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"token": goodToken, "cols": 80, "rows": 24}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "terminal opened", func() bool { return hs.term.openedCount() == 1 })
}

// readClose reads until the peer closes and returns the close code, along
// with any text frames received on the way.
func readClose(t *testing.T, conn *websocket.Conn) (int, []string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var texts []string
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) {
				return ce.Code, texts
			}
			t.Logf("read error: %v", err)
			return -1, texts
		}
		if mt == websocket.TextMessage {
			texts = append(texts, string(data))
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
