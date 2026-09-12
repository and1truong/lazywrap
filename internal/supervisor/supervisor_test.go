package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"lazywrap/internal/config"
	proc "lazywrap/internal/process"
)

type stopAllRunner struct {
	fastErr     error
	slowStarted chan struct{}
	releaseSlow chan struct{}
}

func (r *stopAllRunner) Run(ctx context.Context, spec proc.CommandSpec) error {
	switch spec.Service {
	case "fast":
		return r.fastErr
	case "slow":
		close(r.slowStarted)
		select {
		case <-r.releaseSlow:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	default:
		return nil
	}
}

func (r *stopAllRunner) Start(context.Context, proc.CommandSpec) (*proc.Process, error) {
	return nil, errors.New("unexpected Start call")
}

func TestStopAllWaitsForEveryServiceAfterFailure(t *testing.T) {
	fastErr := errors.New("fast stop failed")
	runner := &stopAllRunner{
		fastErr:     fastErr,
		slowStarted: make(chan struct{}),
		releaseSlow: make(chan struct{}),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{
		"fast": {ID: "fast", Stop: "fast-stop", StopTimeout: time.Second},
		"slow": {ID: "slow", Stop: "slow-stop", StopTimeout: time.Second},
	}}
	sup := New(cfg, runner, logger)
	for _, service := range sup.services {
		service.state = StateRunning
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sup.StopAll(ctx) }()

	select {
	case <-runner.slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow service stop did not start")
	}

	select {
	case err := <-done:
		t.Fatalf("StopAll returned before slow service finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(runner.releaseSlow)
	select {
	case err := <-done:
		if !errors.Is(err, fastErr) {
			t.Fatalf("StopAll error = %v, want %v", err, fastErr)
		}
	case <-time.After(time.Second):
		t.Fatal("StopAll did not return after all services finished")
	}
}
