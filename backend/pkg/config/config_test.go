package config

import (
	"strings"
	"testing"
)

// 配置文件不存在时（容器里只给环境变量的常见部署方式）也必须拿到派生默认。
//
// 这条断言守着的是钱：ReserveTTLMinutes 为 0 会让每笔预留一落地就过期，
// 结算撞上 "预留已失效" 分支，结果是所有请求都白跑、一分钱收不到，
// 而且症状是"计费不准"而不是"启动失败"，很难反查。
func TestLoadMissingFileStillAppliesDerivedDefaults(t *testing.T) {
	cfg, err := Load("./definitely-not-a-real-config-9a7c.yaml")
	if err != nil {
		t.Fatalf("配置文件缺失应回退默认而非报错: %v", err)
	}
	if cfg.Billing.ReserveTTLMinutes <= 0 {
		t.Errorf("ReserveTTLMinutes = %d, 预留会立刻过期", cfg.Billing.ReserveTTLMinutes)
	}
	if cfg.Billing.ReserveDefaultMaxTokens <= 0 {
		t.Errorf("ReserveDefaultMaxTokens = %d, 未传 max_tokens 时预估塌成 0", cfg.Billing.ReserveDefaultMaxTokens)
	}
	if cfg.Billing.ReserveSafetyPercent <= 0 {
		t.Errorf("ReserveSafetyPercent = %d", cfg.Billing.ReserveSafetyPercent)
	}
	if cfg.Retry.Times <= 0 {
		t.Errorf("Retry.Times = %d", cfg.Retry.Times)
	}
	if cfg.Server.MaxRequestBodyBytes != DefaultMaxRequestBodyBytes {
		t.Errorf("MaxRequestBodyBytes = %d, want %d", cfg.Server.MaxRequestBodyBytes, int64(DefaultMaxRequestBodyBytes))
	}
}

// 显式写了 0/负数时按"没配"处理，不能真的把请求体上限设成 0——
// 那会让 MaxBytesReader 把所有 body 判成超限。
func TestZeroBodyLimitFallsBackToDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Server.MaxRequestBodyBytes != DefaultMaxRequestBodyBytes {
		t.Errorf("MaxRequestBodyBytes = %d, want 默认值", cfg.Server.MaxRequestBodyBytes)
	}

	cfg.Server.MaxRequestBodyBytes = -1
	cfg.applyDefaults()
	if cfg.Server.MaxRequestBodyBytes != DefaultMaxRequestBodyBytes {
		t.Errorf("负值未被回退: %d", cfg.Server.MaxRequestBodyBytes)
	}

	cfg.Server.MaxRequestBodyBytes = 4096
	cfg.applyDefaults()
	if cfg.Server.MaxRequestBodyBytes != 4096 {
		t.Errorf("显式配置被覆盖了: %d", cfg.Server.MaxRequestBodyBytes)
	}
}

// 直通 lane 默认开启，只有显式关闭才退回 IR 往返。
func TestRawLaneEnabledByDefault(t *testing.T) {
	if !(GatewayConfig{}).RawLaneEnabled() {
		t.Error("零值配置下直通 lane 应当是开着的")
	}
	if (GatewayConfig{RawLaneDisabled: true}).RawLaneEnabled() {
		t.Error("raw_lane_disabled: true 未生效")
	}
}

// 鉴权密钥体检：哪些必须拦、哪些只能提醒。
func TestCheckAuthKeys(t *testing.T) {
	strong := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name        string
		srv         ServerConfig
		wantFatal   int
		wantWarnMin int
	}{
		{
			name:      "空 jwt_secret",
			srv:       ServerConfig{EncryptKey: strong},
			wantFatal: 1,
		},
		{
			name:      "出厂占位 jwt_secret",
			srv:       ServerConfig{JWTSecret: "change-me-to-a-random-secret", EncryptKey: strong},
			wantFatal: 1,
		},
		{
			name:      "过短 jwt_secret",
			srv:       ServerConfig{JWTSecret: "short", EncryptKey: strong},
			wantFatal: 1,
		},
		{
			name:        "两把钥匙合成一把（升级兼容，只告警）",
			srv:         ServerConfig{JWTSecret: strong, EncryptKey: strong},
			wantWarnMin: 1,
		},
		{
			name:        "未配 encrypt_key（升级兼容，只告警）",
			srv:         ServerConfig{JWTSecret: strong},
			wantWarnMin: 1,
		},
		{
			name: "两把独立且够长的密钥",
			srv:  ServerConfig{JWTSecret: strong, EncryptKey: "fedcba9876543210fedcba9876543210"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Server: tc.srv}
			chk := cfg.CheckAuthKeys()
			if len(chk.Fatal) != tc.wantFatal {
				t.Errorf("Fatal = %d (%v), want %d", len(chk.Fatal), chk.Fatal, tc.wantFatal)
			}
			if len(chk.Warn) < tc.wantWarnMin {
				t.Errorf("Warn = %v, 期望至少 %d 条", chk.Warn, tc.wantWarnMin)
			}
			// 加密密钥的问题一律不该阻断启动：否则老库升级后直接起不来，
			// 运维只会把配置改回去，安全收益变成 0。
			for _, w := range chk.Warn {
				if strings.Contains(w, "encrypt_key") && strings.Contains(w, "拒绝") {
					t.Errorf("encrypt_key 的问题不该出现在 Fatal 语义里: %s", w)
				}
			}
		})
	}
}

// 生效密钥：配了用配的，没配回退 jwt_secret（改造前的行为）。
func TestEffectiveEncryptKey(t *testing.T) {
	both := &Config{Server: ServerConfig{JWTSecret: "jwt-value", EncryptKey: "enc-value"}}
	if got := both.EffectiveEncryptKey(); got != "enc-value" {
		t.Errorf("EffectiveEncryptKey = %q, want enc-value", got)
	}
	fallback := &Config{Server: ServerConfig{JWTSecret: "jwt-value"}}
	if got := fallback.EffectiveEncryptKey(); got != "jwt-value" {
		t.Errorf("未配置时应回退 jwt_secret, got %q", got)
	}
}

// 登录限流的默认值必须非零：忘了配不该等于"登录不限流"。
func TestLoginRateLimitDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Security.LoginPerIPPerMin <= 0 || cfg.Security.LoginPerUserPerMin <= 0 {
		t.Errorf("登录限流默认值丢了: ip=%d user=%d", cfg.Security.LoginPerIPPerMin, cfg.Security.LoginPerUserPerMin)
	}
	cfg.Security.LoginPerIPPerMin = 7
	cfg.applyDefaults()
	if cfg.Security.LoginPerIPPerMin != 7 {
		t.Errorf("显式配置被覆盖: %d", cfg.Security.LoginPerIPPerMin)
	}
}
