package websockets

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
)

// errorRecorder keeps every ERROR record.
type errorRecorder struct {
	mu     sync.Mutex
	errors []string
}

func (r *errorRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *errorRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelError {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.errors = append(r.errors, rec.Message)
	}
	return nil
}
func (r *errorRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *errorRecorder) WithGroup(string) slog.Handler      { return r }

func (r *errorRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errors...)
}

// A browser that closes its socket the ordinary ways — the UI's own shutdown
// code, a tab going away, a normal close — ends both pumps without an ERROR
// line: the read pump sees an expected close, and the write pump then meets
// a connection that is already closed, which says nothing an operator needs.
func TestAnOrdinaryBrowserCloseLogsNoError(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"the UI's shutdown code", spaShutdownCloseCode},
		{"going away", websocket.CloseGoingAway},
		{"normal closure", websocket.CloseNormalClosure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &errorRecorder{}
			removed := make(chan websockets3.ClientID, 1)
			clients := make(chan *Client, 1)
			readDone := make(chan struct{})
			writeDone := make(chan struct{})

			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade: %v", err)
					return
				}
				c := NewClient(conn, make(chan websockets3.InboundMessage, 1), removed, slog.New(rec))
				clients <- &c
				go func() { defer close(writeDone); c.WritePump() }()
				go func() { defer close(readDone); c.ReadPump() }()
			}))
			defer srv.Close()

			browser, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			c := <-clients
			if err := browser.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(tc.code, "please-exit")); err != nil {
				t.Fatalf("close frame: %v", err)
			}
			_ = browser.Close()

			select {
			case <-readDone:
			case <-time.After(5 * time.Second):
				t.Fatal("the read pump did not end on the browser's close")
			}
			<-removed
			// The hub may still hold a frame for this client: the write pump
			// then finds the socket gone and ends.
			c.Send(websockets3.NewMessageError("late frame"))
			select {
			case <-writeDone:
			case <-time.After(5 * time.Second):
				t.Fatal("the write pump did not end on a closed socket")
			}

			if got := rec.recorded(); len(got) != 0 {
				t.Fatalf("an ordinary close logged ERROR lines: %q", got)
			}
		})
	}
}

// A close the browser does not ordinarily send is still reported at ERROR.
func TestAnUnexpectedCloseCodeIsStillAnError(t *testing.T) {
	rec := &errorRecorder{}
	removed := make(chan websockets3.ClientID, 1)
	readDone := make(chan struct{})

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c := NewClient(conn, make(chan websockets3.InboundMessage, 1), removed, slog.New(rec))
		go func() { defer close(readDone); c.ReadPump() }()
	}))
	defer srv.Close()

	browser, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = browser.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "boom"))
	_ = browser.Close()

	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the read pump did not end")
	}
	got := rec.recorded()
	if len(got) != 1 || got[0] != "connection closed unexpectedly" {
		t.Fatalf("ERROR records %q, want one \"connection closed unexpectedly\"", got)
	}
}
