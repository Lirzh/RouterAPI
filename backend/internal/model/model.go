package model

import (
	"time"

	"gorm.io/gorm"
)

// ============ 角色 ============

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// ============ 协议预设（替代原 Provider 表） ============

// 上游协议类型
const (
	ProtocolOpenAI       = "openai"        // OpenAI 官方
	ProtocolAnthropic    = "anthropic"     // Anthropic Claude
	ProtocolGemini       = "gemini"        // Google Gemini
	ProtocolOpenAICompat = "openai-compat" // 任意 OpenAI 兼容端点
	ProtocolCustom       = "custom"        // 自定义鉴权
)

// 鉴权方式
const (
	AuthTypeBearer  = "bearer"    // Authorization: <prefix><key>
	AuthTypeXAPIKey = "x-api-key" // 自定义 header 直接放 key
	AuthTypeQuery   = "query"     // URL query ?key=
)

// ProtocolPreset 协议预设：定义各协议的默认地址与鉴权方式。
// 用户在“供应商”页选择协议后，由预设自动填充默认值，仍可覆盖。
type ProtocolPreset struct {
	Protocol       string `json:"protocol"`
	DisplayName    string `json:"display_name"`
	DefaultBaseURL string `json:"default_base_url"`
	AuthType       string `json:"auth_type"`
	AuthHeader     string `json:"auth_header"`
	AuthPrefix     string `json:"auth_prefix"`
}

var protocolPresets = map[string]ProtocolPreset{
	ProtocolOpenAI: {
		Protocol: ProtocolOpenAI, DisplayName: "OpenAI",
		DefaultBaseURL: "https://api.openai.com",
		AuthType:       AuthTypeBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
	},
	ProtocolAnthropic: {
		Protocol: ProtocolAnthropic, DisplayName: "Anthropic Claude",
		DefaultBaseURL: "https://api.anthropic.com",
		AuthType:       AuthTypeXAPIKey, AuthHeader: "x-api-key", AuthPrefix: "",
	},
	ProtocolGemini: {
		Protocol: ProtocolGemini, DisplayName: "Google Gemini",
		DefaultBaseURL: "https://generativelanguage.googleapis.com",
		AuthType:       AuthTypeQuery, AuthHeader: "", AuthPrefix: "",
	},
	ProtocolOpenAICompat: {
		Protocol: ProtocolOpenAICompat, DisplayName: "OpenAI 兼容",
		DefaultBaseURL: "",
		AuthType:       AuthTypeBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
	},
	ProtocolCustom: {
		Protocol: ProtocolCustom, DisplayName: "自定义",
		DefaultBaseURL: "",
		AuthType:       AuthTypeBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
	},
}

// protocolOrder 预设展示顺序
var protocolOrder = []string{
	ProtocolOpenAI, ProtocolAnthropic, ProtocolGemini, ProtocolOpenAICompat, ProtocolCustom,
}

// GetProtocolPreset 返回协议预设；未知协议回退到 OpenAI 兼容。
func GetProtocolPreset(protocol string) ProtocolPreset {
	if p, ok := protocolPresets[protocol]; ok {
		return p
	}
	return protocolPresets[ProtocolOpenAICompat]
}

// AllProtocolPresets 返回全部协议预设（有序），供前端下拉使用。
func AllProtocolPresets() []ProtocolPreset {
	out := make([]ProtocolPreset, 0, len(protocolOrder))
	for _, k := range protocolOrder {
		out = append(out, protocolPresets[k])
	}
	return out
}

// ============ 日志类型（显式常量，避免 iota 顺序变动破坏历史数据） ============

const (
	LogTypeConsume = 2 // 消费日志
	LogTypeManage  = 3 // 管理操作
	LogTypeSystem  = 4 // 系统
	LogTypeError   = 5 // 错误日志
)

// ============ 积分流水类型 ============

const (
	CreditTypeTopup   = "topup"   // 充值
	CreditTypeConsume = "consume" // 消费
	CreditTypeRefund  = "refund"  // 退款
	CreditTypeAdjust  = "adjust"  // 手动调整
	CreditTypeRedeem  = "redeem"  // 兑换码兑换
)

// User 系统用户（团队成员）
type User struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username" gorm:"uniqueIndex;size:64;not null"`
	Password string `json:"-" gorm:"size:128;not null"`
	Email    string `json:"email" gorm:"size:128"`
	Role     string `json:"role" gorm:"size:32;default:admin;index"` // admin | viewer
	Status   int    `json:"status" gorm:"default:1"`                 // 1=启用 0=禁用
	Credits  int64  `json:"credits" gorm:"default:0"`                // 积分钱包余额
	// 在途请求已预留、尚未结算的积分。可用余额 = Credits - ReservedCredits。
	// 不参与扣减语义（不是"已花掉"），只是把尚未结算的额度从可用额度里先占住。
	ReservedCredits int64          `json:"reserved_credits" gorm:"default:0"`
	TokenVersion    int            `json:"token_version" gorm:"default:0"` // JWT 撤销版本号
	LastLoginAt     *time.Time     `json:"last_login_at"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

// Vendor 供应商 = 一个上游可用实例（协议 + 凭证 + 负载均衡设置）。
// 由原 Channel 升级而来，并把原 Provider（协议字典）折叠为 Protocol 字段 + 可选自定义鉴权覆盖。
type Vendor struct {
	ID           uint   `json:"id" gorm:"primaryKey"`
	Name         string `json:"name" gorm:"size:128;not null"`
	Protocol     string `json:"protocol" gorm:"size:32;default:openai"` // openai|anthropic|gemini|openai-compat|custom
	BaseURL      string `json:"base_url" gorm:"size:512"`
	APIKey       string `json:"api_key,omitempty" gorm:"size:1024"` // AES-GCM 加密存储，读取时返回脱敏
	APIKeyMasked string `json:"api_key_masked" gorm:"-"`
	// 自定义鉴权覆盖（Protocol=custom 或需覆盖预设时使用；为空则取协议预设）
	AuthType   string `json:"auth_type" gorm:"size:32"`
	AuthHeader string `json:"auth_header" gorm:"size:128"`
	AuthPrefix string `json:"auth_prefix" gorm:"size:32"`

	Weight   int `json:"weight" gorm:"default:1"`       // 负载均衡权重
	Priority int `json:"priority" gorm:"default:0"`     // 优先级，越高越先选
	Status   int `json:"status" gorm:"default:1;index"` // 1=启用 0=手动禁用（不自动恢复）

	MaxRetry int `json:"max_retry" gorm:"default:3"`
	MaxRPM   int `json:"max_rpm" gorm:"default:0"` // 每分钟最大请求数，0=不限

	ConsecutiveErrors int        `json:"consecutive_errors" gorm:"default:0"` // 连续错误计数
	CooldownUntil     *time.Time `json:"cooldown_until" gorm:"index"`         // 临时熔断到期（到点自动恢复）

	LastError  string     `json:"last_error" gorm:"size:1024"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// EffectiveAuth 返回该供应商生效的鉴权配置（覆盖优先，否则取协议预设）。
func (v *Vendor) EffectiveAuth() (authType, authHeader, authPrefix string) {
	preset := GetProtocolPreset(v.Protocol)
	authType = v.AuthType
	if authType == "" {
		authType = preset.AuthType
	}
	authHeader = v.AuthHeader
	if authHeader == "" {
		authHeader = preset.AuthHeader
	}
	authPrefix = v.AuthPrefix
	if authPrefix == "" {
		authPrefix = preset.AuthPrefix
	}
	return
}

// EffectiveBaseURL 返回生效的 base_url（为空取协议预设默认地址）。
func (v *Vendor) EffectiveBaseURL() string {
	if v.BaseURL != "" {
		return v.BaseURL
	}
	return GetProtocolPreset(v.Protocol).DefaultBaseURL
}

// IsCoolingDown 是否处于熔断冷却期。
func (v *Vendor) IsCoolingDown(now time.Time) bool {
	return v.CooldownUntil != nil && v.CooldownUntil.After(now)
}

// Model 对外暴露的虚拟模型目录条目（客户端请求时填写的名字）。
type Model struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	Name        string `json:"name" gorm:"uniqueIndex;size:128;not null"` // 虚拟模型名
	DisplayName string `json:"display_name" gorm:"size:128"`
	Description string `json:"description" gorm:"type:text"`
	Enabled     bool   `json:"enabled" gorm:"default:true;index"`

	// 积分定价：每 1M token 消耗的积分（不挂钩现实货币）
	PromptCreditsPer1M     int64 `json:"prompt_credits_per_1m" gorm:"default:0"`
	CompletionCreditsPer1M int64 `json:"completion_credits_per_1m" gorm:"default:0"`
	// 缓存读写的独立单价。为 0 表示沿用 Prompt 单价，即不给缓存折扣、也不对缓存写入加价。
	// 选 0 作哨兵而非默认倍率，是为了让"引入缓存定价"这次变更前后收费金额完全一致：
	// 开启折扣必须是管理员的显式动作，不能靠升级悄悄改动已在跑的账单。
	CacheReadCreditsPer1M  int64 `json:"cache_read_credits_per_1m" gorm:"default:0"`
	CacheWriteCreditsPer1M int64 `json:"cache_write_credits_per_1m" gorm:"default:0"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ModelBinding 模型与供应商的绑定（多对多）：一个虚拟模型可绑定多个供应商实现容灾。
type ModelBinding struct {
	ID            uint      `json:"id" gorm:"primaryKey"`
	ModelID       uint      `json:"model_id" gorm:"uniqueIndex:idx_bind,priority:1;not null"`
	Model         Model     `json:"model" gorm:"foreignKey:ModelID"`
	VendorID      uint      `json:"vendor_id" gorm:"uniqueIndex:idx_bind,priority:2;not null"`
	Vendor        Vendor    `json:"vendor" gorm:"foreignKey:VendorID"`
	UpstreamModel string    `json:"upstream_model" gorm:"uniqueIndex:idx_bind,priority:3;size:128;not null"` // 上游真实模型名
	Params        string    `json:"params" gorm:"type:text"`                                                 // 默认参数 JSON（生效）
	Weight        int       `json:"weight" gorm:"default:1"`
	Priority      int       `json:"priority" gorm:"default:0"`
	Enabled       bool      `json:"enabled" gorm:"default:true"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// APIKey 对外调用密钥（团队成员/应用使用）。
type APIKey struct {
	ID   uint   `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"size:128;not null"`
	// KeyHash 是调用方密钥的 SHA-256（hex），也是唯一的查库条件。库里不存明文：
	// 只读库、备份文件、慢查询日志里任何一处泄露都不该等于交出可用的 bearer 凭证。
	KeyHash string `json:"-" gorm:"uniqueIndex;size:64"`
	// KeyMasked 是持久化的展示形态（ra-abcd****wxyz）。必须落库，
	// 否则脱敏只能从明文现算，而明文已经不存在了。
	KeyMasked string `json:"key_masked" gorm:"size:128"`
	// Key 只在创建成功后随响应出现一次，永不落库（gorm:"-"）。
	Key         string `json:"key,omitempty" gorm:"-"`
	OwnerUserID uint   `json:"owner_user_id" gorm:"index"` // 归属用户（消费其积分）

	AllowedVendors string `json:"allowed_vendors" gorm:"type:text"` // JSON 数组：授予整个供应商（vendor id）
	AllowedModels  string `json:"allowed_models" gorm:"type:text"`  // JSON 数组：授予指定模型（模型名）
	AllowIps       string `json:"allow_ips" gorm:"type:text"`       // JSON 数组：IP/CIDR 白名单，空=不限
	CreditsCap     int64  `json:"credits_cap" gorm:"default:0"`     // 该 Key 累计花费上限（积分），0=不限
	RateLimitRPM   int    `json:"rate_limit_rpm" gorm:"default:0"`  // 该 Key 每分钟最大请求数，0=不限

	Status     int        `json:"status" gorm:"default:1"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// RequestLog 调用日志。
type RequestLog struct {
	ID                uint   `json:"id" gorm:"primaryKey"`
	Type              int    `json:"type" gorm:"index;default:2"` // 2=消费 5=错误
	RequestID         string `json:"request_id" gorm:"index;size:64"`
	UpstreamRequestID string `json:"upstream_request_id" gorm:"index;size:128"` // 上游请求 ID，便于关联排障
	APIKeyID          uint   `json:"api_key_id" gorm:"index"`
	APIKeyName        string `json:"api_key_name" gorm:"size:128"`
	UserID            uint   `json:"user_id" gorm:"index"` // 归属用户
	VendorID          uint   `json:"vendor_id" gorm:"index"`
	VendorName        string `json:"vendor_name" gorm:"size:128"`
	ProtocolName      string `json:"protocol_name" gorm:"size:64"`    // 供应商协议
	InboundProtocol   string `json:"inbound_protocol" gorm:"size:32"` // 入站协议
	Model             string `json:"model" gorm:"size:128;index"`     // 虚拟模型
	UpstreamModel     string `json:"upstream_model" gorm:"size:128"`  // 上游真实模型
	TokenPrompt       int    `json:"token_prompt"`
	TokenCompletion   int    `json:"token_completion"`
	TokenTotal        int    `json:"token_total"`
	// 用量分解：转售定位下"这一单为什么收这么多"必须可审计。
	// 均为上面 prompt/completion 的子集，不额外计入 TokenTotal。
	TokenCached     int       `json:"token_cached" gorm:"default:0"`
	TokenCacheWrite int       `json:"token_cache_write" gorm:"default:0"`
	TokenReasoning  int       `json:"token_reasoning" gorm:"default:0"`
	CreditsCost     int64     `json:"credits_cost" gorm:"default:0"` // 消耗积分
	LatencyMs       int64     `json:"latency_ms"`
	StatusCode      int       `json:"status_code"`
	Success         bool      `json:"success"`
	IsStream        bool      `json:"is_stream"`
	ErrorMessage    string    `json:"error_message" gorm:"type:text"`
	ClientIP        string    `json:"client_ip" gorm:"size:64"`
	UserAgent       string    `json:"user_agent" gorm:"size:512"`
	CreatedAt       time.Time `json:"created_at" gorm:"index"`
}
