// Package session keeps one connection per vCenter context and tracks its
// state, so that a broken customer environment is visible as a broken row
// rather than as a failure of the whole tool.
package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// ConnectionState is where a session is in its lifecycle.
type ConnectionState int

// Connection states.
const (
	Disconnected ConnectionState = iota
	Connecting
	Connected
	Failed
)

// String renders the state for status output.
func (s ConnectionState) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Connected:
		return "connected"
	case Failed:
		return "failed"
	default:
		return "disconnected"
	}
}

// DefaultConnectTimeout bounds how long one vCenter may take to connect before
// it is reported as failed. One unreachable environment must never hold up the
// others.
const DefaultConnectTimeout = 30 * time.Second

// DefaultConcurrency bounds how many vCenters are contacted at once.
const DefaultConcurrency = 8

// Session is one vCenter context and its connection.
type Session struct {
	Context *config.Context

	mu       sync.Mutex
	state    ConnectionState
	client   *vsphere.Client
	err      error
	latency  time.Duration
	lastOK   time.Time
	lastTry  time.Time
	attempts int
	// loggedOut records an operator's logout. Until an explicit Login, a
	// Connect for this context refuses rather than quietly logging back in.
	loggedOut bool
}

// ErrLoggedOut is what Connect returns for a context the operator logged out
// of, until something explicitly logs in to it again.
var ErrLoggedOut = errors.New("logged out")

// Status is an immutable snapshot of a session, safe to render.
type Status struct {
	Name       string
	Endpoint   string
	Route      string
	State      ConnectionState
	Latency    time.Duration
	Version    string
	Err        error
	LastOK     time.Time
	LastTry    time.Time
	Attempts   int
	Datacenter string
	// InstanceID is the vCenter's server GUID (About.InstanceUuid), used to
	// build vSphere Client deep links. It is empty until connected, and
	// vCenter simulators such as vcsim report it empty even then — callers
	// that need it must treat empty as "unavailable", not "unknown yet".
	InstanceID string
}

// Snapshot returns the current status of the session.
func (s *Session) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		Name:       s.Context.Name,
		Endpoint:   s.Context.Endpoint,
		Route:      s.Context.Transport.Describe(),
		State:      s.state,
		Latency:    s.latency,
		Err:        s.err,
		LastOK:     s.lastOK,
		LastTry:    s.lastTry,
		Attempts:   s.attempts,
		Datacenter: s.Context.Datacenter,
	}
	if s.client != nil {
		st.Version = s.client.About.FullVersion()
		st.Route = s.client.Route()
		st.InstanceID = s.client.About.InstanceID
	}
	return st
}

// Client returns the live client, or nil when the session is not connected.
func (s *Session) Client() *vsphere.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// adopt points the session at cc, the current configuration for its name, and
// reports the client that is now orphaned — non-nil only when cc describes a
// different connection than the one the session was holding, in which case
// everything the old vCenter told us is dropped along with it. The caller must
// hold s.mu and is responsible for closing what comes back.
func (s *Session) adopt(cc *config.Context) *vsphere.Client {
	if s.Context.SameConnection(cc) {
		return nil
	}
	s.Context = cc
	stale := s.client
	s.client = nil
	s.state = Disconnected
	s.err = nil
	s.latency = 0
	s.lastOK = time.Time{}
	s.lastTry = time.Time{}
	s.attempts = 0
	return stale
}

// Manager owns the sessions for every configured context.
type Manager struct {
	// ConnectTimeout bounds a single connection attempt.
	ConnectTimeout time.Duration
	// IdleTimeout bounds how long a streaming operation may go without
	// progress. Zero means DefaultIdleTimeout. See StreamOperation.
	IdleTimeout time.Duration
	// Concurrency bounds parallel connections. Zero means DefaultConcurrency.
	Concurrency int
	// ConnectOptions are passed to every connection.
	ConnectOptions vsphere.ConnectOptions

	mu       sync.Mutex
	sessions map[string]*Session
}

// New returns a Manager resolving credentials through r.
func New(r *credentials.Resolver) *Manager {
	return &Manager{
		ConnectTimeout: DefaultConnectTimeout,
		Concurrency:    DefaultConcurrency,
		ConnectOptions: vsphere.ConnectOptions{Resolver: r},
		sessions:       map[string]*Session{},
	}
}

func (m *Manager) session(cc *config.Context) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]*Session{}
	}
	s, ok := m.sessions[cc.Name]
	if !ok {
		s = &Session{Context: cc}
		m.sessions[cc.Name] = s
	}
	return s
}

func (m *Manager) timeout() time.Duration {
	if m.ConnectTimeout > 0 {
		return m.ConnectTimeout
	}
	return DefaultConnectTimeout
}

func (m *Manager) concurrency() int {
	if m.Concurrency > 0 {
		return m.Concurrency
	}
	return DefaultConcurrency
}

// Operation returns a context bounding one complete per-context operation —
// Connect and whatever the caller does with the connection afterwards — to
// the manager's configured timeout, together with a StageTracker that both
// report their progress to.
//
// Connect already bounds itself to the same timeout for callers that stop
// there (see the cctx inside Connect); Operation exists for callers who go
// on to do more with the connection, such as enumerating inventory, where
// that work must count against the same budget rather than running
// unbounded once the connection itself succeeded. Passing the returned
// context to both Connect and the work after it is what makes that one
// budget instead of two.
func (m *Manager) Operation(ctx context.Context) (context.Context, context.CancelFunc, *vsphere.StageTracker) {
	tracker := &vsphere.StageTracker{}
	ctx = vsphere.WithStageReporter(ctx, tracker.Report)
	ctx, cancel := context.WithTimeout(ctx, m.timeout())
	return ctx, cancel, tracker
}

// DefaultIdleTimeout bounds how long a streaming operation may go without
// making any progress. It is deliberately not a bound on the whole
// operation: reading an estate with thousands of virtual machines legitimately
// takes minutes, and a fixed overall deadline cannot tell that apart from a
// connection that has stopped answering. What actually distinguishes the two
// is whether anything has arrived lately, which is what this measures.
const DefaultIdleTimeout = 90 * time.Second

func (m *Manager) idleTimeout() time.Duration {
	if m.IdleTimeout > 0 {
		return m.IdleTimeout
	}
	return DefaultIdleTimeout
}

// StreamOperation is Operation for a retrieval that reports progress as it
// goes. The returned context is cancelled when nothing has reported progress
// for IdleTimeout, rather than after a fixed duration however much work is
// still arriving, so a slow-but-moving estate finishes and a stalled one
// still fails promptly.
//
// progress restarts the idle clock and is safe to call from any goroutine.
// The caller must call the returned cancel when the operation is over, as it
// would for context.WithTimeout; calling it also stops the watchdog.
func (m *Manager) StreamOperation(parent context.Context) (context.Context, func(), context.CancelFunc, *vsphere.StageTracker) {
	tracker := &vsphere.StageTracker{}
	ctx := vsphere.WithStageReporter(parent, tracker.Report)
	ctx, cancel := context.WithCancelCause(ctx)

	idle := m.idleTimeout()
	var mu sync.Mutex
	done := false
	timer := time.AfterFunc(idle, func() { cancel(context.DeadlineExceeded) })

	progress := func() {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		timer.Reset(idle)
	}
	stop := func() {
		mu.Lock()
		done = true
		mu.Unlock()
		timer.Stop()
		cancel(context.Canceled)
	}
	return ctx, progress, stop, tracker
}

// StreamError names what a StreamOperation's failure means. A context
// cancelled by the idle watchdog reports itself as a plain cancellation, so
// the cause is consulted and re-reported as the timeout it actually was —
// wrapping context.DeadlineExceeded so that every caller already testing for
// a timeout keeps recognising one.
func (m *Manager) StreamError(ctx context.Context, err error, tracker *vsphere.StageTracker) error {
	if err == nil {
		return nil
	}
	idle := errors.Is(context.Cause(ctx), context.DeadlineExceeded)
	if !idle && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if !idle {
		return m.TimeoutError(err, tracker)
	}
	if stage := tracker.Current(); stage != "" {
		return fmt.Errorf("gave up after %s with no response while %s: %w", m.idleTimeout(), stage, context.DeadlineExceeded)
	}
	return fmt.Errorf("gave up after %s with no response: %w", m.idleTimeout(), context.DeadlineExceeded)
}

// TimeoutError names what a deadline-exceeded error actually means: the
// configured duration and, when known, the stage the operation was in when
// time ran out — "timed out after 30s while loading hosts" — rather than the
// generic "context deadline exceeded" surfacing from deep inside a govmomi
// call. Any error that is not due to ctx's deadline is returned unchanged.
func (m *Manager) TimeoutError(err error, tracker *vsphere.StageTracker) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if stage := tracker.Current(); stage != "" {
		return fmt.Errorf("timed out after %s while %s: %w", m.timeout(), stage, err)
	}
	return fmt.Errorf("timed out after %s: %w", m.timeout(), err)
}

// Connect returns a connected session for a context, reusing an existing
// connection when one is live and still describes the same vCenter.
//
// A context that was edited keeps its name, so the session found here can be a
// live connection to somewhere else entirely — the endpoint the name used to
// mean. Reusing it would answer questions about the new configuration with
// data from the old one, so the stale connection is closed and replaced
// instead.
func (m *Manager) Connect(ctx context.Context, cc *config.Context) (*Session, error) {
	s, _, err := m.connect(ctx, cc, false)
	return s, err
}

// Login is Connect for an operator's explicit request: it clears a previous
// Logout, and it checks that a connection it would reuse is still logged in.
//
// A vCenter ends a session on its own — after its idle timeout, when it
// restarts, or when an administrator terminates it — and nothing tells the
// client. Reusing such a session answers every request with NotAuthenticated
// for as long as the process runs, so a reused session is pinged first and
// replaced by a fresh login when the ping fails. That costs one property
// read per explicit load, not per request.
func (m *Manager) Login(ctx context.Context, cc *config.Context) (*Session, error) {
	s, reused, err := m.connect(ctx, cc, true)
	if err != nil || !reused {
		return s, err
	}
	client := s.Client()
	if client == nil {
		return s, nil
	}
	if _, err := client.Ping(ctx); err == nil {
		return s, nil
	}
	m.Drop(cc.Name, client)
	s, _, err = m.connect(ctx, cc, true)
	return s, err
}

// Drop discards a connection that has stopped working, so the next Connect
// logs in again instead of reusing it. It only drops client, not whatever has
// replaced it since: two requests that both saw the dead session must not
// tear down the fresh one the first of them made.
func (m *Manager) Drop(name string, client *vsphere.Client) {
	m.mu.Lock()
	s, ok := m.sessions[name]
	m.mu.Unlock()
	if !ok || client == nil {
		return
	}
	s.mu.Lock()
	if s.client != client {
		s.mu.Unlock()
		return
	}
	s.client = nil
	s.state = Disconnected
	s.mu.Unlock()
	// The server has usually forgotten the session already; the logout is in
	// case it has not, and bounded so an unreachable vCenter costs nothing.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout())
		defer cancel()
		_ = client.Close(ctx)
	}()
}

// Logout ends one context's vCenter session at the operator's request. Unlike
// Forget, the session stays known and remembers the logout: Connect refuses
// to log back in until Login is called, so nothing running in the background
// quietly undoes it.
func (m *Manager) Logout(ctx context.Context, cc *config.Context) error {
	s := m.session(cc)
	s.mu.Lock()
	client := s.client
	s.client = nil
	s.state = Disconnected
	s.err = nil
	s.latency = 0
	s.loggedOut = true
	s.mu.Unlock()
	if client == nil {
		return nil
	}
	if err := client.Close(ctx); err != nil {
		return fmt.Errorf("%s: %w", cc.Name, err)
	}
	return nil
}

// connect is Connect and Login's shared body. reused reports that the
// returned session was already connected and no login happened.
func (m *Manager) connect(ctx context.Context, cc *config.Context, explicit bool) (s *Session, reused bool, err error) {
	s = m.session(cc)
	s.mu.Lock()
	if s.loggedOut && !explicit {
		s.mu.Unlock()
		return s, false, fmt.Errorf("context %q: %w; select it or reload to log in again", cc.Name, ErrLoggedOut)
	}
	s.loggedOut = false
	// Adopt the caller's context first: it is the current configuration, and
	// the one the session must describe from here on even if nothing else
	// about it changed.
	stale := s.adopt(cc)
	if stale != nil {
		// Log out of the vCenter this name used to mean, without holding the
		// session lock and without letting a route that has gone away delay
		// the new connection — the session is already detached from it. The
		// logout is a courtesy to the old server, bounded so that a proxy that
		// no longer answers costs a goroutine for a while rather than forever.
		go func() {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.timeout())
			defer cancel()
			_ = stale.Close(lctx)
		}()
	}
	if s.state == Connected && s.client != nil {
		s.mu.Unlock()
		return s, true, nil
	}
	s.state = Connecting
	s.err = nil
	s.lastTry = time.Now()
	s.attempts++
	s.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()

	client, err := vsphere.Connect(cctx, cc, m.ConnectOptions)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loggedOut {
		// The operator logged out while this login was in flight; the logout
		// is the later request, so it wins.
		if client != nil {
			go func() {
				lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.timeout())
				defer cancel()
				_ = client.Close(lctx)
			}()
		}
		return s, false, fmt.Errorf("context %q: %w", cc.Name, ErrLoggedOut)
	}
	if err != nil {
		s.state = Failed
		s.err = err
		s.client = nil
		return s, false, err
	}
	s.state = Connected
	s.client = client
	s.latency = client.Latency
	s.lastOK = time.Now()
	return s, false, nil
}

// ConnectAll connects to every given context concurrently. Failures are
// recorded on the individual sessions and never abort the others: the returned
// error is nil unless the caller's context was cancelled.
func (m *Manager) ConnectAll(ctx context.Context, contexts []*config.Context) []*Session {
	sessions := make([]*Session, len(contexts))
	var g errgroup.Group
	g.SetLimit(m.concurrency())
	for i, cc := range contexts {
		g.Go(func() error {
			s, _ := m.Connect(ctx, cc)
			sessions[i] = s
			// Connection errors live on the session. Returning them here
			// would cancel every other vCenter, which is precisely the
			// coupling this tool exists to avoid.
			return nil
		})
	}
	_ = g.Wait()
	return sessions
}

// Statuses returns a snapshot of every known session, ordered by name.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	out := make([]Status, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Status returns a snapshot of one session by context name. The second result
// is false when nothing has ever been attempted for that context, which the UI
// shows as "not connected" rather than as a failure.
func (m *Manager) Status(name string) (Status, bool) {
	m.mu.Lock()
	s, ok := m.sessions[name]
	m.mu.Unlock()
	if !ok {
		return Status{Name: name}, false
	}
	return s.Snapshot(), true
}

// Forget drops the session for one context and logs out of its vCenter, so a
// context that was edited or removed leaves nothing of itself behind: no live
// login on a server that is no longer configured, and no status for a name
// that may not exist any more.
//
// Forgetting a name that has no session is not an error — a context that was
// never opened has nothing to invalidate.
func (m *Manager) Forget(ctx context.Context, name string) error {
	m.mu.Lock()
	s, ok := m.sessions[name]
	delete(m.sessions, name)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	s.mu.Lock()
	client := s.client
	s.client = nil
	s.state = Disconnected
	s.mu.Unlock()
	if client == nil {
		return nil
	}
	if err := client.Close(ctx); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Close logs out of every session. Errors are joined so that one vCenter that
// will not answer does not hide the rest.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()

	var errs []error
	for _, s := range sessions {
		s.mu.Lock()
		client := s.client
		s.client = nil
		s.state = Disconnected
		s.mu.Unlock()
		if client == nil {
			continue
		}
		if err := client.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Context.Name, err))
		}
	}
	return errors.Join(errs...)
}
