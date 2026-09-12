package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// resolveEnvironment evaluates inline templates against an immutable snapshot of
// inherited and file variables. Inline entries never depend on map iteration order.
func resolveEnvironment(a AppConfig) (map[string]string, error) {
	base := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok { base[key] = value }
	}
	result := map[string]string{}
	for _, name := range a.EnvFiles {
		if strings.TrimSpace(name) == "" { return nil, fmt.Errorf("envFiles: empty path") }
		path, err := expandHomePath(name)
		if err != nil { return nil, fmt.Errorf("envFiles: expand path: %w", err) }
		if !filepath.IsAbs(path) { path = filepath.Join(a.Pwd, path) }
		data, err := os.ReadFile(path)
		if err != nil { return nil, fmt.Errorf("envFiles: %w", err) }
		values, err := parseEnvFile(string(data))
		if err != nil { return nil, fmt.Errorf("envFiles %q: %w", path, err) }
		for key, value := range values { base[key] = value; result[key] = value }
	}
	for key, value := range a.Env {
		resolved, err := interpolateEnv(value, base)
		if err != nil { return nil, fmt.Errorf("environment variable %q: %w", key, err) }
		result[key] = resolved
	}
	if err := validateEnvironment(result); err != nil { return nil, err }
	return result, nil
}

func interpolateEnv(value string, base map[string]string) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(value, "{{")
		if start < 0 { out.WriteString(value); return out.String(), nil }
		out.WriteString(value[:start])
		value = value[start+2:]
		end := strings.Index(value, "}}")
		if end < 0 { return "", fmt.Errorf("unterminated interpolation") }
		key, fallback, hasDefault := strings.Cut(value[:end], ":")
		if !envName.MatchString(key) { return "", fmt.Errorf("invalid interpolation name") }
		replacement, exists := base[key]
		if !exists {
			if !hasDefault { return "", fmt.Errorf("referenced variable %q is not set", key) }
			replacement = fallback
		}
		out.WriteString(replacement)
		value = value[end+2:]
	}
}

// parseEnvFile supports single-line dotenv assignments, optional export,
// quotes, and comments. Values are data: no shell or variable expansion.
func parseEnvFile(data string) (map[string]string, error) {
	values := map[string]string{}
	for index, line := range strings.Split(strings.TrimPrefix(data, "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") { continue }
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[7:])
		}
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envName.MatchString(key) { return nil, fmt.Errorf("line %d: invalid assignment", index+1) }
		value, err := parseEnvValue(strings.TrimSpace(raw))
		if err != nil { return nil, fmt.Errorf("line %d: invalid value", index+1) }
		values[key] = value
	}
	if err := validateEnvironment(values); err != nil { return nil, err }
	return values, nil
}

func parseEnvValue(raw string) (string, error) {
	if raw == "" { return "", nil }
	if raw[0] != '\'' && raw[0] != '"' {
		for i := 0; i < len(raw); i++ {
			if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
				return strings.TrimSpace(raw[:i]), nil
			}
		}
		return raw, nil
	}
	quote := raw[0]
	var out strings.Builder
	for i := 1; i < len(raw); i++ {
		ch := raw[i]
		if ch == quote {
			tail := strings.TrimSpace(raw[i+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") { return "", fmt.Errorf("trailing text") }
			return out.String(), nil
		}
		if ch == '\\' && quote == '"' && i+1 < len(raw) {
			i++
			switch raw[i] {
			case 'n': out.WriteByte('\n')
			case 'r': out.WriteByte('\r')
			case 't': out.WriteByte('\t')
			case '\\', '"': out.WriteByte(raw[i])
			default: out.WriteByte('\\'); out.WriteByte(raw[i])
			}
		} else { out.WriteByte(ch) }
	}
	return "", fmt.Errorf("unterminated quote")
}
