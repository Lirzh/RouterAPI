package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server           ServerConfig   `yaml:"server"`
	Database         DBConfig       `yaml:"database"`
	FrontendDist     string         `yaml:"frontend_dist"`
	Admin            AdminConfig    `yaml:"admin"`
	LogRetentionDays int            `yaml:"log_retention_days"`
	Retry            RetryConfig    `yaml:"retry"`
	Security         SecurityConfig `yaml:"security"` // 登录限流等认证防护
	Health           HealthConfig   `yaml:"health"`
	Billing          BillingConfig    `yaml:"billing"`
	Gateway          GatewayConfig    `yaml:"gateway"` // 转发行为（同格式直通等）
	Leaderboard      LeaderboardConfig `yaml:"leaderboard"` // 全站用户排行榜开关与隐私策略
	TrustedProxies   []string         `yaml:"trusted_proxies"`
}

// LeaderboardConfig 全站用户排行榜（公开接口 /api/leaderboard）的行为开关。
// 语义刻意用"关闭某能力"的负向字段：省略整个 leaderboard 块时零值 = 公开、不匿名，
// 与引入本配置前的行为逐字一致，老部署不会因为"没写这段"而被改动。
type LeaderboardConfig struct {
	// PublicDisabled 关闭公开排行榜：接口仍可达，但返回 enabled=false 且不吐数据，
	// 起始页据此显示"未开放"。要做"仅登录可见"请改路由挂载，而非仅置此位。
	PublicDisabled bool `yaml:"public_disabled"`
	// Anonymize 服务端匿名化用户名（只保留首字符 + 掩码），在数据离开进程前完成，
	// 而非前端遮罩——否则真实用户名仍在公开响应体里。
	Anonymize bool `yaml:"anonymize"`
	// DefaultLimit 未带 limit 参数时返回的条数（受 MaxLimit 上限约束）。
	DefaultLimit int `yaml:"default_limit"`
	// MaxLimit 单次返回条数的硬上限，防止公开接口被拿去拖全表。
	MaxLimit int `yaml:"max_limit"`
}

// PublicEnabled 报告公开排行榜是否开启。
func (l LeaderboardConfig) PublicEnabled() bool { return !l.PublicDisabled }

// GatewayConfig 转发行为开关。
type GatewayConfig struct {
	// RawLaneDisabled 关闭"同格式直通"：入站协议与上游协议一致时也强制走 IR 往返。
	// 缺省即启用。直通路径不改字节、只窥探 usage，是保真度更高的一条路；
	// 留这个开关是为了在出现"客户端看到的字节不对劲"这类问题时能一行配置回退定位。
	RawLaneDisabled bool `yaml:"raw_lane_disabled"`
}

// RawLaneEnabled 报告是否允许同格式直通。
func (g GatewayConfig) RawLaneEnabled() bool { return !g.RawLaneDisabled }

// BillingConfig 积分钱包开关。启用后：请求前预留额度、请求后按 Model 定价结算并记流水。
// 关闭时不计费、不拦截（仅记录 token 用量）。默认为 true（见 config.yaml）。
type BillingConfig struct {
	Enabled bool `yaml:"enabled"`

	// ReserveDisabled 显式关闭请求级额度预留，退回旧的"余额>0 即放行、请求后才扣减"语义。
	// 缺省即启用。留这个开关只是给资金逻辑留一行配置式的回退路径，不代表旧语义更安全——
	// 旧语义下余额 1 分也能打出巨额请求，差额在 clamp 扣减时直接蒸发。
	ReserveDisabled bool `yaml:"reserve_disabled"`

	// ReserveDefaultMaxTokens 客户端未传 max_tokens 时，预估输出用的兜底上限。
	// 预估只决定"要不要放行"，不影响实际收费额（收费仍按上游返回的真实用量）。
	ReserveDefaultMaxTokens int `yaml:"reserve_default_max_tokens"`

	// ReserveSafetyPercent 输入 token 估算的上浮百分比。字符估算偏乐观（尤其代码/JSON 内容），
	// 低估会导致实际超出预留、按预留封顶而少收，所以宁可高估。
	ReserveSafetyPercent int `yaml:"reserve_safety_percent"`

	// ReserveTTLMinutes 预留自动回收时限，必须显著大于上游客户端超时，
	// 否则一次慢请求会在结算之前被清扫器当成泄漏回收掉。
	ReserveTTLMinutes int `yaml:"reserve_ttl_minutes"`
}

// ReserveEnabled 报告是否启用请求级预留（仅在钱包启用时才有意义）。
func (b BillingConfig) ReserveEnabled() bool { return b.Enabled && !b.ReserveDisabled }

type ServerConfig struct {
	Port           int    `yaml:"port"`
	Mode           string `yaml:"mode"`
	JWTSecret      string `yaml:"jwt_secret"`
	JWTExpireHours int    `yaml:"jwt_expire_hours"`

	// MaxRequestBodyBytes 单次入站请求体的上限（0 及以下视为用默认值）。
	// 默认 20MiB，对齐 OpenAI 自家 chat/completions 的请求体上限：
	// 多模态请求要装得下几张 base64 图片，所以不能按"JSON 很小"来设；
	// 但也不能不限——那等于让一个请求打爆内存。
	MaxRequestBodyBytes int64 `yaml:"max_request_body_bytes"`

	// EncryptKey 供应商凭证 AES-GCM 加密所用的密钥材料，必须与 jwt_secret 分开配。
	// 一个值两用意味着"能伪造管理令牌"和"能解出所有上游密钥"是同一次泄露。
	// 留空则回退用 jwt_secret（兼容改造前的部署）并在启动时告警；
	// 配上它之后存量密文会自动重封，见 database.ReencryptVendorSecrets。
	EncryptKey string `yaml:"encrypt_key"`
}

// EffectiveEncryptKey 返回生效的凭证加密密钥；未单独配置时回退 jwt_secret。
func (c *Config) EffectiveEncryptKey() string {
	if c.Server.EncryptKey != "" {
		return c.Server.EncryptKey
	}
	return c.Server.JWTSecret
}

// SecurityConfig 认证侧防护参数。
type SecurityConfig struct {
	// LoginPerIPPerMin 单个来源 IP 每分钟允许的登录尝试次数。
	// 拦的是"一个来源扫一批账号"的凭据填充。
	LoginPerIPPerMin int `yaml:"login_per_ip_per_min"`
	// LoginPerUserPerMin 单个用户名每分钟允许的登录尝试次数。
	// 拦的是"多个来源轮攻一个账号"的分布式爆破。
	//
	// 与钱包/限流那几处不同，这里不是 fail-open：0 以下会退回默认值，
	// 因为"忘记配"不该等于"登录不限流"。要真的关掉请填一个足够大的数。
	LoginPerUserPerMin int `yaml:"login_per_user_per_min"`
}

// DefaultLoginPerIPPerMin / DefaultLoginPerUserPerMin 登录限流的兜底值。
const (
	DefaultLoginPerIPPerMin   = 30
	DefaultLoginPerUserPerMin = 10
)

// DefaultMaxRequestBodyBytes 未配置时的入站请求体上限（20MiB）。
const DefaultMaxRequestBodyBytes int64 = 20 << 20

// DefaultLeaderboardLimit / MaxLimit 全站排行榜的默认与硬上限条数。
const (
	DefaultLeaderboardLimit    = 20
	DefaultLeaderboardMaxLimit = 50
)

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
		// 配置文件缺失时同样要过一遍 applyDefaults：defaultConfig 只给了显式字段，
		// 少了派生默认（预留 TTL、预估上限、请求体上限），会让预留以 0 TTL 立刻过期。
		cfg = defaultConfig()
		cfg.applyDefaults()
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
	if c.Billing.ReserveDefaultMaxTokens <= 0 {
		c.Billing.ReserveDefaultMaxTokens = 4096
	}
	if c.Billing.ReserveSafetyPercent <= 0 {
		c.Billing.ReserveSafetyPercent = 25
	}
	if c.Billing.ReserveTTLMinutes <= 0 {
		c.Billing.ReserveTTLMinutes = 30
	}
	if c.Server.MaxRequestBodyBytes <= 0 {
		c.Server.MaxRequestBodyBytes = DefaultMaxRequestBodyBytes
	}
	if c.Security.LoginPerIPPerMin <= 0 {
		c.Security.LoginPerIPPerMin = DefaultLoginPerIPPerMin
	}
	if c.Security.LoginPerUserPerMin <= 0 {
		c.Security.LoginPerUserPerMin = DefaultLoginPerUserPerMin
	}
	if c.Leaderboard.DefaultLimit <= 0 {
		c.Leaderboard.DefaultLimit = DefaultLeaderboardLimit
	}
	if c.Leaderboard.MaxLimit <= 0 {
		c.Leaderboard.MaxLimit = DefaultLeaderboardMaxLimit
	}
	// 配置给反了（上限小于默认值）时以默认值为准，避免 limit 恒被压低。
	if c.Leaderboard.MaxLimit < c.Leaderboard.DefaultLimit {
		c.Leaderboard.MaxLimit = c.Leaderboard.DefaultLimit
	}
}

// shippedPlaceholderSecrets 是随仓库一起发布的占位密钥。它们出现在 README/config.yaml 里，
// 等同于公开信息，用它签的令牌谁都能伪造。
var shippedPlaceholderSecrets = map[string]bool{
	"change-me":                                  true,
	"change-me-to-a-random-secret":               true,
	"routerapi-default-encryption-key-secret":    true,
	"routerapi-default-encryption-key-change-me": true,
	"secret":   true,
	"changeme": true,
}

const minSecretLen = 32

// AuthKeyCheck 鉴权密钥的体检结果。
type AuthKeyCheck struct {
	// Fatal 是 release 模式下应当拒绝启动的问题：用出厂占位密钥对外运营，
	// 等于把管理面板的钥匙贴在门上。
	Fatal []string
	// Warn 是建议修但不阻断启动的问题，主要是升级兼容类的。
	Warn []string
}

// CheckAuthKeys 体检 JWT 与凭证加密两把密钥。
//
// 之所以不是一次性把所有配置都校验：这两个值一旦用错，后果是"凭证可被伪造/可被解出"，
// 而症状要到被攻击时才看得见，启动时喊一声是唯一的便宜时机。
func (c *Config) CheckAuthKeys() AuthKeyCheck {
	var chk AuthKeyCheck
	secret := c.Server.JWTSecret
	switch {
	case secret == "":
		chk.Fatal = append(chk.Fatal, "server.jwt_secret 未配置：管理令牌无法安全签发")
	case shippedPlaceholderSecrets[secret]:
		chk.Fatal = append(chk.Fatal, "server.jwt_secret 仍是随仓库发布的占位值，任何人都能伪造管理令牌；请换成随机值（如 openssl rand -hex 32）")
	case len(secret) < minSecretLen:
		chk.Fatal = append(chk.Fatal, fmt.Sprintf("server.jwt_secret 只有 %d 字符，短于 %d 的令牌可被离线穷举；请换成随机值", len(secret), minSecretLen))
	}

	switch {
	case c.Server.EncryptKey == "":
		chk.Warn = append(chk.Warn, "未配置 server.encrypt_key，供应商凭证正用 jwt_secret 派生的密钥加密：一次泄露同时失掉两者，建议单独配一把（配置后存量密文会自动重封）")
	case c.Server.EncryptKey == secret:
		chk.Warn = append(chk.Warn, "server.encrypt_key 与 server.jwt_secret 相同，等于还是一把钥匙，建议改成另一个随机值")
	case len(c.Server.EncryptKey) < minSecretLen:
		chk.Warn = append(chk.Warn, "server.encrypt_key 短于 32 字符，建议换成随机值")
	}
	if c.Server.Mode == "release" && c.Admin.Password == "admin123" {
		chk.Warn = append(chk.Warn, "admin.password 仍是默认口令，对外运营前请立即修改")
	}
	return chk
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
	if v := os.Getenv("ENCRYPT_KEY"); v != "" {
		c.Server.EncryptKey = v
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
		Leaderboard:      LeaderboardConfig{DefaultLimit: DefaultLeaderboardLimit, MaxLimit: DefaultLeaderboardMaxLimit},
	}
}
