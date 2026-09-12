package lifecycle

import (
	"context"
	"errors"
	proc "lazywrap/internal/process"
	"log/slog"
	"testing"
)

type fakeRunner struct {
	commands []string
	failures map[string]error
}

func (r *fakeRunner) Run(_ context.Context, spec proc.CommandSpec) error {
	r.commands = append(r.commands, spec.Kind+":"+spec.Command)
	return r.failures[spec.Command]
}

func (r *fakeRunner) Start(context.Context, proc.CommandSpec) (*proc.Process, error) {
	panic("unexpected Start call")
}

func TestStartUpRunsSequentiallyOnce(t *testing.T) {
	runner := &fakeRunner{}
	hooks := New([]string{"first", "second"}, nil, runner, slog.Default())

	if err := hooks.StartUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := hooks.StartUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCommands(t, runner.commands, []string{"startUp:first", "startUp:second"})
}

func TestStartUpFailureStopsLaterCommands(t *testing.T) {
	boom := errors.New("exit status 7")
	runner := &fakeRunner{failures: map[string]error{"second": boom}}
	hooks := New([]string{"first", "second", "third"}, nil, runner, slog.Default())

	err := hooks.StartUp(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapped failure", err)
	}
	wantMessage := `startUp command 2 "second" failed: exit status 7`
	if err.Error() != wantMessage {
		t.Fatalf("error = %q, want %q", err, wantMessage)
	}
	assertCommands(t, runner.commands, []string{"startUp:first", "startUp:second"})
}

func TestTearDownAttemptsEveryCommandOnce(t *testing.T) {
	firstErr := errors.New("first failed")
	thirdErr := errors.New("third failed")
	runner := &fakeRunner{failures: map[string]error{"first": firstErr, "third": thirdErr}}
	hooks := New(nil, []string{"first", "second", "third"}, runner, slog.Default())

	err := hooks.TearDown(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, thirdErr) {
		t.Fatalf("error = %v, want both teardown failures", err)
	}
	if again := hooks.TearDown(context.Background()); again != err {
		t.Fatalf("second TearDown error = %v, want %v", again, err)
	}
	assertCommands(t, runner.commands, []string{"tearDown:first", "tearDown:second", "tearDown:third"})
}

func TestOmittedHooksAreNoOps(t *testing.T) {
	runner := &fakeRunner{}
	hooks := New(nil, nil, runner, slog.Default())

	if err := hooks.StartUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := hooks.TearDown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCommands(t, runner.commands, nil)
}

func assertCommands(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("commands = %#v, want %#v", got, want)
		}
	}
}
