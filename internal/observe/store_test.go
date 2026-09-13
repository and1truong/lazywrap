package observe

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

func TestConfiguredLevelFiltersLogsAndEvents(t *testing.T) {
	for _, minimum := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(minimum.String(), func(t *testing.T) {
			s := New()
			logger := slog.New(&Handler{Store: s, Level: minimum}).With("service", "api").WithGroup("lifecycle")
			want := 0
			for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
				logger.Log(context.Background(), level, level.String())
				logger.Log(context.Background(), level, level.String(), "stream", "stdout")
				if level >= minimum {
					want++
				}
			}
			for _, events := range []bool{false, true} {
				if entries := s.Entries("api", events); len(entries) != want {
					t.Fatalf("events=%v: got %d entries, want %d", events, len(entries), want)
				}
			}
		})
	}
}

func TestFilteredRecordsCannotEvictWarnings(t *testing.T) {
	s := New()
	logger := slog.New(&Handler{Store: s, Level: slog.LevelWarn}).With("service", "api")
	logger.Warn("retain event")
	logger.Warn("retain stderr", "stream", "stderr")
	for i := 0; i <= Capacity; i++ {
		logger.Debug("debug event")
		logger.Info("info event")
		logger.Info("stdout", "stream", "stdout")
	}
	for _, events := range []bool{false, true} {
		if entries := s.Entries("api", events); len(entries) != 1 {
			t.Fatalf("events=%v: warning displaced by filtered records: %+v", events, entries)
		}
	}
}

func TestBoundedSeparateConcurrentStreams(t *testing.T) {
	s := New()
	logger := slog.New(&Handler{Store: s}).With("service", "api")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < Capacity; j++ {
				logger.Info(fmt.Sprint(j), "stream", "stdout")
			}
		}()
	}
	wg.Wait()
	logger.Info("ready", "port", 1234)
	logs := s.Entries("api", false)
	events := s.Entries("api", true)
	if len(logs) != Capacity || len(events) != 1 || events[0].Stream != "" {
		t.Fatalf("logs=%d events=%+v", len(logs), events)
	}
	logs[0].Text = "mutated"
	if s.Entries("api", false)[0].Text == "mutated" {
		t.Fatal("snapshot aliases mutable storage")
	}
}
