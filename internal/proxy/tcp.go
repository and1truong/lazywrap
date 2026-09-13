package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/supervisor"
)

var ErrTCPServerClosed = errors.New("TCP proxy server closed")

// TCPServer lazy-starts one service and proxies raw TCP connections to it.
// A connection holds a supervisor lease for its complete lifetime, so idle
// shutdown cannot stop the service while clients are still connected.
type TCPServer struct {
	serviceID string
	addr      string
	target    string
	sup       *supervisor.Supervisor
	logger    *slog.Logger
	ctx       context.Context
	cancel    context.CancelFunc

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	closing  bool
	wg       sync.WaitGroup
}

func NewTCPServer(app config.RuntimeAppConfig, sup *supervisor.Supervisor, logger *slog.Logger) *TCPServer {
	endpoints := app.EndpointList()
	if len(endpoints) == 0 {
		panic("TCP server requires an endpoint")
	}
	return NewTCPEndpointServer(app.ID, endpoints[0], sup, logger)
}

func NewTCPEndpointServer(serviceID string, endpoint config.RuntimeEndpointConfig, sup *supervisor.Supervisor, logger *slog.Logger) *TCPServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &TCPServer{
		serviceID: serviceID,
		addr:      net.JoinHostPort("127.0.0.1", strconv.Itoa(endpoint.ListenPort)),
		target:    net.JoinHostPort("127.0.0.1", strconv.Itoa(endpoint.Port)),
		sup:       sup,
		logger:    logger.With("service", serviceID, "endpoint", endpoint.Name, "protocol", "tcp"),
		ctx:       ctx,
		cancel:    cancel,
		conns:     make(map[net.Conn]struct{}),
	}
}

func (s *TCPServer) Addr() string { return s.addr }

func (s *TCPServer) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

func (s *TCPServer) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		_ = ln.Close()
		return ErrTCPServerClosed
	}
	if s.listener != nil {
		s.mu.Unlock()
		_ = ln.Close()
		return errors.New("TCP proxy server already serving")
	}
	s.listener = ln
	s.mu.Unlock()

	s.logger.Info("listening", "address", ln.Addr().String(), "target", s.target)
	for {
		client, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing || errors.Is(err, net.ErrClosed) {
				return ErrTCPServerClosed
			}
			return err
		}
		if !s.trackClient(client) {
			_ = client.Close()
			continue
		}
		go s.handle(client)
	}
}

func (s *TCPServer) handle(client net.Conn) {
	defer s.wg.Done()
	defer s.untrackAndClose(client)

	release, err := s.sup.Acquire(s.ctx, s.serviceID)
	if err != nil {
		if s.ctx.Err() == nil {
			s.logger.Error("service unavailable", "err", err)
		}
		return
	}
	defer release()

	backend, err := (&net.Dialer{}).DialContext(s.ctx, "tcp", s.target)
	if err != nil {
		if s.ctx.Err() == nil {
			s.logger.Error("backend connection failed", "target", s.target, "err", err)
		}
		return
	}
	if !s.track(backend) {
		_ = backend.Close()
		return
	}
	defer s.untrackAndClose(backend)

	s.logger.Debug("proxy connection", "client", client.RemoteAddr().String())
	errc := make(chan error, 2)
	go copyTCP(backend, client, errc)
	go copyTCP(client, backend, errc)
	<-errc
	_ = client.Close()
	_ = backend.Close()
	<-errc
}

func copyTCP(dst, src net.Conn, result chan<- error) {
	_, err := io.Copy(dst, src)
	result <- err
}

func (s *TCPServer) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *TCPServer) trackClient(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.conns[conn] = struct{}{}
	// Add while holding mu so Shutdown cannot begin waiting between the
	// closing check and the wait-group increment.
	s.wg.Add(1)
	return true
}

func (s *TCPServer) untrackAndClose(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// Shutdown stops accepting connections and closes active proxy connections.
func (s *TCPServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.cancel()
	}
	ln := s.listener
	connections := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		connections = append(connections, conn)
	}
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("TCP proxy shutdown: %w", ctx.Err())
	}
}
