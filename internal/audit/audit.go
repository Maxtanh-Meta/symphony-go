// Package audit implements a slog.Handler that fans out log records to a
// delegate handler (typically the stderr JSON handler) and to per-issue
// JSONL audit files under a configured root directory.
//
// Records carrying an integer attribute named "issue" or "issue_number"
// are routed (in addition to the delegate) to a file named
// "<rootDir>/{issue}.jsonl"; records without such an attribute are passed
// through to the delegate only. Every string-valued attribute (at any
// nesting depth, up to maxRedactDepth levels) is run through exec.Redact
// with the configured patterns before being written to the audit file.
// slog.LogValuer values are resolved (via LogValue) before walking.
//
// See SPEC.md §13 for the event taxonomy and per-issue JSONL contract.
package audit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/logosc/symphony-go/internal/exec"
)

// maxRedactDepth bounds recursion into nested slog.GroupValue attrs while
// walking a record for redaction. slog values cannot actually cycle
// today, but this defensive cap makes the walker obviously terminating
// and keeps stack usage bounded for pathological inputs (e.g. a future
// LogValuer that returns ever-deeper groups). Beyond this depth, the
// attribute is emitted as-is without further recursion.
const maxRedactDepth = 16

// Handler is a slog.Handler that writes records to a delegate handler and,
// when the record carries an "issue" or "issue_number" int attribute, also
// appends a redacted JSON line to a per-issue JSONL file under rootDir.
//
// The zero value is not usable; construct via New.
type Handler struct {
	delegate       slog.Handler
	rootDir        string
	redactPatterns []string

	// preset attributes accumulated via WithAttrs, already namespaced by
	// any active groups. These are applied to every record this Handler
	// processes (in addition to the record's own attrs).
	presetAttrs []slog.Attr
	// groupStack records active groups (innermost last) so that attrs
	// added via WithAttrs after WithGroup are nested correctly when
	// emitted to the per-issue file.
	groupStack []string

	// shared state across all clones produced by WithAttrs/WithGroup.
	shared *sharedState
}

// sharedState holds the per-issue file cache shared across all derived
// handlers (clones produced by WithAttrs/WithGroup).
type sharedState struct {
	mu    sync.Mutex
	files map[int]*issueSink
	// closed becomes true after Close; subsequent file opens are skipped.
	closed bool
}

// issueSink owns one append-only *os.File for a single issue plus a mutex
// to serialize writes from concurrent goroutines.
type issueSink struct {
	mu      sync.Mutex
	f       *os.File
	handler slog.Handler // a slog.JSONHandler writing into f
}

// New returns a Handler that mirrors records to delegate and writes a
// redacted JSON line to "<rootDir>/{issue}.jsonl" whenever a record's
// attributes include an integer "issue" or "issue_number". rootDir is
// created lazily on first per-issue write. If delegate is nil, records
// are still routed to per-issue files but no delegate output is produced.
func New(rootDir string, redactPatterns []string, delegate slog.Handler) *Handler {
	return &Handler{
		delegate:       delegate,
		rootDir:        rootDir,
		redactPatterns: append([]string(nil), redactPatterns...),
		shared:         &sharedState{files: make(map[int]*issueSink)},
	}
}

// Enabled reports whether the handler is enabled at level. The result is
// the OR of the delegate (if any) and a permissive default of true so
// that audit-only sinks still receive records when the delegate filters
// them out.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.delegate != nil && h.delegate.Enabled(ctx, level) {
		return true
	}
	return true
}

// Handle dispatches r to the delegate (verbatim) and, if r carries an
// "issue"/"issue_number" int attribute, also writes a redacted JSON line
// to the corresponding per-issue file. Errors writing to the audit file
// are reported via the returned error but do not block delegate output;
// the delegate is always invoked first.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	var delegateErr error
	if h.delegate != nil {
		delegateErr = h.delegate.Handle(ctx, r)
	}

	issue, ok := h.findIssue(r)
	if !ok {
		return delegateErr
	}

	sink, err := h.sinkFor(issue)
	if err != nil {
		if delegateErr != nil {
			return fmt.Errorf("audit: %w (delegate: %v)", err, delegateErr)
		}
		return err
	}
	if sink == nil {
		// Handler closed; drop silently.
		return delegateErr
	}

	redacted := h.redactRecord(r)

	sink.mu.Lock()
	hErr := sink.handler.Handle(ctx, redacted)
	sink.mu.Unlock()

	if hErr != nil {
		if delegateErr != nil {
			return fmt.Errorf("audit: %w (delegate: %v)", hErr, delegateErr)
		}
		return hErr
	}
	return delegateErr
}

// WithAttrs returns a new Handler whose records and per-issue file lines
// will include attrs in addition to anything already configured. The
// returned handler shares the per-issue file cache with its parent.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := h.clone()
	if h.delegate != nil {
		clone.delegate = h.delegate.WithAttrs(attrs)
	}
	// Preset attrs for the audit-file emitter need to be wrapped in any
	// currently-open groups so nesting matches what the delegate sees.
	wrapped := wrapInGroups(attrs, h.groupStack)
	clone.presetAttrs = append(clone.presetAttrs, wrapped...)
	return clone
}

// WithGroup returns a new Handler that nests subsequent attributes under
// name. The returned handler shares the per-issue file cache with its
// parent.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := h.clone()
	if h.delegate != nil {
		clone.delegate = h.delegate.WithGroup(name)
	}
	clone.groupStack = append(clone.groupStack, name)
	return clone
}

// Close flushes and closes every per-issue file the handler has opened.
// Subsequent Handle calls will not reopen files. Close is safe to call
// from any clone produced by WithAttrs/WithGroup; the underlying file
// cache is shared.
func (h *Handler) Close() error {
	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()
	if h.shared.closed {
		return nil
	}
	h.shared.closed = true
	var firstErr error
	for n, s := range h.shared.files {
		s.mu.Lock()
		if s.f != nil {
			if err := s.f.Close(); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("close issue %d: %w", n, err)
			}
			s.f = nil
		}
		s.mu.Unlock()
		delete(h.shared.files, n)
	}
	return firstErr
}

// clone returns a shallow copy of h. The presetAttrs and groupStack
// slices are duplicated so callers can append without aliasing the
// parent's state. The shared file cache is intentionally aliased.
func (h *Handler) clone() *Handler {
	c := *h
	if len(h.presetAttrs) > 0 {
		c.presetAttrs = append([]slog.Attr(nil), h.presetAttrs...)
	}
	if len(h.groupStack) > 0 {
		c.groupStack = append([]string(nil), h.groupStack...)
	}
	return &c
}

// findIssue searches the record's attrs (and the handler's preset attrs)
// for an int-valued "issue" or "issue_number" attribute. Group-nested
// attrs are not searched: the convention is that the issue id is a
// top-level attribute on the record.
func (h *Handler) findIssue(r slog.Record) (int, bool) {
	// preset attrs may carry the issue (e.g. via Logger.With("issue", n)).
	for _, a := range h.presetAttrs {
		if n, ok := issueAttrValue(a); ok {
			return n, true
		}
	}
	var found int
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if n, match := issueAttrValue(a); match {
			found, ok = n, true
			return false
		}
		return true
	})
	return found, ok
}

// issueAttrValue returns the integer value of a if a.Key is "issue" or
// "issue_number" and a.Value resolves to an int64-compatible kind.
func issueAttrValue(a slog.Attr) (int, bool) {
	if a.Key != "issue" && a.Key != "issue_number" {
		return 0, false
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindInt64:
		return int(v.Int64()), true
	case slog.KindUint64:
		return int(v.Uint64()), true
	default:
		return 0, false
	}
}

// sinkFor returns (creating if necessary) the issueSink for issue n.
// Returns (nil, nil) if the handler has been closed.
func (h *Handler) sinkFor(n int) (*issueSink, error) {
	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()
	if h.shared.closed {
		return nil, nil
	}
	if s, ok := h.shared.files[n]; ok {
		return s, nil
	}
	if err := os.MkdirAll(h.rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("audit: mkdir %s: %w", h.rootDir, err)
	}
	path := filepath.Join(h.rootDir, fmt.Sprintf("%d.jsonl", n))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	s := &issueSink{
		f: f,
		handler: slog.NewJSONHandler(io.Writer(f), &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}),
	}
	h.shared.files[n] = s
	return s, nil
}

// redactRecord returns a copy of r with the handler's preset attrs
// prepended (wrapped in any active groups) and every string-valued attr
// (recursing one level into groups) passed through exec.Redact.
func (h *Handler) redactRecord(r slog.Record) slog.Record {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	// Preset attrs first (already group-wrapped).
	for _, a := range h.presetAttrs {
		out.AddAttrs(h.redactAttr(a, 0))
	}
	// Record attrs, wrapped in current group stack.
	var raw []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		raw = append(raw, a)
		return true
	})
	wrapped := wrapInGroups(raw, h.groupStack)
	for _, a := range wrapped {
		out.AddAttrs(h.redactAttr(a, 0))
	}
	return out
}

// redactAttr returns a with any string-valued payload (at any nesting
// depth, up to maxRedactDepth) run through exec.Redact. Group-valued
// attrs are walked recursively. slog.LogValuer values are resolved via
// LogValue (already done by Value.Resolve) and then re-dispatched.
//
// depth tracks the current recursion level; when depth >= maxRedactDepth
// the attribute is returned unchanged to ensure obvious termination.
//
// String elements buried inside slog.KindAny (arbitrary Go values such
// as slices or maps of strings) are NOT redacted: walking them would
// require reflection over arbitrary types, which is out of scope. Code
// emitting secrets via KindAny should pre-redact or use slog.String.
func (h *Handler) redactAttr(a slog.Attr, depth int) slog.Attr {
	if depth >= maxRedactDepth {
		return a
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, exec.Redact(v.String(), h.redactPatterns))
	case slog.KindGroup:
		inner := v.Group()
		out := make([]slog.Attr, len(inner))
		for i, ia := range inner {
			out[i] = h.redactAttr(ia, depth+1)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	default:
		// KindLogValuer is unreachable here because Value.Resolve already
		// drove LogValue() to a fixed point; KindAny and primitives fall
		// through unchanged.
		return a
	}
}

// wrapInGroups wraps attrs inside successive groups so the innermost
// group of stack contains attrs directly. wrapInGroups(attrs, ["a","b"])
// yields a single Attr {Key:"a", Group:[{Key:"b", Group: attrs}]}.
// An empty stack returns attrs unchanged.
func wrapInGroups(attrs []slog.Attr, stack []string) []slog.Attr {
	if len(stack) == 0 || len(attrs) == 0 {
		return attrs
	}
	cur := attrs
	for i := len(stack) - 1; i >= 0; i-- {
		cur = []slog.Attr{{Key: stack[i], Value: slog.GroupValue(cur...)}}
	}
	return cur
}
