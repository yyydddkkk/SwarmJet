package api

import "testing"

func TestAccountPasswordHash(t *testing.T) {
	hash, err := hashAccountPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyAccountPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if verifyAccountPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
	for _, invalid := range []string{"", "$argon2id$v=19$m=4294967295,t=3,p=2$a$b", "$argon2id$v=19$m=65536,t=3,p=2$bad$bad"} {
		if verifyAccountPassword(invalid, "password") {
			t.Fatalf("malformed hash accepted: %q", invalid)
		}
	}
}
