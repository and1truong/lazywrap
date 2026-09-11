package process

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestCommandEnvironmentInheritsOverridesAndIsolates(t *testing.T) {
	t.Setenv("LAZYWRAP_INHERITED", "parent")
	t.Setenv("LAZYWRAP_OVERRIDE", "parent")
	runner := NewRunner(slog.New(slog.NewTextHandler(io.Discard, nil)))

	first := environmentMap(runner.command(context.Background(), CommandSpec{Env: map[string]string{
		"LAZYWRAP_OVERRIDE": "first",
		"LAZYWRAP_LOCAL":    "only-first",
	}}).Env)
	second := environmentMap(runner.command(context.Background(), CommandSpec{Env: map[string]string{
		"LAZYWRAP_OVERRIDE": "second",
	}}).Env)

	if got := first["LAZYWRAP_INHERITED"]; got != "parent" {
		t.Fatalf("inherited env = %q, want parent", got)
	}
	if got := first["LAZYWRAP_OVERRIDE"]; got != "first" {
		t.Fatalf("first override = %q, want first", got)
	}
	if got := second["LAZYWRAP_OVERRIDE"]; got != "second" {
		t.Fatalf("second override = %q, want second", got)
	}
	if _, ok := second["LAZYWRAP_LOCAL"]; ok {
		t.Fatal("environment variable leaked between app commands")
	}
}

func environmentMap(entries []string) map[string]string {
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}
