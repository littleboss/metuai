package identity

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func jwtEmployee(t *testing.T, secret []byte) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "u-1",
		"kind": KindEmployee,
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGuestSession_RoundTrip(t *testing.T) {
	secret := []byte("guest-secret")
	in := Principal{
		Kind:        KindGuest,
		GuestID:     "gst_1",
		MeetingID:   "mtg_1",
		DisplayName: "Bob",
		Email:       "bob@example.com",
	}
	s, err := IssueGuestSession(in, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseGuestSession(s, secret)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != KindGuest || out.GuestID != "gst_1" || out.MeetingID != "mtg_1" || out.DisplayName != "Bob" || out.Email != "bob@example.com" {
		t.Fatalf("got %+v", out)
	}
}

func TestParseGuestSession_RejectsEmployeeToken(t *testing.T) {
	secret := []byte("guest-secret")
	emp := jwtEmployee(t, secret)
	if _, err := ParseGuestSession(emp, secret); err == nil {
		t.Fatal("expected error")
	}
}
