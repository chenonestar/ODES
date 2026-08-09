// Package anon 集中全部与匿名性相关的原语。
//
// 设计意图（HLD 5.4）：把随机主键生成、令牌生成、输出重排集中在一个包里，
// 使 code review 有明确检查点——凡是对外输出记录集合的代码路径，
// 必须能追溯到一次 Shuffle 调用。
//
// 本包不得引入任何顺序性、时间性来源：全部随机数取自 crypto/rand。
package anon

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
)

// ID 是作答记录与令牌的主键类型：16 字节随机 UUIDv4。
//
// 禁止使用自增整数或 rowid 作为业务主键（NFR-ANO-011）。配合建表时的
// WITHOUT ROWID，表以主键聚簇，物理存储顺序因主键随机而与插入顺序无关。
type ID [16]byte

// NewID 生成随机主键。全项目唯一的 ID 生成入口，其它地方不得自行生成。
func NewID() ID {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，此时继续运行会产生可预测
		// 的主键，直接摧毁匿名性。宁可崩溃也不能降级。
		panic("anon: crypto/rand 不可用，无法生成随机主键: " + err.Error())
	}
	id[6] = (id[6] & 0x0f) | 0x40 // version 4
	id[8] = (id[8] & 0x3f) | 0x80 // variant 10
	return id
}

func (id ID) Bytes() []byte { return id[:] }

func (id ID) String() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

// Hex 是作答载荷中引用题目与对象所用的紧凑形式（LLD 3.1 的 q / s 字段）。
func (id ID) Hex() string { return fmt.Sprintf("%x", id[:]) }

// IDFromBytes 由数据库读出的 BLOB 还原主键。
func IDFromBytes(b []byte) (ID, error) {
	var id ID
	if len(b) != 16 {
		return id, fmt.Errorf("anon: 主键长度应为 16 字节，得到 %d", len(b))
	}
	copy(id[:], b)
	return id, nil
}

func MustIDFromHex(s string) (ID, error) {
	var id ID
	if len(s) != 32 {
		return id, fmt.Errorf("anon: 十六进制主键长度应为 32，得到 %d", len(s))
	}
	for i := 0; i < 16; i++ {
		var v byte
		for j := 0; j < 2; j++ {
			c := s[i*2+j]
			var d byte
			switch {
			case c >= '0' && c <= '9':
				d = c - '0'
			case c >= 'a' && c <= 'f':
				d = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				d = c - 'A' + 10
			default:
				return id, fmt.Errorf("anon: 非法十六进制字符 %q", c)
			}
			v = v<<4 | d
		}
		id[i] = v
	}
	return id, nil
}

// Charset 是令牌值与短码共用的字符表：31 个字符，已去除易混字符 0 O 1 I L。
//
// 31 是素数，便于短码校验位取模（LLD 4.2）。
const Charset = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// TokenLen 是令牌值的字符数。31 字符表下 32 位 ≈ 158 位熵，
// 超过 FR-TKN-020 要求的 128 位。
const TokenLen = 32

// NewToken 生成令牌值（FR-TKN-020）。
//
// 逐字符用 crypto/rand.Int 取值而非"随机字节取模"——后者在字节值域 256
// 不是字符表长度 31 的整数倍时会产生取模偏置，使前若干个字符出现概率
// 偏高，实际熵低于标称值。
func NewToken() string {
	out := make([]byte, TokenLen)
	for i := range out {
		out[i] = Charset[randIndex(len(Charset))]
	}
	return string(out)
}

// NewShortCode 生成 8 位短码：7 位随机 + 1 位校验位（FR-TKN-023）。
//
// 校验位使参评人员输错时能即时提示，而不是提交后才报错。
func NewShortCode() string {
	body := make([]byte, 7)
	for i := range body {
		body[i] = Charset[randIndex(len(Charset))]
	}
	return string(body) + string(checkDigit(string(body)))
}

// VerifyShortCode 校验短码格式与校验位。
func VerifyShortCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for i := 0; i < 7; i++ {
		if indexOf(code[i]) < 0 {
			return false
		}
	}
	return code[7] == checkDigit(code[:7])
}

func checkDigit(body string) byte {
	sum := 0
	for i := 0; i < len(body); i++ {
		sum += indexOf(body[i]) * (i + 1)
	}
	return Charset[sum%len(Charset)]
}

func indexOf(c byte) int {
	for i := 0; i < len(Charset); i++ {
		if Charset[i] == c {
			return i
		}
	}
	return -1
}

// Shuffle 是基于 crypto/rand 的 Fisher–Yates 重排。
//
// 所有对外输出记录集合之前必须调用（NFR-ANO-030）：统计页、评语列表、
// 原始数据导出、统计聚合的输入。使用 crypto/rand 而非 math/rand——
// 后者的序列可由种子复现，等于把顺序信息又还了回去。
func Shuffle[T any](s []T) {
	for i := len(s) - 1; i > 0; i-- {
		j := randIndex(i + 1)
		s[i], s[j] = s[j], s[i]
	}
}

// Label 生成"作答记录 #N"。N 是重排后的位置，不入库（NFR-ANO-030）。
// 两次导出的编号互不一致，这是刻意设计。
func Label(i int) string { return fmt.Sprintf("作答记录 #%d", i+1) }

func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("anon: crypto/rand 不可用: " + err.Error())
	}
	return int(v.Int64())
}

// U32 供需要少量随机整数的场合使用（如打印排布的抖动）。
func U32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("anon: crypto/rand 不可用: " + err.Error())
	}
	return binary.BigEndian.Uint32(b[:])
}
