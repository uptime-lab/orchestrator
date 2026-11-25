// Package logging carries a request's identity through the context so a log line
// can be tied back to the request and the tenant that caused it.
//
// The gap it closes: `middleware.RequestID` was installed, so Echo's access log
// had an `id` — and nothing else did. No service log line carried the request id,
// and no line anywhere carried `account_id` or `user_id`, so correlating a proxy
// line, an Echo access line, a handful of service `slog` lines and a
// `node_commands` row was impractical.
package logging

import (
	"context"
	"log/slog"
)

// Scope is who and what one request belongs to. All fields are strings because
// they exist to be logged, and a log line must never be the thing that fails
// because an id was malformed.
type Scope struct {
	RequestID string
	AccountID string
	UserID    string
}

// empty reports whether there is nothing worth attaching.
func (s Scope) empty() bool {
	return s.RequestID == "" && s.AccountID == "" && s.UserID == ""
}

type contextKey struct{}

// WithScope returns a context carrying the scope, MERGED over any scope already
// present: the request id is stamped before authentication runs and the tenant
// identity only becomes known afterwards, so the second call must not erase the
// first.
func WithScope(ctx context.Context, s Scope) context.Context {
	if existing, ok := ScopeFrom(ctx); ok {
		if s.RequestID == "" {
			s.RequestID = existing.RequestID
		}
		if s.AccountID == "" {
			s.AccountID = existing.AccountID
		}
		if s.UserID == "" {
			s.UserID = existing.UserID
		}
	}
	return context.WithValue(ctx, contextKey{}, s)
}

// ScopeFrom reads the scope, if any.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	s, ok := ctx.Value(contextKey{}).(Scope)
	return s, ok
}

// ContextHandler adds the request scope to every record logged through a
// context-aware slog method (InfoContext, WarnContext, ErrorContext,
// DebugContext).
//
// A handler rather than a logger wrapper, so it applies to the ~98 call sites
// that already pass a context without touching one of them. The ~658 call sites
// that log without a context are unaffected and stay uncorrelated — converting
// those is a mechanical sweep for another change, and doing it in this one would
// bury the mechanism in noise.
type ContextHandler struct {
	slog.Handler
}

// NewContextHandler wraps a base handler. Returns the base unchanged if it is
// already wrapped, so double-wrapping cannot duplicate attributes.
func NewContextHandler(base slog.Handler) slog.Handler {
	if base == nil {
		return nil
	}
	if _, already := base.(*ContextHandler); already {
		return base
	}
	return &ContextHandler{Handler: base}
}

// Handle attaches the scope, then delegates.
func (h *ContextHandler) Handle(ctx context.Context, record slog.Record) error {
	scope, ok := ScopeFrom(ctx)
	if !ok || scope.empty() {
		return h.Handler.Handle(ctx, record)
	}
	if scope.RequestID != "" {
		record.AddAttrs(slog.String("request_id", scope.RequestID))
	}
	if scope.AccountID != "" {
		record.AddAttrs(slog.String("account_id", scope.AccountID))
	}
	if scope.UserID != "" {
		record.AddAttrs(slog.String("user_id", scope.UserID))
	}
	return h.Handler.Handle(ctx, record)
}

// WithAttrs and WithGroup must re-wrap, or a `logger.With(...)` call anywhere
// silently returns to the unwrapped handler and the scope stops appearing —
// and `logger.With("service", "...")` is the house style, so that would be
// everywhere.
func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup re-wraps too, with one consequence worth knowing: attributes added by
// Handle land INSIDE the open group, so a grouped logger emits
// `<group>.request_id` rather than `request_id`. A handler cannot lift attributes
// out of a group through the slog API. Nothing in this codebase calls WithGroup
// today; anything that starts to should expect the nesting.
func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithGroup(name)}
}
