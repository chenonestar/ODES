// Package crypto 实现应用层字段加密与密钥生命周期（LLD 第 3 章）。
//
// 采用 KEK/DEK 两层结构而非用口令直接加密数据：修改口令时只需重新包裹
// DEK，无需重新加密全部作答记录。
//
//	口令 P ──Argon2id(salt)──> KEK ──AES-GCM 包裹──> wrapped_DEK（可落盘）
//	                                  DEK（32 字节随机，仅存于内存）
//
// 防护边界（须在评审中明确，LLD 3.5）：
//   - 笔记本失窃、数据文件被拷走 —— 有效，这是本机制的主要目标
//   - 归档包在介质上被他人取得   —— 有效
//   - 防止管理员查看作答内容     —— 无效。程序运行时 DEK 就在内存里。
//     防管理员靠的是 anon 包的匿名隔离，不是加密。两套机制目标不同。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// Argon2id 参数（LLD 3.3）。解锁耗时约 0.3–0.8 秒，仅启动时一次。
const (
	argonMem     = 64 * 1024 // 64 MB
	argonTime    = 3
	argonThreads = 4
	keyLen       = 32
	saltLen      = 16
	nonceLen     = 12
)

var (
	ErrWrongPassword = errors.New("口令不正确")
	ErrCorrupted     = errors.New("密文损坏或被篡改")
)

// Meta 是需要落盘的密钥材料。三项都无需保密：没有口令，wrapped_dek 不可解。
type Meta struct {
	KEKSalt    []byte // meta.kek_salt
	WrappedDEK []byte // meta.wrapped_dek
	PwdHash    []byte // meta.admin_pwd_hash，与 KEK 分别派生，仅用于登录校验
}

// Vault 持有解开后的 DEK。不提供任何导出密钥的方法。
type Vault struct {
	aead cipher.AEAD
	dek  []byte // 保留原始字节以便 Close 时清零
}

// Init 首次启动：生成盐与 DEK，用口令派生的 KEK 包裹后返回可落盘的 Meta。
func Init(password string) (*Vault, Meta, error) {
	if err := CheckPasswordStrength(password); err != nil {
		return nil, Meta{}, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, Meta{}, err
	}
	dek := make([]byte, keyLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, Meta{}, err
	}

	kek := deriveKEK(password, salt)
	wrapped, err := sealWith(kek, dek, nil)
	if err != nil {
		return nil, Meta{}, err
	}
	v, err := newVault(dek)
	if err != nil {
		return nil, Meta{}, err
	}
	return v, Meta{KEKSalt: salt, WrappedDEK: wrapped, PwdHash: derivePwdHash(password, salt)}, nil
}

// Unlock 每次启动：口令 → KEK → 解开 wrapped_DEK。
func Unlock(password string, m Meta) (*Vault, error) {
	// 先比对口令哈希，使"口令错"与"数据损坏"两种失败可区分。
	// 用恒定时间比较，避免通过响应时间侧信道逐字节试探。
	if subtle.ConstantTimeCompare(derivePwdHash(password, m.KEKSalt), m.PwdHash) != 1 {
		return nil, ErrWrongPassword
	}
	kek := deriveKEK(password, m.KEKSalt)
	dek, err := openWith(kek, m.WrappedDEK, nil)
	if err != nil {
		return nil, ErrCorrupted
	}
	return newVault(dek)
}

// VerifyPassword 仅校验口令，不解开 DEK。供登录失败计数等场景使用。
func VerifyPassword(password string, m Meta) bool {
	return subtle.ConstantTimeCompare(derivePwdHash(password, m.KEKSalt), m.PwdHash) == 1
}

func newVault(dek []byte) (*Vault, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead, dek: dek}, nil
}

// Seal 加密。格式：nonce(12B) || ciphertext || tag(16B)（LLD 3.2）。
//
// aad 用于绑定上下文，作答载荷传 project_id，防止密文被跨项目重放。
func (v *Vault) Seal(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(nonce, nonce, plain, aad), nil
}

// Open 解密并校验完整性。任何篡改都会在此失败。
func (v *Vault) Open(ct, aad []byte) ([]byte, error) {
	if len(ct) < nonceLen {
		return nil, ErrCorrupted
	}
	plain, err := v.aead.Open(nil, ct[:nonceLen], ct[nonceLen:], aad)
	if err != nil {
		return nil, ErrCorrupted
	}
	return plain, nil
}

// SealString / OpenString 是姓名、职务等短字段的便捷封装。
func (v *Vault) SealString(s string, aad []byte) ([]byte, error) {
	return v.Seal([]byte(s), aad)
}

func (v *Vault) OpenString(ct, aad []byte) (string, error) {
	if len(ct) == 0 {
		return "", nil
	}
	b, err := v.Open(ct, aad)
	return string(b), err
}

// Close 显式清零内存中的密钥材料。进程退出前调用。
//
// 说明：Go 的 GC 可能已复制过这些字节，清零不是绝对保证；它降低而非
// 消除内存取证的可行性。真正的边界见包注释。
func (v *Vault) Close() {
	for i := range v.dek {
		v.dek[i] = 0
	}
	v.aead = nil
}

func deriveKEK(password string, salt []byte) []byte {
	return argon2.IDKey([]byte("kek:"+password), salt, argonTime, argonMem, argonThreads, keyLen)
}

// derivePwdHash 与 KEK 用不同的 info 前缀分别派生：登录校验值即便泄露，
// 也不能用于解开 wrapped_DEK。
func derivePwdHash(password string, salt []byte) []byte {
	return argon2.IDKey([]byte("pwd:"+password), salt, argonTime, argonMem, argonThreads, keyLen)
}

func sealWith(key, plain, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, aad), nil
}

func openWith(key, ct, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < nonceLen {
		return nil, ErrCorrupted
	}
	return aead.Open(nil, ct[:nonceLen], ct[nonceLen:], aad)
}

// CheckPasswordStrength 实现 FR-SYS-010：长度 ≥10 位、含两类以上字符。
func CheckPasswordStrength(p string) error {
	if len([]rune(p)) < 10 {
		return fmt.Errorf("口令长度须不少于 10 位（当前 %d 位）", len([]rune(p)))
	}
	var lower, upper, digit, other bool
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			other = true
		}
	}
	kinds := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			kinds++
		}
	}
	if kinds < 2 {
		return errors.New("口令须包含两类以上字符（小写、大写、数字、符号）")
	}
	return nil
}
