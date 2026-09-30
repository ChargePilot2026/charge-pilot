// Package phonecrypto 集中处理充电用户手机号的保护与还原。
//
// 手机号同时存两份：phone_enc 是 AES-GCM 密文，phone_hash 是明文的 SHA-256。
// 两者用途不同，不能互相替代——hash 只能做等值查询（判断一个号码是否已被占用），
// 永远还原不出明文；只有 enc 能还原，而还原需要部署密钥 PhoneEncryptionKey。
//
// 密钥必须恰好 32 字节（AES-256），并且跨重启保持稳定：换密钥会让所有已存手机号
// 无法解密，而不是报错。这和 user_account.go 绑号时"未配置密钥就拒绝绑定"的
// 策略是同一件事的两端。
//
// 密文格式是 nonce ‖ ciphertext ‖ tag，即 gcm.Seal 直接返回的那一整段。
// nonce 每次随机并前置在密文头部，解密时按 gcm.NonceSize() 切出来即可。
package phonecrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// KeySize 是 AES-256 要求的密钥字节数。
const KeySize = 32

var (
	// ErrNoKey 表示部署没有配置手机号加密密钥。调用方应拒绝写操作，而不是退化成明文存储。
	ErrNoKey = errors.New("手机号加密密钥未配置")
	// ErrInvalidPhone 表示号码不符合中国大陆手机号格式。
	ErrInvalidPhone = errors.New("手机号格式无效")
	// ErrCiphertext 表示密文长度不足以容纳 nonce，或解密失败（密钥不对、密文被改）。
	ErrCiphertext = errors.New("手机号密文无法解密")
)

// mobile 接受国内三大运营商实际发放的号段形态。
//
// 这里刻意不校验短信验证码：没有真实短信服务商时平台无法证明号码归属，
// 假装能证明就等于允许任何人绑定不属于自己的号码。
var mobile = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

// Valid 判断号码是否为可绑定的中国大陆手机号。
func Valid(phone string) bool { return mobile.MatchString(strings.TrimSpace(phone)) }

// Normalize 去掉首尾空白。绑定、加密、哈希三处都走它，避免同一个号码算出两个 hash。
func Normalize(phone string) string { return strings.TrimSpace(phone) }

// Hash 返回号码的 SHA-256 十六进制串，用于等值查询。不可逆。
func Hash(phone string) string {
	sum := sha256.Sum256([]byte(Normalize(phone)))
	return hex.EncodeToString(sum[:])
}

// Mask 把号码脱敏成 138****8000 的形式，用于不需要完整号码的展示位。
func Mask(phone string) string {
	phone = Normalize(phone)
	if len(phone) < 7 {
		return "***"
	}
	return phone[:3] + "****" + phone[len(phone)-4:]
}

// Encrypt 用 AES-GCM 加密号码，nonce 前置在返回值头部。
func Encrypt(key []byte, phone string) ([]byte, error) {
	if len(key) != KeySize {
		return nil, ErrNoKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(Normalize(phone)), nil), nil
}

// Decrypt 还原 Encrypt 写入的密文。
//
// 后台运营侧的列表页需要它：要在列表里显示完整号码，就必须能还原 phone_enc。
// 这是"加密防的是数据库裸读，不防后台本身"的直接后果——能还原的入口必须
// 配独立权限并留审计，否则加密只是把风险从 DBA 挪到了运营岗。
func Decrypt(key, blob []byte) (string, error) {
	if len(key) != KeySize {
		return "", ErrNoKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", ErrCiphertext
	}
	nonce, ciphertext := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrCiphertext
	}
	return string(plain), nil
}
