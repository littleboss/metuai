package livekit

import (
	"testing"

	"github.com/livekit/protocol/auth"
)

func TestIssueRoomToken_Decodable(t *testing.T) {
	s, err := IssueRoomToken("devkey", "secret", "mtg_1", "u-1", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if s == "" {
		t.Fatal("empty token")
	}
	v, err := auth.ParseAPIToken(s)
	if err != nil {
		t.Fatal(err)
	}
	if id := v.Identity(); id != "u-1" {
		t.Fatalf("identity %q", id)
	}
}
