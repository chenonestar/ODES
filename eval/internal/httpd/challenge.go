package httpd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── 简易算术验证（FR-ANS-032）───────────────────────────────────────
//
// 文档要求的是「触发频率阈值时**要求完成简易算术验证**」，不是直接拒绝。
// 差别在现场是实打实的：测评网络里存在"无手机者借用他人手机"的正常
// 场景，同一 IP 连续多次提交是预期行为；一刀切返回 429 会把这些人挡在
// 门外，而他们散会后无法补测。
//
// 两条纪律：
//   - **本地生成**，不调用任何外部验证码服务（现场全程离线，且外部服务
//     会把访问特征送出去）。
//   - 挑战与 IP 一样**只存在于内存**，进程退出即消失（NFR-ANO-040）。
//     挑战 ID 是随机值，不含 IP、不含令牌。

type challenge struct {
	answer  int
	expires time.Time
}

type challengeStore struct {
	mu sync.Mutex
	m  map[string]challenge
}

func newChallengeStore() *challengeStore {
	cs := &challengeStore{m: map[string]challenge{}}
	go func() {
		for range time.Tick(2 * time.Minute) {
			cs.mu.Lock()
			now := time.Now()
			for k, c := range cs.m {
				if now.After(c.expires) {
					delete(cs.m, k)
				}
			}
			cs.mu.Unlock()
		}
	}()
	return cs
}

// issue 生成一道两位数以内的加法题。够挡住脚本，又不为难年长的参评人员。
func (cs *challengeStore) issue() (id, question string) {
	a := randInt(9) + 1
	b := randInt(9) + 1
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	id = base64.RawURLEncoding.EncodeToString(buf)

	cs.mu.Lock()
	cs.m[id] = challenge{answer: a + b, expires: time.Now().Add(5 * time.Minute)}
	cs.mu.Unlock()
	return id, fmt.Sprintf("%d + %d = ?", a, b)
}

// verify 校验并**一次性消费**挑战：答对答错都作废，避免同一道题被重放。
func (cs *challengeStore) verify(id, answer string) bool {
	cs.mu.Lock()
	c, ok := cs.m[id]
	delete(cs.m, id)
	cs.mu.Unlock()
	if !ok || time.Now().After(c.expires) {
		return false
	}
	got, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeEq(int32(got), int32(c.answer)) == 1
}

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// challengeMiddleware 在超过频率阈值时要求完成算术验证，而不是直接拒绝。
//
// 通过 X-Odes-Challenge / X-Odes-Answer 两个请求头携带，作答端在收到
// 428 时弹出输入框、带着答案重试一次。选请求头而非表单字段，是为了
// 不改动提交报文的结构（LLD 8.3）。
func (rl *rateLimiter) challengeMiddleware(cs *challengeStore, max int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rl.allow(clientIP(r), max) {
				next.ServeHTTP(w, r)
				return
			}
			// 已带答案：验对就放行，这一次不再计入频率窗口
			if id := r.Header.Get("X-Odes-Challenge"); id != "" {
				if cs.verify(id, r.Header.Get("X-Odes-Answer")) {
					next.ServeHTTP(w, r)
					return
				}
			}
			id, question := cs.issue()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPreconditionRequired) // 428
			fmt.Fprintf(w, `{"ok":false,"code":"CHALLENGE","msg":%q,"challenge":%q,"question":%q}`,
				"提交过于频繁，请完成一道算术题以继续", id, question)
		})
	}
}
