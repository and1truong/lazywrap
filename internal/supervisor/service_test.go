package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lazywrap/internal/config"
	proc "lazywrap/internal/process"
)

type fakeRunner struct {
	mu     sync.Mutex
	specs  []proc.CommandSpec
	starts atomic.Int32
	builds atomic.Int32
	addr   string
	ready  chan struct{}
	done   chan struct{}
	ln     net.Listener
	runErr error
}

func (f *fakeRunner) Run(_ context.Context, spec proc.CommandSpec) error {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	f.builds.Add(1)
	return f.runErr
}

func (f *fakeRunner) Start(_ context.Context, spec proc.CommandSpec) (*proc.Process, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	f.starts.Add(1)
	<-f.ready
	ln, err := net.Listen("tcp", f.addr)
	if err != nil {
		return nil, err
	}
	f.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return &proc.Process{Done: f.done}, nil
}

func (f *fakeRunner) commandSpecs() []proc.CommandSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proc.CommandSpec(nil), f.specs...)
}

func TestConcurrentAcquireStartsOnce(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	addr := probe.Addr().String()
	_ = probe.Close()
	f := &fakeRunner{addr: addr, ready: make(chan struct{}), done: make(chan struct{})}
	s := newService(context.Background(), config.RuntimeAppConfig{ID: "api", Pwd: t.TempDir(), Build: "build", Launch: "launch", Port: port, Idle: time.Hour, StartTimeout: time.Second, StopTimeout: time.Second}, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	const callers = 100
	var wg sync.WaitGroup
	wg.Add(callers)
	errs := make(chan error, callers)
	releases := make(chan func(), callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			release, err := s.Acquire(context.Background())
			errs <- err
			if release != nil {
				releases <- release
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(f.ready)
	wg.Wait()
	close(errs)
	close(releases)
	for err := range errs {
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}
	if f.starts.Load() != 1 || f.builds.Load() != 1 {
		t.Fatalf("starts=%d builds=%d", f.starts.Load(), f.builds.Load())
	}
	for release := range releases {
		release()
	}
	_ = f.ln.Close()
	close(f.done)
}

func TestCancelledWaiterDoesNotCancelStartup(t *testing.T) {
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	port := probe.Addr().(*net.TCPAddr).Port
	addr := probe.Addr().String()
	_ = probe.Close()
	f := &fakeRunner{addr: addr, ready: make(chan struct{}), done: make(chan struct{})}
	s := newService(context.Background(), config.RuntimeAppConfig{ID: "api", Pwd: t.TempDir(), Launch: "launch", Port: port, Idle: time.Hour, StartTimeout: time.Second, StopTimeout: time.Second}, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := s.Acquire(ctx); result <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	close(f.ready)
	release, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if f.starts.Load() != 1 {
		t.Fatalf("starts=%d", f.starts.Load())
	}
	_ = f.ln.Close()
	close(f.done)
}

func TestStopReturnsStopCommandError(t *testing.T) {
	stopErr := errors.New("stop failed")
	f := &fakeRunner{runErr: stopErr}
	s := newService(context.Background(), config.RuntimeAppConfig{
		ID:          "api",
		Pwd:         t.TempDir(),
		Stop:        "stop",
		StopTimeout: time.Second,
	}, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.state = StateRunning

	if err := s.Stop(context.Background()); !errors.Is(err, stopErr) {
		t.Fatalf("Stop() error = %v, want %v", err, stopErr)
	}
}

func TestPerAppEnvironmentReachesAllLifecycleCommands(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	addr := probe.Addr().String()
	_ = probe.Close()
	ready := make(chan struct{})
	close(ready)
	done := make(chan struct{})
	f := &fakeRunner{addr: addr, ready: ready, done: done}
	env := map[string]string{"APP_ENV": "development"}
	s := newService(context.Background(), config.RuntimeAppConfig{
		ID: "api", Pwd: t.TempDir(), Build: "build", Launch: "launch", Stop: "stop", Env: env,
		Port: port, Idle: time.Hour, StartTimeout: time.Second, StopTimeout: time.Second,
	}, f, slog.New(slog.NewTextHandler(io.Discard, nil)))

	release, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	close(done)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	specs := f.commandSpecs()
	if len(specs) != 3 {
		t.Fatalf("lifecycle commands = %d, want 3", len(specs))
	}
	for i, wantKind := range []string{"build", "launch", "stop"} {
		if specs[i].Kind != wantKind {
			t.Fatalf("command %d kind = %q, want %q", i, specs[i].Kind, wantKind)
		}
		if !maps.Equal(specs[i].Env, env) {
			t.Fatalf("%s env = %#v, want %#v", wantKind, specs[i].Env, env)
		}
	}
}

func TestZeroIdleDoesNotScheduleShutdown(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	addr := probe.Addr().String()
	_ = probe.Close()
	f := &fakeRunner{addr: addr, ready: make(chan struct{}), done: make(chan struct{})}
	close(f.ready)
	s := newService(context.Background(), config.RuntimeAppConfig{
		ID: "kafka", Pwd: t.TempDir(), Launch: "launch", Port: port,
		Idle: 0, StartTimeout: time.Second, StopTimeout: time.Second,
	}, f, slog.New(slog.NewTextHandler(io.Discard, nil)))

	release, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	time.Sleep(50 * time.Millisecond)

	if got := s.State(); got != StateRunning {
		t.Fatalf("state = %v, want %v", got, StateRunning)
	}
	_ = f.ln.Close()
	close(f.done)
}
