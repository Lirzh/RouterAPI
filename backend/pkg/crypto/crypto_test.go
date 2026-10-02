package crypto

import (
	"strings"
	"testing"
)

// TestSetKeyAnyLength 验证任意长度密钥（含短于 32 字符）都能派生出可用的 AES-256 密钥，
// 修复此前短密钥被静默忽略、默认密钥仅 30 字节导致 aes.NewCipher 失败的问题。
func TestSetKeyAnyLength(t *testing.T) {
	secrets := []string{
		"change-me",                        // 9 字节
		"change-me-to-a-random-secret",     // 28 字节（config.yaml 默认）
		strings.Repeat("a", 32),            // 恰好 32
		strings.Repeat("long-secret-", 10), // 远超 32
	}
	for _, s := range secrets {
		SetKey(s)
		if len(encKey) != 32 {
			t.Fatalf("密钥长度 = %d, want 32 (secret=%q)", len(encKey), s)
		}
		plain := "sk-test-upstream-key-123456"
		ct, err := Encrypt(plain)
		if err != nil {
			t.Fatalf("Encrypt 失败 (secret=%q): %v", s, err)
		}
		got, err := Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt 失败 (secret=%q): %v", s, err)
		}
		if got != plain {
			t.Errorf("往返不一致 (secret=%q): got %q want %q", s, got, plain)
		}
	}
}

// TestSetKeyEmptyKeepsDefault 空密钥应保留兜底密钥而非清空。
func TestSetKeyEmptyKeepsDefault(t *testing.T) {
	SetKey("some-valid-secret")
	before := string(encKey)
	SetKey("")
	if string(encKey) != before {
		t.Errorf("空密钥不应改变 encKey")
	}
}

// TestEncryptEmpty 空明文直接返回空串，不报错。
func TestEncryptEmpty(t *testing.T) {
	ct, err := Encrypt("")
	if err != nil || ct != "" {
		t.Errorf("Encrypt(\"\") = (%q, %v), want (\"\", nil)", ct, err)
	}
}

// TestMaskString 脱敏不泄露完整密钥。
func TestMaskString(t *testing.T) {
	if m := MaskString("sk-1234567890abcd"); strings.Contains(m, "1234567890") {
		t.Errorf("脱敏泄露原文: %s", m)
	}
	if !strings.Contains(MaskString("sk-1234567890abcd"), "****") {
		t.Errorf("脱敏未包含 ****")
	}
}

// 短密钥曾让 s[:2] 越界 panic（任何管理端列表接口都会被打成 500）；
// 长度 8 时另一分支会把前 4 + 后 4 全部露出来，等于没脱敏。
func TestMaskStringShortInputs(t *testing.T) {
	for _, s := range []string{"", "k", "ab", "abcd1234", "abcdefgh", "0123456789"} {
		m := MaskString(s)
		if m == "" {
			t.Errorf("MaskString(%q) 返回空串", s)
		}
		if len(s) >= 4 && strings.Contains(m, s) {
			t.Errorf("MaskString(%q) 泄露了完整原文: %s", s, m)
		}
		if s != "" && len(s) <= 8 && m != "****" {
			t.Errorf("短密钥 %q 应整串遮蔽, got %q", s, m)
		}
	}
}
