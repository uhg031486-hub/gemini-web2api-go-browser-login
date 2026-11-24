package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPickCookieAccountRotatesWithinSameSecond(t *testing.T) {
	// Giả lập pool có 4 tài khoản sạch cùng trạng thái.
	var ids []int64
	for i := 0; i < 4; i++ {
		id, err := accountAdd("rr-test", "SAPISID=s; __Secure-1PSID=p", "")
		if err != nil {
			t.Fatalf("accountAdd failed: %v", err)
		}
		ids = append(ids, id)
		t.Cleanup(func() { _ = accountDelete(id) })
	}

	// Ép các tài khoản có cùng fail_count=0 và cùng last_used_at để tái hiện kịch bản
	// nhiều request tới trong cùng một giây.
	for _, id := range ids {
		_, _ = getDB().Exec(`UPDATE accounts SET fail_count=0, last_used_at=1700000000 WHERE id=?`, id)
	}

	seen := map[int64]int{}
	for i := 0; i < 4; i++ {
		a, ok := pickCookieAccount()
		if !ok || a == nil {
			t.Fatalf("pick %d failed", i)
		}
		seen[a.ID]++
		releaseAccountInflight(a.ID)
	}

	if len(seen) < 4 {
		t.Fatalf("trong cùng 1 giây chỉ chọn trúng %d/4 tài khoản (bị dồn vào 1 tài khoản): %v", len(seen), seen)
	}
}

func TestPickCookieAccountBalancesInflight(t *testing.T) {
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := accountAdd("inflight-test", "SAPISID=s; __Secure-1PSID=p", "")
		if err != nil {
			t.Fatalf("accountAdd failed: %v", err)
		}
		ids = append(ids, id)
		t.Cleanup(func() { _ = accountDelete(id) })
	}

	// Pick lần 1 chọn tài khoản đầu tiên và CHƯA release (đang bận inflight=1).
	a1, ok1 := pickCookieAccount()
	if !ok1 {
		t.Fatal("pick 1 failed")
	}
	defer releaseAccountInflight(a1.ID)

	// Pick tiếp 2 lần sau khi a1 vẫn đang bận: phải chọn các tài khoản còn rảnh, không đè lên a1.
	a2, ok2 := pickCookieAccount()
	if !ok2 {
		t.Fatal("pick 2 failed")
	}
	defer releaseAccountInflight(a2.ID)
	a3, ok3 := pickCookieAccount()
	if !ok3 {
		t.Fatal("pick 3 failed")
	}
	defer releaseAccountInflight(a3.ID)

	if a2.ID == a1.ID || a3.ID == a1.ID || a2.ID == a3.ID {
		t.Fatalf("không cân bằng luồng bận (inflight): got [%d, %d, %d]", a1.ID, a2.ID, a3.ID)
	}
}

func TestParseToolCallsSupportsWebVariants(t *testing.T) {
	// 1. Arguments là chuỗi JSON escaped (thường gặp trên model web)
	rawStrArgs := "```tool_call\n{\"name\": \"read_file\", \"arguments\": \"{\\\"path\\\":\\\"a.txt\\\"}\"}\n```"
	clean1, calls1 := parseToolCalls(rawStrArgs)
	if len(calls1) != 1 || calls1[0].Function.Name != "read_file" {
		t.Fatalf("không parse được arguments dạng chuỗi JSON: calls=%+v clean=%q", calls1, clean1)
	}
	var args1 map[string]interface{}
	if err := json.Unmarshal([]byte(calls1[0].Function.Arguments), &args1); err != nil || args1["path"] != "a.txt" {
		t.Fatalf("arguments sau chuẩn hóa sai: %q", calls1[0].Function.Arguments)
	}

	// 2. Fallback khi model quên chữ tool_call mà xuất ```json có đúng name + arguments
	rawJSONFence := "Tôi sẽ đọc file ngay.\n```json\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"b.txt\"}}\n```"
	clean2, calls2 := parseToolCalls(rawJSONFence)
	if len(calls2) != 1 || calls2[0].Function.Name != "read_file" || clean2 != "Tôi sẽ đọc file ngay." {
		t.Fatalf("không parse được fallback ```json: calls=%+v clean=%q", calls2, clean2)
	}

	// 3. Edge case: ```json config bình thường (không phải tool call) KHÔNG ĐƯỢC parse nhầm
	normalJSON := "Cấu hình đây:\n```json\n{\"port\": 8083, \"name\": \"demo\"}\n```"
	clean3, calls3 := parseToolCalls(normalJSON)
	if len(calls3) != 0 || clean3 != normalJSON {
		t.Fatalf("nhận nhầm JSON thường thành tool_call: calls=%+v clean=%q", calls3, clean3)
	}
}

func makeMockWrbFrame(text string) string {
	inner := make([]interface{}, 44)
	inner[1] = []interface{}{"c_x", "r_y"}
	inner[4] = []interface{}{
		[]interface{}{
			"rc_z",
			[]interface{}{text},
			nil, nil, nil, nil, nil, nil,
			[]interface{}{2},
			"en",
		},
	}
	inner[39] = hexFlash38
	inner[42] = "4 Hydrogen"
	inner[43] = true
	innerBytes, _ := json.Marshal(inner)
	outer := [][]interface{}{
		{"wrb.fr", nil, string(innerBytes)},
	}
	outerBytes, _ := json.Marshal(outer)
	return string(outerBytes)
}

func TestIsUpstreamSoftErrorAndPrefix(t *testing.T) {
	softCases := []string{
		"I seem to be encountering an error. Can I try something else for you?",
		"  i encountered an error doing what you asked. could you try again? \n",
		"Something went wrong and I wasn't able to generate a response.",
		"Something went wrong. Please try again.",
		"I'm having a hard time fulfilling your request. Can I help you with something else instead?",
		"Sorry, something went wrong. Please try your request again.",
	}
	for _, s := range softCases {
		if !isUpstreamSoftError(s) {
			t.Fatalf("expected soft error match for %q", s)
		}
		if !isUpstreamSoftErrorPrefix(s) {
			t.Fatalf("expected soft error prefix match for full %q", s)
		}
	}

	normalCases := []string{
		"",
		"OK",
		"Tôi dường như đang gặp lỗi. Tôi có thể thử cách khác giúp bạn không?",
		"I seem to be encountering an error. Can I try something else for you? Here is the translated text: Xin chào.",
		"Tôi là trợ lý dịch thuật.",
	}
	for _, s := range normalCases {
		if isUpstreamSoftError(s) {
			t.Fatalf("unexpected false-positive soft error for %q", s)
		}
	}

	if !isUpstreamSoftErrorPrefix("I seem to be encountering") {
		t.Fatal("expected partial prefix of canned soft error to be held back while streaming")
	}
	if isUpstreamSoftErrorPrefix("I seem to understand Vietnamese") {
		t.Fatal("diverged prefix should not be held back")
	}
	if isUpstreamSoftErrorPrefix("Xin chào") {
		t.Fatal("normal translation prefix should not be held back")
	}
}

func TestStreamGenerateRetriesSoftErrorAcrossAccounts(t *testing.T) {
	id1, err := accountAdd("soft-err-1", "SAPISID=s1; __Secure-1PSID=p1", "")
	if err != nil {
		t.Fatalf("accountAdd 1 failed: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id1) })
	id2, err := accountAdd("soft-err-2", "SAPISID=s2; __Secure-1PSID=p2", "")
	if err != nil {
		t.Fatalf("accountAdd 2 failed: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id2) })

	blMu.Lock()
	oldFetchAt := blFetchAt
	blFetchAt = time.Now()
	blMu.Unlock()
	rtMu.Lock()
	oldRT := rtVal
	rtVal.RetryAttempts = 3
	rtVal.RetryDelaySec = 0
	rtMu.Unlock()
	t.Cleanup(func() {
		blMu.Lock()
		blFetchAt = oldFetchAt
		blMu.Unlock()
		rtMu.Lock()
		rtVal = oldRT
		rtMu.Unlock()
	})

	oldXSRF, oldReq, oldSleep := getXSRFFn, doGeminiReqFn, retrySleepFn
	t.Cleanup(func() {
		getXSRFFn, doGeminiReqFn, retrySleepFn = oldXSRF, oldReq, oldSleep
	})
	retrySleepFn = func(time.Duration) {}
	getXSRFFn = func(cookie, proxyURL string, authUser int) (string, error) {
		return "xsrf-ok", nil
	}

	var usedCookies []string
	doGeminiReqFn = func(endpoint, body string, headers map[string]string, proxyURL string, lineCB func(string), timeoutSec int) (int, []byte, int64, []string, error) {
		usedCookies = append(usedCookies, headers["Cookie"])
		if len(usedCookies) == 1 {
			frame := makeMockWrbFrame("I seem to be encountering an error. Can I try something else for you?")
			if lineCB != nil {
				lineCB(frame)
			}
			raw := ")]}'\n\n900\n" + frame + "\n"
			return 200, []byte(raw), 50, nil, nil
		}
		frame := makeMockWrbFrame("Xin chào thế giới")
		if lineCB != nil {
			lineCB(frame)
		}
		raw := ")]}'\n\n900\n" + frame + "\n"
		return 200, []byte(raw), 40, nil, nil
	}

	var streamed strings.Builder
	res, err := streamGenerate("Translate hello world", "Translate hello world", Models["gemini-4-hydrogen-low"], func(d string) {
		streamed.WriteString(d)
	}, nil)
	if err != nil {
		t.Fatalf("expected retry across accounts to succeed, got err: %v", err)
	}
	if len(usedCookies) != 2 {
		t.Fatalf("expected 2 upstream attempts across accounts, got %d", len(usedCookies))
	}
	if usedCookies[0] == usedCookies[1] {
		t.Fatalf("expected retry to switch accounts, reused same cookie %q", usedCookies[0])
	}
	if got := extractResponseText(res.Raw); got != "Xin chào thế giới" {
		t.Fatalf("unexpected response text: %q", got)
	}
	if strings.Contains(streamed.String(), "encountering an error") {
		t.Fatalf("soft error leaked into streamed output: %q", streamed.String())
	}
	if streamed.String() != "Xin chào thế giới" {
		t.Fatalf("unexpected streamed delta: %q", streamed.String())
	}
	// Soft error không được làm tăng fail_count hoặc khóa cookie.
	var fc int
	if err := getDB().QueryRow(`SELECT fail_count FROM accounts WHERE id=?`, id1).Scan(&fc); err != nil || fc != 0 {
		t.Fatalf("expected fail_count=0 after soft error on acct 1, got fc=%d err=%v", fc, err)
	}
	if inflight := accountInflightCount(id1) + accountInflightCount(id2); inflight != 0 {
		t.Fatalf("leaked inflight count after retry: %d", inflight)
	}
}

func TestStreamGenerateFailsWhenAllSoftErrorsExhaustRetryBudget(t *testing.T) {
	id1, err := accountAdd("soft-exhaust-1", "SAPISID=e1; __Secure-1PSID=pe1", "")
	if err != nil {
		t.Fatalf("accountAdd failed: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id1) })

	blMu.Lock()
	oldFetchAt := blFetchAt
	blFetchAt = time.Now()
	blMu.Unlock()
	rtMu.Lock()
	oldRT := rtVal
	rtVal.RetryAttempts = 3
	rtVal.RetryDelaySec = 0
	rtMu.Unlock()
	t.Cleanup(func() {
		blMu.Lock()
		blFetchAt = oldFetchAt
		blMu.Unlock()
		rtMu.Lock()
		rtVal = oldRT
		rtMu.Unlock()
	})

	oldXSRF, oldReq, oldSleep := getXSRFFn, doGeminiReqFn, retrySleepFn
	t.Cleanup(func() {
		getXSRFFn, doGeminiReqFn, retrySleepFn = oldXSRF, oldReq, oldSleep
	})
	retrySleepFn = func(time.Duration) {}
	getXSRFFn = func(cookie, proxyURL string, authUser int) (string, error) {
		return "xsrf-ok", nil
	}
	doGeminiReqFn = func(endpoint, body string, headers map[string]string, proxyURL string, lineCB func(string), timeoutSec int) (int, []byte, int64, []string, error) {
		frame := makeMockWrbFrame("I encountered an error doing what you asked. Could you try again?")
		if lineCB != nil {
			lineCB(frame)
		}
		return 200, []byte(")]}'\n\n900\n" + frame + "\n"), 30, nil, nil
	}

	var streamed strings.Builder
	res, err := streamGenerate("hi", "hi", Models["gemini-4-hydrogen-low"], func(d string) {
		streamed.WriteString(d)
	}, nil)
	if err == nil {
		t.Fatal("expected error when all retries return soft error, got nil")
	}
	if !strings.Contains(fmt.Sprint(err), "soft error") {
		t.Fatalf("expected descriptive soft error, got: %v", err)
	}
	if streamed.Len() != 0 {
		t.Fatalf("soft error leaked into stream on failure: %q", streamed.String())
	}
	if res == nil || res.AccountID == 0 {
		t.Fatalf("expected error attribution to preserve AccountID, got: %+v", res)
	}
}
