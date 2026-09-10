package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
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
	Path          string    `yaml:"path"`
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
	ID, Pwd, Build, Launch, Stop, Path string
	Port                               int
	Idle, StartTimeout, StopTimeout    time.Duration
	IncludePrefix                      bool
}

func DefaultPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "local-apps.yaml"), nil
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
	if c.Idle.Duration == 0 {
		c.Idle.Duration = 30 * time.Minute
	}
	if c.StartTimeout.Duration == 0 {
		c.StartTimeout.Duration = 30 * time.Second
	}
	if c.StopTimeout.Duration == 0 {
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
	paths, ports := map[string]string{}, map[int]string{}
	for id, a := range c.Apps {
		if strings.TrimSpace(a.Launch) == "" {
			return RuntimeConfig{}, fmt.Errorf("app %q: launch is required", id)
		}
		if a.Port < 1 || a.Port > 65535 {
			return RuntimeConfig{}, fmt.Errorf("app %q: invalid port %d", id, a.Port)
		}
		if a.Path == "" || !strings.HasPrefix(a.Path, "/") {
			return RuntimeConfig{}, fmt.Errorf("app %q: path must start with /", id)
		}
		path := a.Path
		if path != "/" {
			path = strings.TrimRight(path, "/")
		}
		if prior, ok := paths[path]; ok {
			return RuntimeConfig{}, fmt.Errorf("apps %q and %q use duplicate path %q", prior, id, path)
		}
		paths[path] = id
		if prior, ok := ports[a.Port]; ok {
			return RuntimeConfig{}, fmt.Errorf("apps %q and %q use duplicate port %d", prior, id, a.Port)
		}
		ports[a.Port] = id
		if a.Pwd == "" {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd is required", id)
		}
		st, err := os.Stat(a.Pwd)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd: %w", id, err)
		}
		if !st.IsDir() {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd is not a directory", id)
		}
		idle := c.Idle.Duration
		if a.Idle != nil {
			idle = a.Idle.Duration
		}
		if idle <= 0 {
			return RuntimeConfig{}, fmt.Errorf("app %q: idle must be positive", id)
		}
		r.Apps[id] = RuntimeAppConfig{ID: id, Pwd: a.Pwd, Build: a.Build, Launch: a.Launch, Stop: a.Stop, Path: path, Port: a.Port, Idle: idle, StartTimeout: r.StartTimeout, StopTimeout: r.StopTimeout, IncludePrefix: a.IncludePrefix}
	}
	return r, nil
}
