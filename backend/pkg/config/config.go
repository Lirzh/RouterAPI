package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server           ServerConfig    `yaml:"server"`
	Database         DBConfig        `yaml:"database"`
	FrontendDist     string          `yaml:"frontend_dist"`
	Admin            AdminConfig     `yaml:"admin"`
	LogRetentionDays int             `yaml:"log_retention_days"`
	Retry            RetryConfig     `yaml:"retry"`
	Health           HealthConfig    `yaml:"health"`
	Billing          BillingConfig   `yaml:"billing"`
	TrustedProxies   []string        `yaml:"trusted_proxies"`
}

// BillingConfig 积分钱包开关。启用后：请求前做余额门槛校验、请求后按 Model 定价原子扣减积分并记流水。
// 关闭时不计费、不拦截（仅记录 token 用量）。默认为 true（见 config.yaml）。
type BillingConfig struct {
	Enabled bool `yaml:"enabled"`
}

type ServerConfig struct {
	Port           int    `yaml:"port"`
	Mode           string `yaml:"mode"`
	JWTSecret      string `yaml:"jwt_secret"`
	JWTExpireHours int    `yaml:"jwt_expire_hours"`
}

// DBConfig 支持 SQLite 与 PostgreSQL 双驱动，由 Driver 决定。
type DBConfig struct {
	Driver string `yaml:"driver"` // sqlite | postgres
	Path   string `yaml:"path"`   // sqlite 数据文件路径

	// PostgreSQL 连接参数（Driver=postgres 时使用）
	DSN      string `yaml:"dsn"` // 完整 DSN，优先于下列离散字段
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// BuildPostgresDSN 组装 PostgreSQL DSN；若已显式配置 DSN 则直接返回。
func (d DBConfig) BuildPostgresDSN() string {
	if d.DSN != "" {
		return d.DSN
	}
	host := d.Host
	if host == "" {
		host = "localhost"
	}
	port := d.Port
	if port == 0 {
		port = 5432
	}
	ssl := d.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		host, port, d.User, d.Password, d.DBName, ssl)
}

type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// RetryConfig 全局重试默认值（供应商未单独配置 MaxRetry 时使用）。
type RetryConfig struct {
	Times int `yaml:"times"` // 跨供应商重试次数，默认 2
}

// HealthConfig 后台健康探测（熔断自动恢复）。
type HealthConfig struct {
	Enabled         bool `yaml:"enabled"`
	IntervalMinutes int  `yaml:"interval_minutes"` // 探测间隔，默认 5 分钟
}

func Load(path string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		// 回退到默认值并使用环境变量覆盖
		cfg = defaultConfig()
		cfg.applyEnv()
		return cfg, nil
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	return cfg, nil
}

// applyDefaults 为零值字段填充合理默认。
func (c *Config) applyDefaults() {
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	if c.Retry.Times <= 0 {
		c.Retry.Times = 2
	}
	if c.Health.IntervalMinutes <= 0 {
		c.Health.IntervalMinutes = 5
	}
	if c.LogRetentionDays <= 0 {
		c.LogRetentionDays = 30
	}
}

// BillingEnabled 返回积分钱包是否启用。
func (c *Config) BillingEnabled() bool { return c.Billing.Enabled }

func (c *Config) applyEnv() {
	if v := os.Getenv("PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.Server.Port = port
		}
	}
	if v := os.Getenv("GIN_MODE"); v != "" {
		c.Server.Mode = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.Server.JWTSecret = v
	}
	if v := os.Getenv("DB_DRIVER"); v != "" {
		c.Database.Driver = v
	}
	if v := os.Getenv("DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("DB_DSN"); v != "" {
		c.Database.DSN = v
	}
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:           3000,
			Mode:           "debug",
			JWTSecret:      "change-me",
			JWTExpireHours: 48,
		},
		Database: DBConfig{
			Driver: "sqlite",
			Path:   "./data/routerapi.db",
		},
		FrontendDist:     "../frontend/dist",
		Admin:            AdminConfig{Username: "admin", Password: "admin123"},
		LogRetentionDays: 30,
		Retry:            RetryConfig{Times: 2},
		Health:           HealthConfig{Enabled: true, IntervalMinutes: 5},
		Billing:          BillingConfig{Enabled: true},
	}
}
