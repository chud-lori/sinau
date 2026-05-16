package auth

import "testing"

func TestPasswordHashVerify(t *testing.T) {
	hash, err := HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("verysecurepass123", hash) {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword("wrongpass", hash) {
		t.Fatal("wrong password verified")
	}
}

func TestTokenHashIsStableAndOpaque(t *testing.T) {
	token, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty token")
	}
	if HashToken(token) != HashToken(token) {
		t.Fatal("hash is not stable")
	}
	if HashToken(token) == token {
		t.Fatal("hash should not equal raw token")
	}
}

func TestValidExternalURL(t *testing.T) {
	valid := []string{
		"https://docs.google.com/document/d/example",
		"https://drive.google.com/file/d/example",
		"http://localhost:9999/note",
	}
	for _, u := range valid {
		if !ValidExternalURL(u) {
			t.Fatalf("expected valid URL: %s", u)
		}
	}
	invalid := []string{"javascript:alert(1)", "file:///etc/passwd", "https://", "docs.google.com/a"}
	for _, u := range invalid {
		if ValidExternalURL(u) {
			t.Fatalf("expected invalid URL: %s", u)
		}
	}
}
