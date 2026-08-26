package identity

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseEmployeeToken_OK(t *testing.T) {
	secret := []byte("test-secret")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-1",
		"kind":         KindEmployee,
		"email":        "a@corp.local",
		"display_name": "Alice",
		"roles":        []any{"organizer"},
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseEmployeeToken(s, secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "u-1" || p.Kind != KindEmployee || p.Email != "a@corp.local" || p.DisplayName != "Alice" {
		t.Fatalf("got %+v", p)
	}
	if !p.HasRole("organizer") || p.HasRole("audit_admin") {
		t.Fatalf("roles not parsed: %+v", p.Roles)
	}
}

func TestParseEmployeeToken_RejectsGuestKind(t *testing.T) {
	secret := []byte("test-secret")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "g-1",
		"kind": KindGuest,
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	s, _ := tok.SignedString(secret)
	if _, err := ParseEmployeeToken(s, secret); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseEmployeeToken_RejectsWrongSecret(t *testing.T) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "u-1",
		"kind": KindEmployee,
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	s, _ := tok.SignedString([]byte("a"))
	if _, err := ParseEmployeeToken(s, []byte("b")); err == nil {
		t.Fatal("expected error")
	}
}
