package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/config"
)

const (
	// DefaultListenerApplicationName marks the dedicated LISTEN connection in
	// pg_stat_activity so operators can find (and tests can terminate) it.
	DefaultListenerApplicationName = "relay-realtime-listener"

	defaultMinBackoff = 500 * time.Millisecond
	defaultMaxBackoff = 30 * time.Second
	defaultSubBuffer  = 8
)

// ErrListenerClosed is returned when subscribing after Close.
var ErrListenerClosed = errors.New("realtime: listener is closed")

// ChangeSource matches *Listener and is what the SSE handler depends on.
type ChangeSource interface {
	Listen(ctx context.Context, workspaceID string) (<-chan int64, error)
}

// Listener owns one dedicated PostgreSQL connection that issues
// LISTEN workspace_changes and fans notifications out to per-workspace
// subscribers. The connection is re-established with exponential backoff when
// it is lost; notifications that arrive while disconnected are lost by design
// because clients always pull with their own sequence cursor.
type Listener struct {
	dsn     string
	appName string

	minBackoff time.Duration
	maxBackoff time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	ready     chan struct{}
	readyOnce sync.Once

	mu     sync.RWMutex
	subs   map[string]map[*subscription]struct{}
	closed bool

	wg sync.WaitGroup
}

// ListenerOption customises a Listener.
type ListenerOption func(*Listener)

// WithBackoff overrides the reconnect backoff bounds.
func WithBackoff(min, max time.Duration) ListenerOption {
	return func(l *Listener) {
		if min > 0 {
			l.minBackoff = min
		}
		if max >= l.minBackoff {
			l.maxBackoff = max
		}
	}
}

// WithApplicationName sets the application_name of the dedicated connection.
// Use a unique value per process when several listeners share a database.
func WithApplicationName(name string) ListenerOption {
	return func(l *Listener) {
		if strings.TrimSpace(name) != "" {
			l.appName = strings.TrimSpace(name)
		}
	}
}

// NewListener starts a dedicated PostgreSQL LISTEN connection for dsn. The
// connection is established asynchronously; use WaitReady to block until the
// first LISTEN has been acknowledged. Cancel ctx or call Close to stop the
// listener and release the connection.
func NewListener(ctx context.Context, dsn string, opts ...ListenerOption) (*Listener, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%w: empty DSN", ErrInvalidDSN)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l := &Listener{
		dsn:        strings.TrimSpace(dsn),
		appName:    DefaultListenerApplicationName,
		minBackoff: defaultMinBackoff,
		maxBackoff: defaultMaxBackoff,
		done:       make(chan struct{}),
		ready:      make(chan struct{}),
		subs:       make(map[string]map[*subscription]struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	l.dsn = decorateListenerDSN(l.dsn, l.appName)
	l.ctx, l.cancel = context.WithCancel(ctx)

	l.wg.Add(1)
	go l.run()

	return l, nil
}

// NewListenerFromConfig starts a listener from the API DB configuration.
func NewListenerFromConfig(ctx context.Context, cfg *config.DBConfig, opts ...ListenerOption) (*Listener, error) {
	dsn, err := DSNFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return NewListener(ctx, dsn, opts...)
}

// WaitReady blocks until the LISTEN connection is established for the first
// time, the context is cancelled, or the listener is closed.
func (l *Listener) WaitReady(ctx context.Context) error {
	if l == nil {
		return ErrListenerClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-l.ready:
		return nil
	case <-l.done:
		return ErrListenerClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Listen subscribes to change signals for one workspace. The returned channel
// receives the append-log sequence numbers published for that workspace and is
// closed when ctx is cancelled or the listener is closed. Signals coalesce
// under back-pressure: only the newest pending sequence is kept, which is safe
// because consumers pull from their own cursor.
func (l *Listener) Listen(ctx context.Context, workspaceID string) (<-chan int64, error) {
	if l == nil {
		return nil, ErrListenerClosed
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrEmptyWorkspaceID
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sub := &subscription{ch: make(chan int64, defaultSubBuffer)}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrListenerClosed
	}
	set := l.subs[workspaceID]
	if set == nil {
		set = make(map[*subscription]struct{})
		l.subs[workspaceID] = set
	}
	set[sub] = struct{}{}
	// Register the waiter before releasing the lock so Close's wg.Wait cannot
	// race with a counter that is still zero.
	l.wg.Add(1)
	l.mu.Unlock()

	go func() {
		defer l.wg.Done()
		select {
		case <-ctx.Done():
		case <-l.done:
		}
		l.unsubscribe(workspaceID, sub)
	}()

	return sub.ch, nil
}

// Close stops the listener: subscribers' channels are closed, the dedicated
// connection is released and every goroutine started by the listener exits
// before Close returns.
func (l *Listener) Close() error {
	if l == nil {
		return nil
	}

	l.shutdown()
	l.wg.Wait()
	return nil
}

// shutdown marks the listener closed, cancels the connection loop and closes
// every subscriber channel. It is idempotent and safe to call from Close and
// from the run goroutine when the caller's context is cancelled.
func (l *Listener) shutdown() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	pending := make([]*subscription, 0, len(l.subs))
	for _, set := range l.subs {
		for sub := range set {
			pending = append(pending, sub)
		}
	}
	l.subs = nil
	l.mu.Unlock()

	l.cancel()
	close(l.done)

	for _, sub := range pending {
		sub.close()
	}
}

func (l *Listener) run() {
	defer l.wg.Done()
	// If the caller's context is cancelled without Close, still close the
	// subscriber channels and release their watcher goroutines.
	defer l.shutdown()

	backoff := l.minBackoff
	for {
		if l.ctx.Err() != nil {
			return
		}

		notifications := make(chan *pq.Notification, 128)
		conn, err := pq.NewListenerConn(l.dsn, notifications)
		if err != nil {
			log.Warn().Err(err).Str("component", "realtime.listener").
				Dur("backoff", backoff).Msg("listen connection failed")
			if !l.sleep(backoff) {
				return
			}
			backoff = nextBackoff(backoff, l.maxBackoff)
			continue
		}

		// ListenerConn.Listen blocks until the server acknowledges LISTEN and
		// re-issues it automatically after a reconnect.
		if _, err := conn.Listen(ChannelWorkspaceChanges); err != nil {
			_ = conn.Close()
			drainNotifications(notifications)
			if l.ctx.Err() != nil {
				return
			}
			log.Warn().Err(err).Str("component", "realtime.listener").
				Dur("backoff", backoff).Msg("LISTEN failed")
			if !l.sleep(backoff) {
				return
			}
			backoff = nextBackoff(backoff, l.maxBackoff)
			continue
		}

		backoff = l.minBackoff
		l.readyOnce.Do(func() { close(l.ready) })
		log.Info().Str("component", "realtime.listener").
			Str("channel", ChannelWorkspaceChanges).Msg("listening for workspace changes")

		l.consume(notifications)

		_ = conn.Close()
		// Drain until the pq receiver goroutine closes the channel; this also
		// releases it if it was blocked handing us a notification.
		drainNotifications(notifications)

		if l.ctx.Err() != nil {
			return
		}
		log.Warn().Str("component", "realtime.listener").
			Dur("backoff", l.minBackoff).Msg("listen connection lost, reconnecting")
		if !l.sleep(l.minBackoff) {
			return
		}
	}
}

func (l *Listener) consume(notifications <-chan *pq.Notification) {
	for {
		select {
		case <-l.ctx.Done():
			return
		case n, ok := <-notifications:
			if !ok {
				return
			}
			l.dispatch(n)
		}
	}
}

func (l *Listener) dispatch(n *pq.Notification) {
	if n == nil || n.Channel != ChannelWorkspaceChanges {
		return
	}
	var ev ChangeEvent
	if err := json.Unmarshal([]byte(n.Extra), &ev); err != nil {
		log.Debug().Err(err).Str("component", "realtime.listener").
			Str("payload", n.Extra).Msg("ignoring malformed change signal")
		return
	}
	if ev.WorkspaceID == "" || ev.Seq < 0 {
		return
	}
	l.publish(ev.WorkspaceID, ev.Seq)
}

func (l *Listener) publish(workspaceID string, seq int64) {
	l.mu.RLock()
	set := l.subs[workspaceID]
	targets := make([]*subscription, 0, len(set))
	for sub := range set {
		targets = append(targets, sub)
	}
	l.mu.RUnlock()

	for _, sub := range targets {
		sub.send(seq)
	}
}

func (l *Listener) unsubscribe(workspaceID string, sub *subscription) {
	l.mu.Lock()
	if set, ok := l.subs[workspaceID]; ok {
		delete(set, sub)
		if len(set) == 0 {
			delete(l.subs, workspaceID)
		}
	}
	l.mu.Unlock()
	sub.close()
}

// sleep waits for d, returning false when the listener is shutting down.
func (l *Listener) sleep(d time.Duration) bool {
	if d <= 0 {
		d = time.Millisecond
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-l.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, max time.Duration) time.Duration {
	if max <= 0 {
		max = defaultMaxBackoff
	}
	next := current * 2
	if next <= 0 || next > max {
		return max
	}
	return next
}

func drainNotifications(ch <-chan *pq.Notification) {
	for range ch {
	}
}

// subscription is one caller's signal channel. The mutex makes close safe
// against a concurrent dispatch without holding the listener lock while
// sending.
type subscription struct {
	ch     chan int64
	mu     sync.Mutex
	closed bool
}

func (s *subscription) send(seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- seq:
	default:
		// Buffer full: keep the newest sequence, drop the oldest hint.
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- seq:
		default:
		}
	}
}

func (s *subscription) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// decorateListenerDSN adds the application name and a connect timeout when the
// caller has not set them, so a dead database cannot block Close forever.
func decorateListenerDSN(dsn, appName string) string {
	if !strings.Contains(dsn, "://") {
		// lib/pq keyword/value form: append space-separated parameters.
		extra := ""
		if !strings.Contains(dsn, "application_name=") {
			extra += " application_name=" + appName
		}
		if !strings.Contains(dsn, "connect_timeout=") {
			extra += " connect_timeout=5"
		}
		return dsn + extra
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	if q.Get("application_name") == "" {
		q.Set("application_name", appName)
	}
	if q.Get("connect_timeout") == "" {
		q.Set("connect_timeout", "5")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
