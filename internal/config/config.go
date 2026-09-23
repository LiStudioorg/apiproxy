package config

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 由根目录单份 config.toml 加载，含 [server] / [auth] / [platforms.xxx] 三块。
type Config struct {
	mu        sync.RWMutex
	Server    ServerConfig              `toml:"server"`
	Auth      AuthConfig                `toml:"auth"`
	Platforms map[string]PlatformConfig `toml:"platforms"`
	path      string
}

type ServerConfig struct {
	Host          string `toml:"host"`
	Port          int    `toml:"port"`
	TLS           bool   `toml:"tls"`
	TLSCert       string `toml:"tls_cert"`
	TLSKey        string `toml:"tls_key"`
	QueueCapacity int    `toml:"queue_capacity"`
	QueueWorkers  int    `toml:"queue_workers"`
	// 单浏览器模式下的全局 profile 目录与代理（各平台 Cookie 按域名隔离，共用一个浏览器）。
	ProfileDir string `toml:"profile_dir"`
	Proxy      string `toml:"proxy"`
}

func (s ServerConfig) UseTLS() bool {
	return s.TLS && s.TLSCert != "" && s.TLSKey != ""
}

// TLSMisconfigured 开启 tls 但证书/密钥缺失。
func (s ServerConfig) TLSMisconfigured() bool {
	return s.TLS && (s.TLSCert == "" || s.TLSKey == "")
}

type AuthConfig struct {
	Enabled    bool          `toml:"enabled"`
	Password   string        `toml:"password"`
	SessionTTL time.Duration `toml:"session_ttl"`
	APIKeys    []string      `toml:"api_keys"`
}

type AccountConfig struct {
	ProfileDir string `toml:"profile_dir"`
	Proxy      string `toml:"proxy"`
}

type PlatformConfig struct {
	Enabled       bool            `toml:"enabled"`
	ProfileDir    string          `toml:"profile_dir"`
	Accounts      []AccountConfig `toml:"accounts"`
	Proxy         string          `toml:"proxy"`
	MaxConcurrent int             `toml:"max_concurrency"`
	MinInterval   time.Duration   `toml:"min_interval"`
	RequestLimit  int64           `toml:"request_limit"`
}

// EffectiveAccounts 返回账号列表；未配置 accounts 时回退为单个平台账号（profile_dir + proxy）。
func (p PlatformConfig) EffectiveAccounts() []AccountConfig {
	if len(p.Accounts) == 0 {
		return []AccountConfig{{ProfileDir: p.ProfileDir, Proxy: p.Proxy}}
	}
	return p.Accounts
}

// Load 从单份 toml（默认项目根目录 config.toml）加载配置。
func Load(path string) (*Config, error) {
	if path == "" {
		path = "config.toml"
	}
	c := &Config{path: path}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return c, nil
}

func (c *Config) Reload() error {
	var nc Config
	if _, err := toml.DecodeFile(c.path, &nc); err != nil {
		return fmt.Errorf("parse %s: %w", c.path, err)
	}
	nc.path = c.path
	nc.applyDefaults()

	c.mu.Lock()
	c.Server = nc.Server
	c.Auth = nc.Auth
	c.Platforms = nc.Platforms
	c.mu.Unlock()
	return nil
}

func (c *Config) GetServer() ServerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server
}

func (c *Config) GetAuth() AuthConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Auth
}

func (c *Config) applyDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.QueueCapacity <= 0 {
		c.Server.QueueCapacity = 32
	}
	if c.Server.QueueWorkers <= 0 {
		c.Server.QueueWorkers = 1
	}
	if c.Server.ProfileDir == "" {
		c.Server.ProfileDir = "./profiles"
	}
	if c.Auth.SessionTTL <= 0 {
		c.Auth.SessionTTL = 24 * time.Hour
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

func (c *Config) GetPlatform(name string) (PlatformConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.Platforms[name]
	return p, ok
}

// PlatformSettings 网页端可编辑的平台字段（匿名结构便于与前端 JSON 一一对应）。
// 只更新这些字段；profile_dir / accounts 等由文件配置决定，不在网页上改。
func (p PlatformConfig) PlatformSettings() PlatformSettings {
	return PlatformSettings{
		Enabled:       p.Enabled,
		Proxy:         p.Proxy,
		MaxConcurrent: p.MaxConcurrent,
		MinIntervalS:  p.MinInterval.Seconds(),
		RequestLimit:  p.RequestLimit,
	}
}

type PlatformSettings struct {
	Enabled       bool    `json:"enabled"`
	Proxy         string  `json:"proxy"`
	MaxConcurrent int     `json:"max_concurrency"`
	MinIntervalS  float64 `json:"min_interval_s"`
	RequestLimit  int64   `json:"request_limit"`
	ProfileDir    string  `json:"profile_dir"`
}

// AppSettings 网页端设置快照，供 GET/POST /api/settings 使用。
type AppSettings struct {
	Server struct {
		Host          string `json:"host"`
		Port          int    `json:"port"`
		TLS           bool   `json:"tls"`
		QueueCapacity int    `json:"queue_capacity"`
		QueueWorkers  int    `json:"queue_workers"`
		Proxy         string `json:"proxy"`
		ProfileDir    string `json:"profile_dir"`
	} `json:"server"`
	Auth struct {
		Enabled     bool     `json:"enabled"`
		HasPassword bool     `json:"has_password"`
		APIKeys     []string `json:"api_keys"`
	} `json:"auth"`
	Platforms map[string]PlatformSettings `json:"platforms"`
}

func (c *Config) SettingsSnapshot() AppSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var s AppSettings
	s.Server.Host = c.Server.Host
	s.Server.Port = c.Server.Port
	s.Server.TLS = c.Server.UseTLS()
	s.Server.QueueCapacity = c.Server.QueueCapacity
	s.Server.QueueWorkers = c.Server.QueueWorkers
	s.Server.Proxy = c.Server.Proxy
	s.Server.ProfileDir = c.Server.ProfileDir
	s.Auth.Enabled = c.Auth.Enabled
	s.Auth.HasPassword = c.Auth.Password != ""
	s.Auth.APIKeys = append([]string(nil), c.Auth.APIKeys...)
	s.Platforms = map[string]PlatformSettings{}
	for name, p := range c.Platforms {
		ps := p.PlatformSettings()
		ps.ProfileDir = p.ProfileDir
		s.Platforms[name] = ps
	}
	return s
}

// UpdateSettings 将网页端提交的 [server] 队列与各平台设置写回 config.toml（保留文件里的
// host/port/tls、profile_dir、accounts、[auth] 等字段），写完后内存同步，返回生效的新快照。
func (c *Config) UpdateSettings(server *ServerEdit, platforms map[string]PlatformSettings) error {
	if server != nil {
		if server.QueueCapacity < 0 || server.QueueWorkers < 0 {
			return fmt.Errorf("queue 参数不能为负")
		}
	}
	for name, p := range platforms {
		if p.MaxConcurrent < 1 {
			return fmt.Errorf("%s: max_concurrency 必须 ≥ 1", name)
		}
		if p.MinIntervalS < 0 {
			return fmt.Errorf("%s: min_interval 不能为负", name)
		}
		if p.RequestLimit < 0 {
			return fmt.Errorf("%s: request_limit 不能为负", name)
		}
	}

	c.mu.Lock()
	next := Config{
		Server:    c.Server,
		Auth:      c.Auth,
		Platforms: map[string]PlatformConfig{},
		path:      c.path,
	}
	for name, p := range c.Platforms {
		next.Platforms[name] = p
	}
	if server != nil {
		next.Server.QueueCapacity = server.QueueCapacity
		next.Server.QueueWorkers = server.QueueWorkers
		next.Server.Proxy = server.Proxy
	}
	for name, p := range platforms {
		cur, ok := next.Platforms[name]
		if !ok {
			continue // 未知平台不新增
		}
		cur.Enabled = p.Enabled
		cur.Proxy = p.Proxy
		cur.MaxConcurrent = p.MaxConcurrent
		cur.MinInterval = time.Duration(p.MinIntervalS * float64(time.Second))
		cur.RequestLimit = p.RequestLimit
		next.Platforms[name] = cur
	}

	data, err := toml.Marshal(&next)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("序列化配置: %w", err)
	}
	if err := writeFileAtomic(c.path, data); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("写回配置: %w", err)
	}
	// 以文件为准重新加载，保证与磁盘一致
	c.Platforms = next.Platforms
	c.Server = next.Server
	c.Auth = next.Auth
	c.mu.Unlock()
	return nil
}

type ServerEdit struct {
	QueueCapacity int    `json:"queue_capacity"`
	QueueWorkers  int    `json:"queue_workers"`
	Proxy         string `json:"proxy"`
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Watch 轮询单份配置文件的修改时间，变化即热加载。
func (c *Config) Watch(ctx context.Context, interval time.Duration, onChange func()) {
	var mtime time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			fi, err := os.Stat(c.path)
			if err != nil {
				continue
			}
			latest := fi.ModTime()
			if mtime.IsZero() {
				mtime = latest
				continue
			}
			if latest.After(mtime) {
				mtime = latest
				if err := c.Reload(); err == nil && onChange != nil {
					onChange()
				}
			}
		}
	}
}
