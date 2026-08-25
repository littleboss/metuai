package meeting

import (
	"strings"
	"testing"
)

func TestCreateAndCheckPassword(t *testing.T) {
	s := NewMemoryStore()
	m, plain, err := s.Create("standup", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == "" || m.OrganizerID != "u-1" || m.Locked {
		t.Fatalf("meeting %+v", m)
	}
	if len(plain) != 8 {
		t.Fatalf("password len %d", len(plain))
	}
	if !s.CheckPassword(m.ID, plain) {
		t.Fatal("password should match")
	}
	if s.CheckPassword(m.ID, "wrong") {
		t.Fatal("wrong password should fail")
	}
}

func TestGetMissing(t *testing.T) {
	s := NewMemoryStore()
	if _, ok := s.Get("nope"); ok {
		t.Fatal("expected missing")
	}
}

func TestMemoryStoreAckRecording(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("recorded meeting", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}

	principal := PrincipalKey("employee", "u-1", "")
	if s.HasAck(m.ID, principal) {
		t.Fatal("recording should not be acknowledged before AckRecording")
	}
	if err := s.AckRecording(m.ID, principal); err != nil {
		t.Fatal(err)
	}
	if !s.HasAck(m.ID, principal) {
		t.Fatal("recording acknowledgment was not saved")
	}
}

func TestMemoryStoreAckRecordingMissingMeeting(t *testing.T) {
	s := NewMemoryStore()
	if err := s.AckRecording("missing", "employee:u-1"); err == nil {
		t.Fatal("expected an error for a missing meeting")
	}
}

func TestPrincipalKey(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		userID  string
		guestID string
		want    string
	}{
		{name: "employee", kind: "employee", userID: "u-1", want: "employee:u-1"},
		{name: "guest", kind: "guest", guestID: "g-1", want: "guest:g-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrincipalKey(tt.kind, tt.userID, tt.guestID); got != tt.want {
				t.Fatalf("PrincipalKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRandomIDUsesPrefix(t *testing.T) {
	id, err := RandomID("mtg_")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "mtg_") {
		t.Fatalf("RandomID() = %q, want mtg_ prefix", id)
	}
	if len(id) != len("mtg_")+8 {
		t.Fatalf("RandomID() length = %d, want %d", len(id), len("mtg_")+8)
	}
}
