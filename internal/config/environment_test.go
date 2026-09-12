package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFilesAndInterpolation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LW_PARENT", "parent")
	t.Setenv("LW_OVERRIDE", "parent")
	for name, content := range map[string]string{
		"base.env": "LW_OVERRIDE=first\nLW_EMPTY=\nLW_FILE=from-file\n",
		"local.env": "LW_OVERRIDE=second\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil { t.Fatal(err) }
	}
	path := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf(`apps:
  api:
    pwd: %q
    launch: server
    port: 1980
    envFiles:
      - base.env
      - %q
    env:
      LW_OVERRIDE: "inline {{LW_OVERRIDE}}"
      MESSAGE: "{{LW_PARENT}}/{{LW_FILE}}/{{LW_MISSING_TEST:default:with:colons}}"
      EMPTY: "{{LW_EMPTY:fallback}}"
      LITERAL: "${HOME}"
  other:
    pwd: %q
    launch: server
    port: 1981
`, dir, filepath.Join(dir, "local.env"), dir)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil { t.Fatal(err) }
	cfg, err := Load(path)
	if err != nil { t.Fatal(err) }
	want := map[string]string{"LW_OVERRIDE":"inline second", "LW_EMPTY":"", "LW_FILE":"from-file", "MESSAGE":"parent/from-file/default:with:colons", "EMPTY":"", "LITERAL":"${HOME}"}
	if !maps.Equal(cfg.Apps["api"].Env, want) { t.Fatalf("unexpected environment: %#v", cfg.Apps["api"].Env) }
	if len(cfg.Apps["other"].Env) != 0 { t.Fatal("variables leaked to another service") }
	if os.Getenv("LW_OVERRIDE") != "parent" { t.Fatal("parent environment mutated") }
}

func TestInterpolateEnv(t *testing.T) {
	base := map[string]string{"A":"one", "EMPTY":"", "RAW":"{{MISSING}}"}
	for _, tc := range []struct { input, want string; fail bool }{
		{"prefix {{A}} {{A}} suffix", "prefix one one suffix", false},
		{"{{MISSING:}}", "", false},
		{"{{EMPTY:default}}", "", false},
		{"{{MISSING:https://localhost:3000}}", "https://localhost:3000", false},
		{"{{RAW}}", "{{MISSING}}", false},
		{"${A}", "${A}", false},
		{"{{MISSING}}", "", true},
		{"{{A", "", true},
		{"{{:secret}}", "", true},
		{"{{BAD NAME:secret}}", "", true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := interpolateEnv(tc.input, base)
			if (err != nil) != tc.fail || got != tc.want { t.Fatalf("got %q, %v", got, err) }
			if err != nil && strings.Contains(err.Error(), "secret") { t.Fatal("error leaked value") }
		})
	}
}

func TestParseEnvFile(t *testing.T) {
	got, err := parseEnvFile("\ufeff# comment\r\nexport A = value # comment\r\nB='literal # ${A}'\nC=\"line\\nquote\\\"\" # comment\nD=hello#world\nEMPTY=\nA=last\nCMD=$(touch /tmp/never)\n")
	if err != nil { t.Fatal(err) }
	want := map[string]string{"A":"last", "B":"literal # ${A}", "C":"line\nquote\"", "D":"hello#world", "EMPTY":"", "CMD":"$(touch /tmp/never)"}
	if !maps.Equal(got, want) { t.Fatalf("got %#v", got) }
	for _, input := range []string{"NO_EQUALS", "1BAD=x", "A='secret", "A=\"secret\" trailing", "A=secret\x00"} {
		_, err := parseEnvFile(input)
		if err == nil { t.Fatalf("accepted malformed input %q", input) }
		if strings.Contains(err.Error(), "secret") { t.Fatal("error leaked secret") }
	}
}

func TestResolveEnvironmentRejectsBadFiles(t *testing.T) {
	for _, files := range [][]string{{""}, {"missing.env"}} {
		_, err := resolveEnvironment(AppConfig{Pwd:t.TempDir(), EnvFiles:files})
		if err == nil { t.Fatal("expected envFiles error") }
	}
}
