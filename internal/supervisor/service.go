package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"lazywrap/internal/config"
	proc "lazywrap/internal/process"
)

var ErrClosing = errors.New("supervisor is shutting down")

type Service struct {
	id             string
	cfg            config.RuntimeAppConfig
	runner         proc.ProcessRunner
	logger         *slog.Logger
	lifecycle      context.Context
	mu             sync.Mutex
	state          State
	process        *proc.Process
	activeRequests int
	idleTimer      *time.Timer
	startDone      chan struct{}
	startErr       error
	stopDone       chan struct{}
	stopErr        error
	generation     uint64
}

func newService(ctx context.Context, c config.RuntimeAppConfig, r proc.ProcessRunner, l *slog.Logger) *Service {
	return &Service{id: c.ID, cfg: c, runner: r, logger: l.With("service", c.ID), lifecycle: ctx, state: StateStopped}
}
func (s *Service) Config() config.RuntimeAppConfig { return s.cfg }
func (s *Service) State() State                    { s.mu.Lock(); defer s.mu.Unlock(); return s.state }
func (s *Service) commandSpec(command, kind string) proc.CommandSpec {
	return proc.CommandSpec{Command: command, Dir: s.cfg.Pwd, Service: s.id, Kind: kind, Env: s.cfg.Env}
}
func (s *Service) Acquire(ctx context.Context) (func(), error) {
	for {
		s.mu.Lock()
		switch s.state {
		case StateRunning:
			s.generation++
			if s.idleTimer != nil {
				s.idleTimer.Stop()
				s.idleTimer = nil
			}
			s.activeRequests++
			s.mu.Unlock()
			var once sync.Once
			return func() { once.Do(s.release) }, nil
		case StateStopped, StateFailed:
			s.state = StateBuilding
			s.startDone = make(chan struct{})
			s.startErr = nil
			ch := s.startDone
			go s.start(ch)
			s.mu.Unlock()
			if e := wait(ctx, ch); e != nil {
				return nil, e
			}
		case StateBuilding, StateStarting:
			ch := s.startDone
			s.mu.Unlock()
			if e := wait(ctx, ch); e != nil {
				return nil, e
			}
		case StateStopping:
			ch := s.stopDone
			s.mu.Unlock()
			if e := wait(ctx, ch); e != nil {
				return nil, e
			}
		}
		s.mu.Lock()
		err := s.startErr
		st := s.state
		s.mu.Unlock()
		if st == StateFailed && err != nil {
			return nil, err
		}
	}
}
func wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}
func (s *Service) start(attempt chan struct{}) {
	ctx, cancel := context.WithTimeout(s.lifecycle, s.cfg.StartTimeout)
	defer cancel()
	if s.cfg.Build != "" {
		s.logger.Info("building")
		if e := s.runner.Run(ctx, s.commandSpec(s.cfg.Build, "build")); e != nil {
			s.fail(attempt, fmt.Errorf("build failed: %w", e))
			return
		}
	}
	s.mu.Lock()
	if s.state != StateBuilding {
		s.mu.Unlock()
		return
	}
	s.state = StateStarting
	s.mu.Unlock()
	s.logger.Info("starting")
	// The process uses the service lifecycle; only readiness is bounded by startTimeout.
	p, e := s.runner.Start(s.lifecycle, s.commandSpec(s.cfg.Launch, "launch"))
	if e != nil {
		s.fail(attempt, fmt.Errorf("launch failed: %w", e))
		return
	}
	s.mu.Lock()
	s.process = p
	s.mu.Unlock()
	if e = s.ready(ctx, p); e != nil {
		_ = p.Terminate()
		timer := time.NewTimer(s.cfg.StopTimeout)
		select {
		case <-p.Done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = p.Kill()
		}
		s.fail(attempt, e)
		return
	}
	s.mu.Lock()
	if s.state != StateStarting {
		s.mu.Unlock()
		return
	}
	s.state = StateRunning
	s.startErr = nil
	close(attempt)
	s.scheduleIdleLocked()
	s.mu.Unlock()
	s.logger.Info("ready", "port", s.cfg.Port)
	if s.cfg.Stop == "" {
		go s.watch(p)
	}
}
func (s *Service) ready(ctx context.Context, p *proc.Process) error {
	if s.cfg.GRPCHealth {
		return s.grpcReady(ctx, p)
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
	done := p.Done
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-done:
			result := p.Result()
			if s.cfg.Stop == "" {
				if result.Err == nil {
					return errors.New("launch exited before readiness")
				}
				return fmt.Errorf("launch exited: %w", result.Err)
			}
			if result.Err != nil {
				return fmt.Errorf("launch exited: %w", result.Err)
			}
			done = nil
		case <-ctx.Done():
			return fmt.Errorf("readiness timeout: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func (s *Service) grpcReady(ctx context.Context, p *proc.Process) error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create gRPC health client: %w", err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	done := p.Done
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		response, checkErr := client.Check(checkCtx, &healthpb.HealthCheckRequest{})
		cancel()
		if checkErr == nil && response.GetStatus() == healthpb.HealthCheckResponse_SERVING {
			return nil
		}
		select {
		case <-done:
			result := p.Result()
			if s.cfg.Stop == "" {
				if result.Err == nil {
					return errors.New("launch exited before readiness")
				}
				return fmt.Errorf("launch exited: %w", result.Err)
			}
			if result.Err != nil {
				return fmt.Errorf("launch exited: %w", result.Err)
			}
			done = nil
		case <-ctx.Done():
			return fmt.Errorf("readiness timeout: %w", ctx.Err())
		case <-tick.C:
		}
	}
}
func (s *Service) fail(attempt chan struct{}, e error) {
	s.mu.Lock()
	if s.startDone != attempt || (s.state != StateBuilding && s.state != StateStarting) {
		s.mu.Unlock()
		return
	}
	s.state = StateFailed
	s.startErr = e
	close(attempt)
	s.process = nil
	s.mu.Unlock()
	s.logger.Error("startup failed", "err", e)
}
func (s *Service) watch(p *proc.Process) {
	<-p.Done
	r := p.Result()
	s.mu.Lock()
	if s.process == p && s.state == StateRunning {
		s.generation++
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		s.idleTimer = nil
		s.process = nil
		s.state = StateFailed
		if r.Err == nil {
			s.startErr = errors.New("service exited unexpectedly")
		} else {
			s.startErr = fmt.Errorf("service exited: %w", r.Err)
		}
		s.mu.Unlock()
		s.logger.Warn("service exited unexpectedly", "err", r.Err)
		return
	}
	s.mu.Unlock()
}
func (s *Service) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRequests > 0 {
		s.activeRequests--
	}
	if s.activeRequests == 0 && s.state == StateRunning {
		s.scheduleIdleLocked()
	}
}
func (s *Service) scheduleIdleLocked() {
	s.generation++
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	if s.cfg.Idle == 0 {
		return
	}
	g := s.generation
	s.idleTimer = time.AfterFunc(s.cfg.Idle, func() { s.idle(g) })
}
func (s *Service) idle(g uint64) {
	s.mu.Lock()
	if g != s.generation || s.state != StateRunning || s.activeRequests != 0 {
		s.mu.Unlock()
		return
	}
	s.logger.Info("idle timeout reached")
	s.beginStopLocked()
	s.mu.Unlock()
	go s.stop()
}
func (s *Service) beginStopLocked() {
	wasStarting := s.state == StateBuilding || s.state == StateStarting
	s.generation++
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.state = StateStopping
	s.stopDone = make(chan struct{})
	s.stopErr = nil
	if wasStarting && s.startDone != nil {
		s.startErr = ErrClosing
		close(s.startDone)
		s.startDone = nil
	}
}
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	switch s.state {
	case StateStopped:
		s.mu.Unlock()
		return nil
	case StateStopping:
		ch := s.stopDone
		s.mu.Unlock()
		return s.waitForStop(ctx, ch)
	default:
		s.beginStopLocked()
		ch := s.stopDone
		s.mu.Unlock()
		go s.stop()
		return s.waitForStop(ctx, ch)
	}
}

func (s *Service) waitForStop(ctx context.Context, done <-chan struct{}) error {
	if err := wait(ctx, done); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopErr
}
func (s *Service) stop() {
	s.logger.Info("stopping")
	s.mu.Lock()
	p := s.process
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StopTimeout)
	defer cancel()
	var e error
	if s.cfg.Stop != "" {
		e = s.runner.Run(ctx, s.commandSpec(s.cfg.Stop, "stop"))
		if p != nil {
			select {
			case <-p.Done:
			case <-ctx.Done():
				_ = p.Terminate()
				_ = p.Kill()
			}
		}
	} else if p != nil {
		_ = p.Terminate()
		select {
		case <-p.Done:
		case <-ctx.Done():
			_ = p.Kill()
		}
	}
	s.mu.Lock()
	s.process = nil
	s.state = StateStopped
	s.stopErr = e
	close(s.stopDone)
	s.mu.Unlock()
	if e != nil {
		s.logger.Warn("stop command failed", "err", e)
	}
}
