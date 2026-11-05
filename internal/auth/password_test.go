package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword("correct horse battery", h); err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Fatal("expected mismatch")
	}
	if _, err := VerifyPassword("x", "$bcrypt$nope"); err == nil {
		t.Fatal("expected invalid hash error")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salts must differ")
	}
}

func TestValidatePassword(t *testing.T) {
	if ValidatePassword("short") == nil {
		t.Fatal("short password accepted")
	}
	if ValidatePassword("пароль12") != nil { // 8 runes, multi-byte
		t.Fatal("8-rune password rejected")
	}
}

func TestTokenHash(t *testing.T) {
	tok, hash, err := NewToken()
	if err != nil || len(tok) < 40 {
		t.Fatalf("bad token %q %v", tok, err)
	}
	if string(HashToken(tok)) != string(hash) {
		t.Fatal("hash mismatch")
	}
}
