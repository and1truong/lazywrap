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

type canceledBuildRunner struct{ entered chan struct{} }

func (r *canceledBuildRunner) Run(ctx context.Context, _ proc.CommandSpec) error {
	close(r.entered)
	<-ctx.Done()
	return ctx.Err()
}
func (r *canceledBuildRunner) Start(context.Context, proc.CommandSpec) (*proc.Process, error) {
	return nil, errors.New("must not launch after canceled build")
}

func TestManualStopCancelsBuildAndBlocksLazyStart(t *testing.T) {
	r := &canceledBuildRunner{make(chan struct{})}
	s := New(config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{"api": {ID: "api", Build: "build", StartTimeout: time.Minute, StopTimeout: time.Second}}}, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer s.StopAll(ctx)
	acquired := make(chan error, 1)
	go func() { _, err := s.Acquire(ctx, "api"); acquired <- err }()
	select {
	case <-r.entered:
	case <-ctx.Done():
		t.Fatal("build did not begin")
	}
	if err := s.Action(ctx, "api", "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err == nil {
			t.Fatal("acquire succeeded after manual stop")
		}
	case <-ctx.Done():
		t.Fatal("acquire stuck")
	}
	if _, err := s.Acquire(ctx, "api"); !errors.Is(err, ErrClosing) {
		t.Fatalf("lazy-start not blocked: %v", err)
	}
	snap := s.Snapshots()[0]
	if snap.State != StateStopped || !snap.ManualStopped || snap.PID != 0 {
		t.Fatalf("snapshot %+v", snap)
	}
}
