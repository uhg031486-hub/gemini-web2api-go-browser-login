package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteKeepaliveReportsBrowserOwnership(t *testing.T) {
	id, err := accountAdd("maintenance-fixture", "SAPISID=fixture; __Secure-1PSID=fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountDelete(id) })
	if _, err := getDB().Exec(`UPDATE accounts SET source='remote' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleAdminCookieItem(w, httptest.NewRequest("POST", fmt.Sprintf("/admin/api/cookies/%d/rotate", id), nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != false || body["requires_browser_sync"] != true {
		t.Fatalf("no-op reported as renewal: %v", body)
	}
}

func TestSessionHealthDistinguishesStaleFromDead(t *testing.T) {
	noFailureCount := cookieAcctView(CookieAccount{Source: "manual", Status: "enabled", LastOkAt: time.Now().Unix(), LastError: "no SNlM0e in page (cookie expired or not signed in)"})
	if noFailureCount["health"] != "needs_sync" {
		t.Fatal("confirmed expired session hidden by zero fail count")
	}
	if noFailureCount["maintenance_mode"] != "server" {
		t.Fatal("missing server renewal ownership")
	}
	failedCheck := cookieAcctView(CookieAccount{Status: "enabled", LastError: "fetch /app: HTTP 302"})
	if failedCheck["health"] != "check_failed" {
		t.Fatal("failed check must not be called merely unchecked")
	}
	stale := cookieAcctView(CookieAccount{Status: "enabled", LastOkAt: time.Now().Add(-time.Hour).Unix()})
	if stale["health"] != "stale" {
		t.Fatalf("historical success still green: %v", stale["health"])
	}
	expired := cookieAcctView(CookieAccount{Status: "enabled", FailCount: 1, LastError: "no SNlM0e in page (cookie expired or not signed in)"})
	if expired["health"] != "needs_sync" {
		t.Fatalf("stale browser cookie is called dead: %v", expired["health"])
	}
	if !strings.Contains(fmt.Sprint(expired["maintenance_hint"]), "đồng bộ") {
		t.Fatal("missing actionable browser renewal hint")
	}
}

func TestRenewalDoesNotClaimValidSessionFromAuxiliaryCookie(t *testing.T) {
	failed := cookieRenewalResult([]string{"SIDCC", "__Secure-1PSIDCC"}, false, 600)
	if failed["ok"] != false || failed["login_valid"] != false || failed["renewed"] != false {
		t.Fatal("auxiliary cookie falsely reported login renewal")
	}
	valid := cookieRenewalResult([]string{"__Secure-1PSIDTS"}, true, 600)
	if valid["ok"] != true || valid["login_valid"] != true || valid["renewed"] != true {
		t.Fatal("verified primary session renewal rejected")
	}
	stillValid := cookieRenewalResult(nil, true, 600)
	if stillValid["ok"] != true || stillValid["renewed"] != false {
		t.Fatal("valid session must not imply a newly issued ticket")
	}
}
