package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML configuration.
type Config struct {
	Pool         PoolConfig     `yaml:"pool"`
	ScanInterval Duration       `yaml:"scan_interval"`
	DBPath       string         `yaml:"db_path"`
	ListenAddr   string         `yaml:"listen_addr"`
	TrustedCIDRs string         `yaml:"trusted_cidrs"`
	Pipelines    []PipelineConfig `yaml:"pipelines"`
}

// PoolConfig controls the worker pool.
type PoolConfig struct {
	Size           int      `yaml:"size"`
	ShrinkGrace    Duration `yaml:"shrink_grace"`
	ShrinkKillOrder string  `yaml:"shrink_kill_order"` // "oldest" or "youngest"
}

// PipelineConfig defines a single conversion pipeline.
type PipelineConfig struct {
	Name      string            `yaml:"name"`
	Priority  int               `yaml:"priority"`
	Paths     []string          `yaml:"paths"`
	Direction string            `yaml:"direction"` // "oldest" or "newest"
	MinAge    *Duration         `yaml:"min_age"`
	MaxAge    *Duration         `yaml:"max_age"`
	Target    TargetConfig      `yaml:"target"`
	Command   string            `yaml:"command"`
	Extra     map[string]any    `yaml:"extra"`
}

// TargetConfig describes how to derive the output path from the input path.
type TargetConfig struct {
	Regex  string `yaml:"regex"`
	Format string `yaml:"format"`
}

// Duration is a yaml-unmarshallable time.Duration.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}

// UnmarshalJSON implements json.Unmarshaler for Duration.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = dur
	return nil
}

// Load reads and parses the YAML config at path, then applies any
// REFINERY_* environment variable overrides.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyEnv(&cfg)
	return &cfg, nil
}

// applyEnv overrides config fields with values from REFINERY_* env vars.
func applyEnv(cfg *Config) {
	if v := os.Getenv("REFINERY_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("REFINERY_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("REFINERY_TRUSTED_CIDRS"); v != "" {
		cfg.TrustedCIDRs = v
	}
}

// Validate returns an error if the config is invalid.
func Validate(cfg *Config) error {
	if cfg.Pool.Size <= 0 {
		cfg.Pool.Size = 4
	}
	if cfg.Pool.ShrinkGrace.Duration == 0 {
		cfg.Pool.ShrinkGrace.Duration = time.Minute
	}
	if cfg.Pool.ShrinkKillOrder == "" {
		cfg.Pool.ShrinkKillOrder = "oldest"
	}
	if cfg.Pool.ShrinkKillOrder != "oldest" && cfg.Pool.ShrinkKillOrder != "youngest" {
		return fmt.Errorf("pool.shrink_kill_order must be 'oldest' or 'youngest', got %q", cfg.Pool.ShrinkKillOrder)
	}
	if cfg.ScanInterval.Duration == 0 {
		cfg.ScanInterval.Duration = 30 * time.Second
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "sticky-refinery.db"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}

	names := make(map[string]bool)
	for i, p := range cfg.Pipelines {
		if p.Name == "" {
			return fmt.Errorf("pipelines[%d]: name is required", i)
		}
		if names[p.Name] {
			return fmt.Errorf("pipelines[%d]: duplicate name %q", i, p.Name)
		}
		names[p.Name] = true
		if len(p.Paths) == 0 {
			return fmt.Errorf("pipeline %q: paths is required", p.Name)
		}
		if p.Command == "" {
			return fmt.Errorf("pipeline %q: command is required", p.Name)
		}
		if p.Target.Format == "" {
			return fmt.Errorf("pipeline %q: target.format is required", p.Name)
		}
		if p.Direction == "" {
			cfg.Pipelines[i].Direction = "oldest"
		}
		if p.Direction != "oldest" && p.Direction != "newest" {
			return fmt.Errorf("pipeline %q: direction must be 'oldest' or 'newest'", p.Name)
		}
	}
	return nil
}
