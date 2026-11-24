package app

import (
	"database/sql"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"
)

var remoteAccountStoreMu sync.Mutex

// Only call after verifySlotIdentity: email is the Google identity, while
// profile and authuser describe where the latest verified session came from.
// A second /u/0 in the same extension profile must never replace another user.
func storeRemoteVerifiedAccount(label, profile, cookie, email string, authuser int, maintenanceMode ...string) (int64, error) {
	source := "remote"
	if len(maintenanceMode) > 0 {
		switch maintenanceMode[0] {
		case "", "browser":
		case "server":
			source = "manual"
		default:
			return 0, fmt.Errorf("Chế độ duy trì phiên không hợp lệ")
		}
	}
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return 0, fmt.Errorf("Không xác minh được email tài khoản; chưa thay đổi pool")
	}
	if authuser < 0 || authuser > 9 || extractSAPISID(cookie) == "" || cookieValue(cookie, "__Secure-1PSID") == "" {
		return 0, fmt.Errorf("Phiên hoặc chỉ số tài khoản không hợp lệ; chưa thay đổi pool")
	}
	profile = sanitizeProfileName(profile)
	if strings.TrimSpace(label) == "" {
		label = "remote:" + profile + " " + email
	}
	remoteAccountStoreMu.Lock()
	defer remoteAccountStoreMu.Unlock()
	tx, err := getDB().Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`SELECT id FROM accounts WHERE source IN ('remote','manual') AND account_email=? ORDER BY id LIMIT 1`, email).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if err == sql.ErrNoRows {
		// Adopt an old row only when its auto-generated label identifies this exact
		// verified user. Unknown labels are preserved, never guessed from /u/N.
		err = tx.QueryRow(`SELECT id FROM accounts WHERE source='remote' AND account_email='' AND profile=?
   AND (LOWER(TRIM(label))=? OR LOWER(TRIM(label))=?) ORDER BY id LIMIT 1`,
			profile, email, "remote:"+strings.ToLower(profile)+" "+email).Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
	}
	now := time.Now().Unix()
	note := "Nhập từ extension; danh tính Google đã xác minh; trình duyệt duy trì"
	if source == "manual" {
		note = "Nhập từ extension; danh tính Google đã xác minh; VPS tự duy trì"
	}
	if id > 0 {
		_, err = tx.Exec(`UPDATE accounts SET label=?,cookie=?,profile=?,authuser=?,account_email=?,source=?,note=?,last_ok_at=?,last_error='',fail_count=0 WHERE id=?`,
			strings.TrimSpace(label), cookie, profile, authuser, email, source, note, now, id)
	} else {
		q := `INSERT INTO accounts(label,cookie,status,note,created_at,last_ok_at,source,profile,authuser,account_email) VALUES (?,?,'enabled',?,?,?,?,?,?,?)`
		args := []any{strings.TrimSpace(label), cookie, note, now, now, source, profile, authuser, email}
		if curDialect == dialectPostgres {
			err = tx.QueryRow(q+" RETURNING id", args...).Scan(&id)
		} else {
			var result sql.Result
			result, err = tx.Exec(q, args...)
			if err == nil {
				id, err = result.LastInsertId()
			}
		}
	}
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
