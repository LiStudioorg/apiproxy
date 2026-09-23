package config

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	mu        sync.RWMutex
	Server    ServerConfig              `yaml:"server"`
	Platforms map[string]PlatformConfig `yaml:"platforms"`
	path      string
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	AdminPath string `yaml:"admin_path"`
}

type PlatformConfig struct {
	Enabled       bool          `yaml:"enabled"`
	ProfileDir    string        `yaml:"profile_dir"`
	Proxy         string        `yaml:"proxy"`
	Headless      bool          `yaml:"headless"`
	MaxConcurrent int           `yaml:"max_concurrency"`
	MinInterval   time.Duration `yaml:"min_interval"`
	RequestLimit  int64         `yaml:"request_limit"`
}

func Load(path string) (*Config, error) {
	c := &Config{path: path}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return c, nil
}

func (c *Config) Reload() error {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var nc Config
	nc.path = c.path
	if err := yaml.Unmarshal(data, &nc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	nc.applyDefaults()
	c.mu.Lock()
	c.Server = nc.Server
	c.Platforms = nc.Platforms
	c.mu.Unlock()
	return nil
}

func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.AdminPath == "" {
		c.Server.AdminPath = "/admin"
	}
	for name, p := range c.Platforms {
		if p.ProfileDir == "" {
			p.ProfileDir = "./profiles/" + name
		}
		if p.MaxConcurrent <= 0 {
			p.MaxConcurrent = 1
		}
		if p.MinInterval <= 0 {
			p.MinInterval = 2 * time.Second
		}
		if p.RequestLimit <= 0 {
			p.RequestLimit = 150
		}
		c.Platforms[name] = p
	}
}

func (c *Config) GetServer() ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server
}

func (c *Config) GetPlatform(name string) (PlatformConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.Platforms[name]
	return p, ok
}

func (c *Config) Watch(ctx context.Context, interval time.Duration, onChange func()) {
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			fi, err := os.Stat(c.path)
			if err != nil {
				continue
			}
			if fi.ModTime().After(last) {
				last = fi.ModTime()
				if err := c.Reload(); err == nil && onChange != nil {
					onChange()
				}
			}
		}
	}
}
