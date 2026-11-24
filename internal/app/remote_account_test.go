package app

import (
	"fmt"
	"sync"
	"testing"
)

func TestRemoteAccountIdentityPreservesDifferentUsers(t *testing.T) {
	profile := "test-remote-identity"
	t.Cleanup(func() { _, _ = getDB().Exec(`DELETE FROM accounts WHERE profile LIKE 'test-remote-%'`) })
	a, err := storeRemoteVerifiedAccount("remote:"+profile+" alpha@example.test", profile, "SAPISID=a; __Secure-1PSID=a", " Alpha@Example.Test ", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := storeRemoteVerifiedAccount("remote:"+profile+" beta@example.test", profile, "SAPISID=b; __Secure-1PSID=b", "beta@example.test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("different Google users overwrote the same pool row")
	}
	a2, err := storeRemoteVerifiedAccount("alpha@example.test", "test-remote-other", "SAPISID=a2; __Secure-1PSID=a2", "alpha@example.test", 2)
	if err != nil {
		t.Fatal(err)
	}
	if a2 != a {
		t.Fatal("same Google user duplicated when profile/slot changed")
	}
	if got := accountByID(b); got == nil || got.Cookie != "SAPISID=b; __Secure-1PSID=b" {
		t.Fatal("other account changed")
	}
	if got := accountByID(a); got == nil || got.AuthUser != 2 || got.Cookie != "SAPISID=a2; __Secure-1PSID=a2" {
		t.Fatal("refresh did not update the same user")
	}
	// Sharing a Google cookie jar must not collapse two verified account slots.
	c, err := storeRemoteVerifiedAccount("gamma@example.test", profile, "SAPISID=b; __Secure-1PSID=b", "gamma@example.test", 1)
	if err != nil || c == b {
		t.Fatalf("shared jar collapsed: %d %v", c, err)
	}
}

func TestRemoteAccountAdoptsOnlyMatchingLegacyIdentity(t *testing.T) {
	profile := "test-remote-legacy"
	t.Cleanup(func() { _, _ = getDB().Exec(`DELETE FROM accounts WHERE profile=?`, profile) })
	if err := browserStoreCookieSourceAuthUser("remote:"+profile+" old@example.test", profile, "SAPISID=old; __Secure-1PSID=old", "remote", 0); err != nil {
		t.Fatal(err)
	}
	var old int64
	if err := getDB().QueryRow(`SELECT id FROM accounts WHERE profile=?`, profile).Scan(&old); err != nil {
		t.Fatal(err)
	}
	other, err := storeRemoteVerifiedAccount("new@example.test", profile, "SAPISID=new; __Secure-1PSID=new", "new@example.test", 0)
	if err != nil || other == old {
		t.Fatalf("unrelated legacy row replaced: %d %v", other, err)
	}
	same, err := storeRemoteVerifiedAccount("old@example.test", profile, "SAPISID=fresh; __Secure-1PSID=fresh", "old@example.test", 0)
	if err != nil || same != old {
		t.Fatalf("matching legacy identity not preserved: %d %v", same, err)
	}
	if accountByID(other) == nil {
		t.Fatal("new account removed while adopting legacy")
	}
}

func TestRemoteAccountRejectsInvalidIdentityAndSerializesRefresh(t *testing.T) {
	profile := "test-remote-concurrent"
	t.Cleanup(func() { _, _ = getDB().Exec(`DELETE FROM accounts WHERE profile=?`, profile) })
	for _, v := range []struct {
		email, cookie string
		slot          int
	}{
		{"", "SAPISID=x; __Secure-1PSID=x", 0}, {"bad", "SAPISID=x; __Secure-1PSID=x", 0},
		{"ok@example.test", "", 0}, {"ok@example.test", "SAPISID=x; __Secure-1PSID=x", -1},
		{"ok@example.test", "SAPISID=x; __Secure-1PSID=x", 10},
	} {
		if _, err := storeRemoteVerifiedAccount("fixture", profile, v.cookie, v.email, v.slot); err == nil {
			t.Fatalf("accepted invalid fixture %+v", v)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := storeRemoteVerifiedAccount("parallel@example.test", profile, fmt.Sprintf("SAPISID=%d; __Secure-1PSID=x", i), "parallel@example.test", 0)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := getDB().QueryRow(`SELECT count(*) FROM accounts WHERE profile=?`, profile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("concurrent refresh created %d rows", n)
	}
}
