package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func baseConfig(dir string) Config {
	return Config{Port: 3000, Apps: map[string]AppConfig{"api": {Pwd: dir, Launch: "server", Path: "/service/api/", Port: 1980}}}
}

func TestNormalizeDefaultsAndPath(t *testing.T) {
	cfg, err := baseConfig(t.TempDir()).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Path != "/service/api" {
		t.Fatalf("path = %q", cfg.Apps["api"].Path)
	}
	if cfg.Apps["api"].Idle != 30*time.Minute || cfg.StartTimeout != 30*time.Second || cfg.StopTimeout != 10*time.Second {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
}

func TestDefaultPathUsesLazywrapName(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "lazywrap.yaml" {
		t.Fatalf("default config path = %q", path)
	}
}

func TestNormalizeExpandsHomeRelativePwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	c := baseConfig(t.TempDir())
	app := c.Apps["api"]
	app.Pwd = "~/"
	c.Apps["api"] = app
	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Pwd != home {
		t.Fatalf("pwd = %q, want %q", cfg.Apps["api"].Pwd, home)
	}
}

func TestNormalizeRoutingModes(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name     string
		app      AppConfig
		wantErr  string
		wantHost string
	}{
		{"host only", AppConfig{Pwd: dir, Launch: "server", Host: "FOO.LocalHost", Port: 1980}, "", "foo.localhost"},
		{"path only", AppConfig{Pwd: dir, Launch: "server", Path: "/service/foo", Port: 1980}, "", ""},
		{"path and host", AppConfig{Pwd: dir, Launch: "server", Path: "/service/foo", Host: "foo.localhost", Port: 1980}, "exactly one of path or host is required", ""},
		{"neither path nor host", AppConfig{Pwd: dir, Launch: "server", Port: 1980}, "exactly one of path or host is required", ""},
		{"include prefix with host", AppConfig{Pwd: dir, Launch: "server", Host: "foo.localhost", Port: 1980, IncludePrefix: true}, "includePrefix is not supported", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Apps: map[string]AppConfig{"api": tt.app}}
			got, err := c.Normalize()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Apps["api"].Host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.Apps["api"].Host, tt.wantHost)
			}
		})
	}
}

func TestNormalizeRejectsDuplicateHosts(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"foo": {Pwd: dir, Launch: "server", Host: "foo.localhost", Port: 1980},
		"bar": {Pwd: dir, Launch: "server", Host: "FOO.LOCALHOST", Port: 1981},
	}}
	if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "duplicate host") {
		t.Fatalf("error = %v, want duplicate host", err)
	}
}

func TestNormalizeIdleOverride(t *testing.T) {
	c := baseConfig(t.TempDir())
	c.Idle = Duration{Duration: time.Hour}
	override := Duration{Duration: time.Minute}
	app := c.Apps["api"]
	app.Idle = &override
	c.Apps["api"] = app
	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Idle != time.Minute {
		t.Fatal("idle override not applied")
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad port", func(c *Config) { c.Port = 70000 }, "invalid wrapper port"},
		{"bad service port", func(c *Config) { a := c.Apps["api"]; a.Port = 0; c.Apps["api"] = a }, "invalid port"},
		{"missing launch", func(c *Config) { a := c.Apps["api"]; a.Launch = ""; c.Apps["api"] = a }, "launch is required"},
		{"bad path", func(c *Config) { a := c.Apps["api"]; a.Path = "api"; c.Apps["api"] = a }, "path must start"},
		{"bad pwd", func(c *Config) { a := c.Apps["api"]; a.Pwd = file; c.Apps["api"] = a }, "not a directory"},
		{"duplicate path", func(c *Config) {
			c.Apps["web"] = AppConfig{Pwd: dir, Launch: "server", Path: "/service/api", Port: 1981}
		}, "duplicate path"},
		{"duplicate port", func(c *Config) { c.Apps["web"] = AppConfig{Pwd: dir, Launch: "server", Path: "/web", Port: 1980} }, "duplicate port"},
		{"log level", func(c *Config) { c.LogLevel = "verbose" }, "invalid log level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig(dir)
			tt.mutate(&c)
			_, err := c.Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(p, []byte("idle: forever\n"), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected duration error")
	}
}

func TestExplicitZeroDurationIsRejected(t *testing.T) {
	zero := Duration{Duration: 0, set: true}
	c := baseConfig(t.TempDir())
	c.Idle = zero
	if _, err := c.Normalize(); err == nil {
		t.Fatal("expected explicit zero duration to be rejected")
	}
}
