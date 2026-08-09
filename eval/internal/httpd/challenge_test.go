package httpd

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// FR-ANS-032：超过频率阈值时要求完成简易算术验证，而不是直接拒绝。
//
// 差别在现场是实打实的：测评网络里"无手机者借用他人手机"是正常场景，
// 一刀切 429 会把这些人挡在门外，而他们散会后无法补测。
func TestChallengeInsteadOfHardReject(t *testing.T) {
	rl := newRateLimiter()
	cs := newChallengeStore()
	const max = 3

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.challengeMiddleware(cs, max)(ok)

	call := func(id, ans string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/e/x/submit", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		if id != "" {
			r.Header.Set("X-Odes-Challenge", id)
			r.Header.Set("X-Odes-Answer", ans)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	for i := 0; i < max; i++ {
		if got := call("", "").Code; got != http.StatusOK {
			t.Fatalf("第 %d 次应放行，得到 %d", i+1, got)
		}
	}

	// 超过阈值：应出题（428），而不是 429
	w := call("", "")
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("超过阈值应返回 428 并出题，得到 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"code":"CHALLENGE"`) {
		t.Fatalf("响应里没有 CHALLENGE：%s", body)
	}
	id := between(body, `"challenge":"`, `"`)
	question := between(body, `"question":"`, `"`)
	if id == "" || question == "" {
		t.Fatalf("挑战 ID 或题面为空：%s", body)
	}

	// 答错：不放行
	if got := call(id, "999").Code; got == http.StatusOK {
		t.Error("答错也放行了")
	}

	// 一次性消费：同一个 ID 即便答对也不该再生效
	if got := call(id, answerOf(t, question)).Code; got == http.StatusOK {
		t.Error("同一挑战被重复使用：应当一次性消费，防止重放")
	}

	// 重新取一道并答对：放行
	w = call("", "")
	id = between(w.Body.String(), `"challenge":"`, `"`)
	question = between(w.Body.String(), `"question":"`, `"`)
	if got := call(id, answerOf(t, question)).Code; got != http.StatusOK {
		t.Errorf("答对后应放行，得到 %d", got)
	}
}

func TestChallengeQuestionIsSolvable(t *testing.T) {
	cs := newChallengeStore()
	for i := 0; i < 200; i++ {
		id, q := cs.issue()
		if !cs.verify(id, answerOf(t, q)) {
			t.Fatalf("自己出的题算不对：%s", q)
		}
	}
}

// answerOf 解 "a + b = ?" —— 题面必须简单到年长参评人员也能口算。
func answerOf(t *testing.T, q string) string {
	t.Helper()
	parts := strings.Split(strings.TrimSuffix(strings.TrimSpace(q), " = ?"), " + ")
	if len(parts) != 2 {
		t.Fatalf("题面格式意外：%q", q)
	}
	a, err1 := strconv.Atoi(parts[0])
	b, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("题面不是两个整数相加：%q", q)
	}
	return strconv.Itoa(a + b)
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return ""
	}
	return s[:j]
}
