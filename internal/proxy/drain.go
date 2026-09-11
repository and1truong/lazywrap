package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
)

// DrainHandler rejects new requests after draining begins and reports when all
// requests that were already in flight have completed. This includes requests
// served on h2c connections, which net/http does not track after they are
// hijacked by h2c.NewHandler.
type DrainHandler struct {
	next http.Handler

	mu       sync.Mutex
	active   int
	draining bool
	done     chan struct{}
}

func NewDrainHandler(next http.Handler) *DrainHandler {
	return &DrainHandler{next: next, done: make(chan struct{})}
}

func (h *DrainHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.draining {
		h.mu.Unlock()
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/grpc") {
			writeGRPCError(w, codes.Unavailable, "server shutting down")
			return
		}
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	h.active++
	h.mu.Unlock()

	defer h.release()
	h.next.ServeHTTP(w, r)
}

// BeginDrain prevents new requests from entering the wrapped handler. The
// returned channel closes after every request admitted before this call exits.
func (h *DrainHandler) BeginDrain() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.draining {
		h.draining = true
		if h.active == 0 {
			close(h.done)
		}
	}
	return h.done
}

func (h *DrainHandler) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
	if h.draining && h.active == 0 {
		close(h.done)
	}
}

// ConnectionTracker wraps accepted connections so h2c connections remain
// closable after net/http marks them hijacked and stops managing them.
type ConnectionTracker struct {
	mu      sync.Mutex
	conns   map[*trackedConn]struct{}
	closed  bool
	changed chan struct{}
}

func NewConnectionTracker() *ConnectionTracker {
	return &ConnectionTracker{conns: make(map[*trackedConn]struct{}), changed: make(chan struct{})}
}

// DrainServer stops accepting work, waits for handlers admitted before the
// drain to finish, and then closes any connections net/http no longer owns.
// The final step is required for connections hijacked by h2c.NewHandler.
func DrainServer(ctx context.Context, server *http.Server, handler *DrainHandler, connections *ConnectionTracker) error {
	drained := handler.BeginDrain()
	shutdownErr := server.Shutdown(ctx)
	var drainErr error
	select {
	case <-drained:
	case <-ctx.Done():
		drainErr = ctx.Err()
	}
	connectionErr := connections.Wait(ctx)
	return errors.Join(shutdownErr, drainErr, connectionErr, connections.CloseAll())
}

func (t *ConnectionTracker) Track(listener net.Listener) net.Listener {
	return &trackedListener{Listener: listener, tracker: t}
}

// CloseAll closes every accepted connection, including connections hijacked
// by h2c.NewHandler. Call it only after new requests are blocked and admitted
// requests have drained.
func (t *ConnectionTracker) CloseAll() error {
	t.mu.Lock()
	t.closed = true
	connections := make([]*trackedConn, 0, len(t.conns))
	for conn := range t.conns {
		connections = append(connections, conn)
	}
	t.mu.Unlock()

	errs := make([]error, 0)
	for _, conn := range connections {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Wait blocks until all accepted connections have closed. When the HTTP/2
// server is configured with http2.ConfigureServer, graceful shutdown sends
// GOAWAY and causes h2c connections to close after their streams complete.
func (t *ConnectionTracker) Wait(ctx context.Context) error {
	for {
		t.mu.Lock()
		if len(t.conns) == 0 {
			t.mu.Unlock()
			return nil
		}
		changed := t.changed
		t.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *ConnectionTracker) add(conn *trackedConn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.conns[conn] = struct{}{}
	t.signalLocked()
	return true
}

func (t *ConnectionTracker) remove(conn *trackedConn) {
	t.mu.Lock()
	delete(t.conns, conn)
	t.signalLocked()
	t.mu.Unlock()
}

func (t *ConnectionTracker) signalLocked() {
	close(t.changed)
	t.changed = make(chan struct{})
}

type trackedListener struct {
	net.Listener
	tracker *ConnectionTracker
}

func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, tracker: l.tracker}
	if !l.tracker.add(tracked) {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	return tracked, nil
}

type trackedConn struct {
	net.Conn
	tracker *ConnectionTracker
	once    sync.Once
	err     error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.tracker.remove(c)
	})
	return c.err
}
