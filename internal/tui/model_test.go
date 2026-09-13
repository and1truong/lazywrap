package tui

import (
	"lazywrap/internal/observe"
	"lazywrap/internal/supervisor"
	"log/slog"
	"strings"
	"testing"
)

func TestPausedLogsKeepTheirAnchor(t *testing.T) {
	s := observe.New()
	l := slog.New(&observe.Handler{Store: s}).With("service", "api")
	for _, text := range []string{"one", "two", "three", "four"} {
		l.Info(text, "stream", "stdout")
	}
	m := model{selected: "api", offset: 1}
	first := strings.Join(m.logLines(s, 3), "\n")
	l.Info("five", "stream", "stdout")
	second := strings.Join(m.logLines(s, 3), "\n")
	if first != second {
		t.Fatalf("paused logs moved: %q -> %q", first, second)
	}
	m.key("g")
	if !strings.Contains(strings.Join(m.logLines(s, 3), "\n"), "five") {
		t.Fatal("tail did not resume")
	}
}

func TestSelectionFilterAndConfirmedKill(t *testing.T) {
	m := model{apps: []app{{Snapshot: supervisor.Snapshot{ID: "api"}}, {Snapshot: supervisor.Snapshot{ID: "worker"}}}, selected: "worker"}
	m.reselect()
	if m.selected != "worker" {
		t.Fatal("selection moved")
	}
	m.key("k")
	m.selected = "api"
	_, action, id := m.key("y")
	if action != "kill" || id != "worker" {
		t.Fatal("confirmation changed target")
	}
	m.key("/")
	for _, k := range []string{"w", "o", "r", "k", "\r"} {
		m.key(k)
	}
	if m.selected != "worker" {
		t.Fatal("filter failed")
	}
	m.busy = true
	_, action, _ = m.key("S")
	if action != "" {
		t.Fatal("overlapping action allowed")
	}
}

func TestSmallTerminalsAndUntrustedText(t *testing.T) {
	m := model{apps: []app{{Snapshot: supervisor.Snapshot{ID: "api\x1b[2J\a"}}}, selected: "api\x1b[2J\a", history: map[string][]sample{}}
	for _, size := range [][2]int{{1, 1}, {40, 8}, {80, 24}, {120, 40}} {
		v := m.view(size[0], size[1], observe.New())
		if strings.Contains(v, "\x1b[2J") || strings.Contains(v, "\a") {
			t.Fatal("untrusted terminal control leaked")
		}
		if n := len(strings.Split(v, "\r\n")); n != size[1] {
			t.Fatalf("height %d != %d", n, size[1])
		}
	}
}

func TestSplitEscapeSequence(t *testing.T) {
	k, rest := nextKey("\x1b[")
	if k != "" || rest != "\x1b[" {
		t.Fatal("lost prefix")
	}
	k, rest = nextKey(rest + "AS")
	if k != "\x1b[A" || rest != "S" {
		t.Fatal("invalid key framing")
	}
}
