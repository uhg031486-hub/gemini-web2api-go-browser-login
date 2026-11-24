package app

import "testing"

func TestVerifiedImportTransfersMaintenanceWithoutDuplicate(t *testing.T) {
	email := "ownership@example.test"
	t.Cleanup(func() { getDB().Exec("DELETE FROM accounts WHERE account_email=?", email) })
	id, err := storeRemoteVerifiedAccount("ownership", "test-ownership", "SAPISID=a; __Secure-1PSID=a", email, 0)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := storeRemoteVerifiedAccount("ownership", "test-ownership", "SAPISID=b; __Secure-1PSID=b", email, 1, "server")
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatal("ownership transfer duplicated account")
	}
	a := accountByID(id)
	if a.Source != "manual" || browserManagedCookie(*a) || a.AuthUser != 1 {
		t.Fatal("server ownership not persisted")
	}
	id3, err := storeRemoteVerifiedAccount("ownership", "test-ownership", "SAPISID=c; __Secure-1PSID=c", email, 0, "server")
	if err != nil || id3 != id {
		t.Fatal("reimport of server-maintained identity duplicated account")
	}
	_, err = storeRemoteVerifiedAccount("ownership", "test-ownership", "SAPISID=d; __Secure-1PSID=d", email, 0, "invalid")
	if err == nil {
		t.Fatal("invalid maintenance mode accepted")
	}
	if accountByID(id).Cookie != "SAPISID=c; __Secure-1PSID=c" {
		t.Fatal("invalid mode changed account")
	}
	id4, err := storeRemoteVerifiedAccount("ownership", "test-ownership", "SAPISID=e; __Secure-1PSID=e", email, 0, "browser")
	if err != nil || id4 != id || !browserManagedCookie(*accountByID(id)) {
		t.Fatal("explicit browser ownership regression")
	}
}
