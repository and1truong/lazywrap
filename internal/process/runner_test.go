//go:build unix

package process

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestProcessCompletionBroadcastsResult(t *testing.T) {
	runner := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
	p, err := runner.Start(context.Background(), CommandSpec{Command: "exit 7", Dir: t.TempDir(), Service: "test", Kind: "launch"})
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	second := make(chan struct{})
	go func() { <-p.Done; close(first) }()
	go func() { <-p.Done; close(second) }()
	<-first
	<-second
	if p.Result().Err == nil {
		t.Fatal("expected non-zero exit result")
	}
}

func TestContextCancellationKillsCommandDescendants(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	runner := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))
	p, err := runner.Start(ctx, CommandSpec{
		Command: `(sleep 0.3; echo survived > survived) & echo ready > ready; wait`,
		Dir:     dir,
		Service: "test",
		Kind:    "build",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(dir + "/ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not create readiness marker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-p.Done:
	case <-time.After(time.Second):
		t.Fatal("command did not stop after context cancellation")
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(dir + "/survived"); !os.IsNotExist(err) {
		t.Fatalf("descendant survived context cancellation: %v", err)
	}
}
