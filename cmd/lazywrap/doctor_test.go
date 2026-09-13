package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDoctorChecksAndListsAppsInStableOrder(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "lazywrap.yaml")
	contents := fmt.Sprintf(`port: 3000
apps:
  web:
    pwd: %s
    launch: web-server
    path: /web
    port: 8080
  database:
    pwd: %s
    launch: postgres
    protocol: tcp
    listenPort: 15432
    port: 5432
  api:
    pwd: %s
    launch: api-server
    protocol: grpc
    port: 50051
`, strconv.Quote(dir), strconv.Quote(dir), strconv.Quote(dir))
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runArgs([]string{"doctor", "-c", configPath}, &output); err != nil {
		t.Fatal(err)
	}

	want := fmt.Sprintf(`[ok] configuration: %s
[ok] app api: grpc://api.localhost:3000 -> 127.0.0.1:50051 (pwd: %s)
[ok] app database: tcp://127.0.0.1:15432 -> 127.0.0.1:5432 (pwd: %s)
[ok] app web: http://127.0.0.1:3000/web -> 127.0.0.1:8080 (pwd: %s)
[ok] 3 app(s) checked
`, configPath, dir, dir, dir)
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestDoctorAcceptsGlobalConfigFlagBeforeSubcommand(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "lazywrap.yaml")
	contents := fmt.Sprintf("apps:\n  api:\n    pwd: %s\n    launch: server\n    port: 8080\n", strconv.Quote(dir))
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runArgs([]string{"-c", configPath, "doctor"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "[ok] 1 app(s) checked") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestDoctorAcceptsProcessOnlyAppWithoutRunningCommands(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched")
	configPath := filepath.Join(dir, "lazywrap.yaml")
	contents := fmt.Sprintf("apps:\n  api:\n    pwd: %s\n    launch: touch %s\n    port: 0\n", strconv.Quote(dir), strconv.Quote(marker))
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runArgs([]string{"doctor", "-c", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "app api: process only") {
		t.Fatalf("output = %q", output.String())
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("doctor executed launch command; stat error = %v", statErr)
	}
}

func TestDoctorRejectsUnexpectedArguments(t *testing.T) {
	err := runDoctorArgs("unused.yaml", []string{"extra"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("error = %v", err)
	}
}

func TestDoctorRejectsUnknownConfigFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "lazywrap.yaml")
	contents := fmt.Sprintf("apps:\n  api:\n    pwd: %s\n    lauch: server\n    launch: server\n    port: 8080\n", strconv.Quote(dir))
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	err := runDoctor(configPath, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `field lauch not found`) {
		t.Fatalf("error = %v", err)
	}
}

func TestDoctorShowsComposedAppSource(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "lazywrap.yaml")
	resourcePath := filepath.Join(dir, "apps.yaml")
	if err := os.WriteFile(configPath, []byte("resources:\n  - apps.yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("apps:\n  api:\n    pwd: %s\n    launch: server\n    port: 8080\n", strconv.Quote(dir))
	if err := os.WriteFile(resourcePath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runDoctor(configPath, &output); err != nil {
		t.Fatal(err)
	}
	canonicalResourcePath, err := filepath.EvalSymlinks(resourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "source: "+canonicalResourcePath) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestDoctorDoesNotShowSourceForHomeRelativeRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, "lazywrap.yaml")
	contents := fmt.Sprintf("apps:\n  api:\n    pwd: %s\n    launch: server\n    port: 8080\n", strconv.Quote(home))
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runDoctor("~/lazywrap.yaml", &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "source:") {
		t.Fatalf("root app unexpectedly includes source annotation: %q", output.String())
	}
}
