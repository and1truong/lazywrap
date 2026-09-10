//go:build unix

package process

import (
	"context"
	"io"
	"log/slog"
	"testing"
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
