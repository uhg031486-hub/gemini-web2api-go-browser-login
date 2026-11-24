package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// keyCookieWants 是判断一条 cookie 是否"复制全了"要看的关键项。
//
// 抓包实测浏览器每次发往 gemini.google.com 的有 30 个 cookie，其中 16 个是
// 每请求必带的登录态项。这里只挑最能反映"复制全没全"的几个来展示，不是说
// 别的不重要 —— 我们把整串原样转发，少一个就是少发一个。
var keyCookieWants = []string{"SID", "HSID", "SSID", "APISID", "SAPISID",
	"__Secure-1PSID", "__Secure-1PSIDTS", "__Secure-1PAPISID", "__Secure-1PSIDCC"}

// cookieAcctView 是账号的脱敏视图：**不含完整 cookie 值**，只回状态摘要。
// 凭证没必要从服务端再发回浏览器一次（跟旧 handleAdminCookie 同原则）。
func cookieAcctView(a CookieAccount) map[string]interface{} {
	names := cookieNames(a.Cookie)
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	var key []string
	for _, w := range keyCookieWants {
		if nameSet[w] {
			key = append(key, w)
		}
	}
	tail := ""
	if s := extractSAPISID(a.Cookie); len(s) >= 4 {
		tail = s[len(s)-4:]
	}
	// health 是给面板标红用的派生健康度（#18）。判据用 fail_count：markAccountResult
	// 成功会把它清零、失败才 +1，而且只有 401/403 才算失败（见 markCookieByStatus），
	// 所以 fail_count>0 就是「当前鉴权在失败」= 多半 cookie 过期了，直接标红提示重导。
	//   dead     鉴权失败中（红）—— 不管启用还是被自动停用，只要 fail_count>0
	//   disabled 用户手动停用且没有失败记录（灰）
	//   unknown  还没成功用过/检测过（待检测）
	//   ok       在用、没失败（绿）
	health := "ok"
	hint := ""
	switch {
	case looksLikeStaleSession(errors.New(a.LastError)):
		health = "needs_sync"
		hint = "Phiên cần đồng bộ lại từ trình duyệt; chưa có bằng chứng tài khoản Google bị khóa"
	case a.FailCount > 0:
		health = "dead"
	case a.Status != "enabled":
		health = "disabled"
	case a.LastError != "":
		health = "check_failed"
		hint = "Lần kiểm tra gần đây thất bại; xem lỗi cụ thể trước khi kết luận phiên hết hạn"
	case a.LastOkAt == 0:
		health = "unknown"
	case time.Since(time.Unix(a.LastOkAt, 0)) > 30*time.Minute:
		health = "stale"
		hint = "Kết quả thành công đã cũ; cần kiểm tra hoặc đồng bộ phiên mới"
	}
	return map[string]interface{}{
		"maintenance_mode": map[bool]string{true: "browser", false: "server"}[browserManagedCookie(a)],
		"id":               a.ID,
		"label":            a.Label,
		"status":           a.Status,
		"health":           health,
		"maintenance_hint": hint,
		"note":             a.Note,
		"created_at":       a.CreatedAt,
		"last_used_at":     a.LastUsedAt,
		"last_ok_at":       a.LastOkAt,
		"last_error":       a.LastError,
		"fail_count":       a.FailCount,
		"cookie_count":     len(names),
		"key_cookies":      key,
		"sapisid_tail":     tail,
		"has_1psidts":      nameSet["__Secure-1PSIDTS"],
		"proxy_id":         a.ProxyID,
		"proxy_name":       proxyNameByID(a.ProxyID),
	}
}

func cookieRenewalResult(names []string, loggedIn bool, nextSec int) map[string]interface{} {
	renewed := false
	for _, name := range names {
		if name == "__Secure-1PSIDTS" {
			renewed = true
		}
	}
	detail := "Phiên hiện hợp lệ; lượt này chưa xác nhận gia hạn PSIDTS"
	if !loggedIn {
		detail = "Chưa xác minh được phiên đăng nhập sau gia hạn. Cookie phụ có thể đã đổi nhưng chưa đủ để dùng tài khoản; hãy đăng nhập và đồng bộ lại."
	} else if renewed {
		detail = "Đã gia hạn PSIDTS và xác minh phiên đăng nhập còn hợp lệ"
	}
	return map[string]interface{}{"ok": loggedIn, "login_valid": loggedIn, "renewed": renewed, "refreshed": names, "next_sec": nextSec, "detail": detail}
}

// handleAdminCookies — GET 列出池 / POST 新增一条。
func handleAdminCookies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		accts := accountList()
		items := make([]map[string]interface{}, 0, len(accts))
		for _, a := range accts {
			items = append(items, cookieAcctView(a))
		}
		total, enabled := accountCount()
		writeJSON(w, 200, map[string]interface{}{
			"items": items, "total": total, "enabled": enabled,
		})
	case http.MethodPost:
		var p struct {
			Label  string `json:"label"`
			Cookie string `json:"cookie"`
			Note   string `json:"note"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &p); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		id, err := accountAdd(p.Label, p.Cookie, p.Note)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		logf("[cookies] 新增账号 #%d label=%q", id, strings.TrimSpace(p.Label))
		writeJSON(w, 200, map[string]interface{}{"id": id})
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

// handleAdminCookieItem — /admin/api/cookies/{id}[/toggle]
//
//	DELETE          删除
//	POST .../toggle 翻转 enabled/disabled
//	PATCH           改 label / note / status
func handleAdminCookieItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/api/cookies/")
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || parts[0] == "" {
		writeJSON(w, 404, map[string]string{"error": "missing id"})
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad id"})
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch r.Method {
	case http.MethodDelete:
		if err := accountDelete(id); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		logf("[cookies] 删除账号 #%d", id)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodPost:
		if action == "rotate" {
			for _, a := range accountList() {
				if a.ID == id {
					if browserManagedCookie(a) {
						writeJSON(w, 200, map[string]interface{}{"ok": false, "renewed": false, "requires_browser_sync": true,
							"detail": "Phiên do extension duy trì. Hãy đồng bộ tài khoản trong tiện ích; máy chủ chưa thực hiện gia hạn cookie."})
						return
					}
					iv, names, err := rotateAccount(a)
					if err != nil {
						writeJSON(w, 200, map[string]interface{}{"ok": false, "detail": err.Error()})
						return
					}
					fresh := accountByID(id)
					loggedIn := false
					if fresh != nil {
						proxyURL := ""
						if fresh.ProxyID > 0 {
							proxyURL = proxyURLByID(fresh.ProxyID)
						}
						if page, e := fetchAppPage(fresh.Cookie, proxyURL, fresh.AuthUser); e == nil {
							loggedIn = parsePageIdentity(page).LoggedIn
						}
					}
					writeJSON(w, 200, cookieRenewalResult(names, loggedIn, int(iv.Seconds())))
					return
				}
			}
			writeJSON(w, 404, map[string]string{"error": "account not found"})
			return
		}
		if action == "check" {
			for _, a := range accountList() {
				if a.ID == id {
					writeJSON(w, 200, checkAccountCookie(a))
					return
				}
			}
			writeJSON(w, 404, map[string]string{"error": "account not found"})
			return
		}
		if action != "toggle" {
			writeJSON(w, 400, map[string]string{"error": "unknown action"})
			return
		}
		cur := ""
		for _, a := range accountList() {
			if a.ID == id {
				cur = a.Status
				break
			}
		}
		next := "enabled"
		if cur == "enabled" {
			next = "disabled"
		}
		if err := accountSetStatus(id, next); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]interface{}{"status": next})
	case http.MethodPatch:
		var p struct {
			Label  *string `json:"label"`
			Note   *string `json:"note"`
			Status *string `json:"status"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &p); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		if p.Status != nil {
			if err := accountSetStatus(id, *p.Status); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
		}
		if p.Label != nil || p.Note != nil {
			// 只改传了的字段：没传的沿用当前值。
			var curLabel, curNote string
			for _, a := range accountList() {
				if a.ID == id {
					curLabel, curNote = a.Label, a.Note
					break
				}
			}
			if p.Label != nil {
				curLabel = *p.Label
			}
			if p.Note != nil {
				curNote = *p.Note
			}
			if err := accountUpdateMeta(id, curLabel, curNote); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}
