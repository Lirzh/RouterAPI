package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// defaultKeySeed 未显式设置加密密钥时的兜底种子（生产环境务必通过配置覆盖）。
const defaultKeySeed = "routerapi-default-encryption-key-change-me"

// encKey 用于 AES-GCM 加密供应商凭证；恒为 32 字节（AES-256）。
var encKey = deriveKey(defaultKeySeed)

// deriveKey 用 SHA-256 将任意长度的密钥材料派生为固定的 32 字节 AES-256 密钥。
func deriveKey(seed string) []byte {
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

// SetKey 设置加密密钥（任意长度均可，内部经 SHA-256 派生为 32 字节）。
// 空字符串忽略，保留兜底密钥。
func SetKey(key string) {
	if key == "" {
		return
	}
	encKey = deriveKey(key)
}

// seal/open 用显式密钥做 AES-256-GCM。Encrypt/Decrypt 走当前生效密钥，
// 迁移路径（换密钥后要重封存量密文）用 EncryptWith/DecryptWith，不去动全局状态。
func seal(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)), nil
}

func open(key []byte, ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	plaintext, err := aesGCM.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// Encrypt 用当前生效密钥加密
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	return seal(encKey, plaintext)
}

// Decrypt 用当前生效密钥解密
func Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	return open(encKey, ciphertext)
}

// EncryptWith 用指定密钥加密（迁移/重封场景）。
func EncryptWith(key, plaintext string) (string, error) { return seal(deriveKey(key), plaintext) }

// DecryptWith 用指定密钥解密；密钥不匹配时返回错误而非猜测结果。
func DecryptWith(key, ciphertext string) (string, error) { return open(deriveKey(key), ciphertext) }

// HashSecret 给 bearer 凭证明文算索引用的摘要（SHA-256，hex）。
//
// 消费者 API Key 与口令的存储要求相反：Key 是 256 bit 随机值，不是低熵口令，
// 所以这里要的是"可索引的单向摘要"而不是慢哈希——bcrypt/scrypt 在此只会
// 给每次请求加几十毫秒，对离线爆破的抗性却几乎不增加（候选空间由 Key 长度决定）。
// 前提是新库只存摘要：拿到只读库的人不该同时拿到可用的凭证。
func HashSecret(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

const maskKeep = 4

// MaskString 脱敏处理：保留首尾各 4 位。
//
// 两个原版缺陷一并修掉：
//   - 短密钥会让 s[:2] 直接越界 panic（长度 1 的密钥就能把请求打成 500）；
//   - 长度恰好 8 时走另一分支，会把前 4 + 后 4 = 全部字符露出来，等于没脱敏。
//
// 规则：只有当被遮住的中间部分不短于露出的首尾之和时，部分露出才有意义，
// 否则整串遮蔽。
func MaskString(s string) string {
	if len(s) < maskKeep*4 {
		return strings.Repeat("*", maskKeep)
	}
	return s[:maskKeep] + strings.Repeat("*", maskKeep) + s[len(s)-maskKeep:]
}

// HashPassword bcrypt 加密密码
func HashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hashed), err
}

// CheckPassword 校验密码
func CheckPassword(hashed, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
}
