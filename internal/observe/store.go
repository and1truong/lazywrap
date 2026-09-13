// Package observe provides bounded, concurrent log/event snapshots shared by UIs.
package observe

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const Capacity = 500

type Entry struct {
	Seq                   uint64
	At                    time.Time
	Service, Stream, Text string
}

type Store struct {
	mu           sync.Mutex
	seq          uint64
	logs, events map[string][]Entry
}

func New() *Store { return &Store{logs: make(map[string][]Entry), events: make(map[string][]Entry)} }

func (s *Store) append(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.Seq = s.seq
	target := s.events
	if e.Stream != "" {
		target = s.logs
	}
	entries := target[e.Service]
	if len(entries) == Capacity {
		copy(entries, entries[1:])
		entries = entries[:Capacity-1]
	}
	// Bound individual records too; children may emit very long lines.
	if len(e.Text) > 4096 {
		e.Text = e.Text[:4096] + "…"
	}
	target[e.Service] = append(entries, e)
}

func (s *Store) Entries(service string, events bool) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.logs
	if events {
		target = s.events
	}
	return append([]Entry(nil), target[service]...)
}

type Handler struct {
	Store *Store
	Level slog.Level
	attrs []slog.Attr
	group string
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool { return level >= h.Level }
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	e := Entry{At: r.Time, Text: r.Message}
	attrs := append([]slog.Attr(nil), h.attrs...)
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	for _, a := range attrs {
		switch a.Key {
		case "service":
			e.Service = a.Value.String()
		case "stream":
			e.Stream = a.Value.String()
		default:
			e.Text += fmt.Sprintf(" %s=%s", a.Key, a.Value)
		}
	}
	if h.group != "" {
		e.Text = strings.TrimSpace(h.group + " " + e.Text)
	}
	h.Store.append(e)
	return nil
}
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &c
}
func (h *Handler) WithGroup(name string) slog.Handler { c := *h; c.group += name + "."; return &c }
