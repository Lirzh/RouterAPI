package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"

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

// Encrypt 加密
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(encKey)
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
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 解密
func Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(encKey)
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
	nonce, data := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// MaskString 脱敏处理
func MaskString(s string) string {
	if len(s) <= 8 {
		return s[:2] + "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
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
