package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
	set bool
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
	d.set = true
	return nil
}

type Config struct {
	Port         int                  `yaml:"port"`
	Idle         Duration             `yaml:"idle"`
	LogLevel     string               `yaml:"logLevel"`
	StartTimeout Duration             `yaml:"startTimeout"`
	StopTimeout  Duration             `yaml:"stopTimeout"`
	Apps         map[string]AppConfig `yaml:"apps"`
}
type AppConfig struct {
	Pwd           string    `yaml:"pwd"`
	Build         string    `yaml:"build"`
	Launch        string    `yaml:"launch"`
	Stop          string    `yaml:"stop"`
	Protocol      string    `yaml:"protocol"`
	Path          string    `yaml:"path"`
	Host          string    `yaml:"host"`
	ListenPort    int       `yaml:"listenPort"`
	Port          int       `yaml:"port"`
	Idle          *Duration `yaml:"idle"`
	IncludePrefix bool      `yaml:"includePrefix"`
}
type RuntimeConfig struct {
	Port                      int
	LogLevel                  string
	StartTimeout, StopTimeout time.Duration
	Apps                      map[string]RuntimeAppConfig
}
type RuntimeAppConfig struct {
	ID, Pwd, Build, Launch, Stop, Protocol, Path, Host string
	ListenPort, Port                                   int
	Idle, StartTimeout, StopTimeout                    time.Duration
	IncludePrefix                                      bool
}

const (
	ProtocolHTTP = "http"
	ProtocolTCP  = "tcp"
)

func DefaultPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "lazywrap.yaml"), nil
}

func expandHomePath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func normalizeHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /\\@") || strings.Contains(host, ":") {
		return "", fmt.Errorf("must be a hostname without a port")
	}
	return strings.ToLower(host), nil
}

func Load(path string) (RuntimeConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return RuntimeConfig{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return RuntimeConfig{}, err
	}
	return c.Normalize()
}
func (c Config) Normalize() (RuntimeConfig, error) {
	if c.Port == 0 {
		c.Port = 3000
	}
	if !c.Idle.set && c.Idle.Duration == 0 {
		c.Idle.Duration = 30 * time.Minute
	}
	if !c.StartTimeout.set && c.StartTimeout.Duration == 0 {
		c.StartTimeout.Duration = 30 * time.Second
	}
	if !c.StopTimeout.set && c.StopTimeout.Duration == 0 {
		c.StopTimeout.Duration = 10 * time.Second
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Port < 1 || c.Port > 65535 {
		return RuntimeConfig{}, fmt.Errorf("invalid wrapper port %d", c.Port)
	}
	switch c.LogLevel {
	case "debug", "info", "warning", "warn", "error":
	default:
		return RuntimeConfig{}, fmt.Errorf("invalid log level %q", c.LogLevel)
	}
	if c.Idle.Duration <= 0 || c.StartTimeout.Duration <= 0 || c.StopTimeout.Duration <= 0 {
		return RuntimeConfig{}, fmt.Errorf("durations must be positive")
	}
	r := RuntimeConfig{Port: c.Port, LogLevel: c.LogLevel, StartTimeout: c.StartTimeout.Duration, StopTimeout: c.StopTimeout.Duration, Apps: make(map[string]RuntimeAppConfig, len(c.Apps))}
	paths, hosts, ports, listenPorts := map[string]string{}, map[string]string{}, map[int]string{}, map[int]string{}
	for id, a := range c.Apps {
		if strings.TrimSpace(a.Launch) == "" {
			return RuntimeConfig{}, fmt.Errorf("app %q: launch is required", id)
		}
		if a.Port < 1 || a.Port > 65535 {
			return RuntimeConfig{}, fmt.Errorf("app %q: invalid port %d", id, a.Port)
		}
		protocol := strings.ToLower(strings.TrimSpace(a.Protocol))
		if protocol == "" {
			protocol = ProtocolHTTP
		}
		path, host := strings.TrimSpace(a.Path), strings.TrimSpace(a.Host)
		switch protocol {
		case ProtocolHTTP:
			if a.ListenPort != 0 {
				return RuntimeConfig{}, fmt.Errorf("app %q: listenPort is only supported with TCP", id)
			}
			if (path == "") == (host == "") {
				return RuntimeConfig{}, fmt.Errorf("app %q: exactly one of path or host is required", id)
			}
			if host != "" {
				if a.IncludePrefix {
					return RuntimeConfig{}, fmt.Errorf("app %q: includePrefix is not supported with host routing", id)
				}
				var err error
				host, err = normalizeHost(host)
				if err != nil {
					return RuntimeConfig{}, fmt.Errorf("app %q: host: %w", id, err)
				}
				if prior, ok := hosts[host]; ok {
					return RuntimeConfig{}, fmt.Errorf("apps %q and %q use duplicate host %q", prior, id, host)
				}
				hosts[host] = id
			} else {
				if !strings.HasPrefix(path, "/") {
					return RuntimeConfig{}, fmt.Errorf("app %q: path must start with /", id)
				}
				if path != "/" {
					path = strings.TrimRight(path, "/")
				}
				if prior, ok := paths[path]; ok {
					return RuntimeConfig{}, fmt.Errorf("apps %q and %q use duplicate path %q", prior, id, path)
				}
				paths[path] = id
			}
		case ProtocolTCP:
			if path != "" || host != "" || a.IncludePrefix {
				return RuntimeConfig{}, fmt.Errorf("app %q: path, host, and includePrefix are not supported with TCP", id)
			}
			if a.ListenPort < 1 || a.ListenPort > 65535 {
				return RuntimeConfig{}, fmt.Errorf("app %q: invalid listenPort %d", id, a.ListenPort)
			}
			if a.ListenPort == c.Port {
				return RuntimeConfig{}, fmt.Errorf("app %q: listenPort %d conflicts with wrapper port", id, a.ListenPort)
			}
			if prior, ok := listenPorts[a.ListenPort]; ok {
				return RuntimeConfig{}, fmt.Errorf("apps %q and %q use duplicate listenPort %d", prior, id, a.ListenPort)
			}
			listenPorts[a.ListenPort] = id
		default:
			return RuntimeConfig{}, fmt.Errorf("app %q: invalid protocol %q", id, a.Protocol)
		}
		if prior, ok := ports[a.Port]; ok {
			return RuntimeConfig{}, fmt.Errorf("apps %q and %q use duplicate port %d", prior, id, a.Port)
		}
		ports[a.Port] = id
		if a.Pwd == "" {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd is required", id)
		}
		pwd, err := expandHomePath(a.Pwd)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: expand pwd: %w", id, err)
		}
		st, err := os.Stat(pwd)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd: %w", id, err)
		}
		if !st.IsDir() {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd is not a directory", id)
		}
		a.Pwd = pwd
		idle := c.Idle.Duration
		if a.Idle != nil {
			idle = a.Idle.Duration
		}
		if idle < 0 {
			return RuntimeConfig{}, fmt.Errorf("app %q: idle must be non-negative", id)
		}
		r.Apps[id] = RuntimeAppConfig{ID: id, Pwd: a.Pwd, Build: a.Build, Launch: a.Launch, Stop: a.Stop, Protocol: protocol, Path: path, Host: host, ListenPort: a.ListenPort, Port: a.Port, Idle: idle, StartTimeout: r.StartTimeout, StopTimeout: r.StopTimeout, IncludePrefix: a.IncludePrefix}
	}
	return r, nil
}
