// Package secretbox 负责凭据的对称加密、解密与掩码。
//
// 设计约束（见 docs/集中配置管理台设计.md）：
//   - 数据库只存密文，明文永不落库、不回显；
//   - 主密钥由部署环境注入，不进数据库、不进代码仓库；
//   - 密文带算法版本前缀，便于将来轮换算法。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// Version 是当前密文格式版本，写入密文前缀。
	Version = "v1"
	// KeySize 是 AES-256 要求的密钥长度。
	KeySize = 32
)

// ErrInvalidKey 表示主密钥长度不符合要求。
var ErrInvalidKey = errors.New("secretbox: key must be 32 bytes for AES-256")

// ErrMalformed 表示密文格式无法识别或被截断。
var ErrMalformed = errors.New("secretbox: malformed ciphertext")

// ErrMismatch 表示密钥不匹配或密文被篡改。
var ErrMismatch = errors.New("secretbox: decryption failed (wrong key or tampered data)")

// Box 封装一个 AES-256-GCM AEAD。
type Box struct {
	aead cipher.AEAD
}

// New 用给定的 32 字节密钥构造 Box。
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: got %d bytes", ErrInvalidKey, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// NewFromBase64 用 base64 编码的 32 字节密钥构造 Box。
func NewFromBase64(encoded string) (*Box, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("secretbox: decode key: %w", err)
	}
	return New(raw)
}

// GenerateKey 生成一个可直接写入环境变量的 base64 密钥。
func GenerateKey() (string, error) {
	buf := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Seal 加密明文，返回 "v1:<base64(nonce||ciphertext)>"。
func (b *Box) Seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), []byte(Version))
	return Version + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

// Open 解密 Seal 生成的密文。
func (b *Box) Open(sealed string) (string, error) {
	prefix, payload, ok := strings.Cut(sealed, ":")
	if !ok || prefix != Version {
		return "", ErrMalformed
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", ErrMalformed
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrMalformed
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(Version))
	if err != nil {
		return "", ErrMismatch
	}
	return string(plain), nil
}

// Mask 生成给界面展示的掩码：保留末 4 位，其余固定为 ****。
// 空值返回未配置，短于 8 位的值只显示 ****，避免泄露过短凭据。
func Mask(plain string) string {
	if plain == "" {
		return ""
	}
	runes := []rune(plain)
	if len(runes) < 8 {
		return "****"
	}
	return "****" + string(runes[len(runes)-4:])
}
