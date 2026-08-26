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
	if !strings.HasPrefix(m.PasswordHash, "$2") {
		t.Fatalf("password should use an adaptive salted hash, got %q", m.PasswordHash)
	}
	if s.CheckPassword(m.ID, "wrong") {
		t.Fatal("wrong password should fail")
	}
}

func TestLegacyPasswordHashStillMatches(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("legacy", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	m.PasswordHash = legacyPasswordHash("old-password")
	s.meetings[m.ID] = m
	if !s.CheckPassword(m.ID, "old-password") || s.CheckPassword(m.ID, "wrong") {
		t.Fatal("legacy SHA-256 hash compatibility failed")
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

func TestMemoryStoreGuestPresenceKeepsDisplayName(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("guest names", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertGuestPresence(m.ID, "gst_1", "Bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.AckRecording(m.ID, PrincipalKey("guest", "", "gst_1")); err != nil {
		t.Fatal(err)
	}
	guests, err := s.ListGuestParticipants(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(guests) != 1 || guests[0].GuestID != "gst_1" || guests[0].DisplayName != "Bob" {
		t.Fatalf("guest presence %+v", guests)
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

func TestMemoryStoreEndAndKickAndChat(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("lifecycle", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Kick(m.ID, "guest:g-1"); err != nil {
		t.Fatal(err)
	}
	if !s.IsKicked(m.ID, "guest:g-1") {
		t.Fatal("expected kicked identity")
	}
	if err := s.Kick(m.ID, "u-2~phone"); err != nil {
		t.Fatal(err)
	}
	if !s.IsKicked(m.ID, "u-2") || !s.IsKicked(m.ID, "u-2~watch") {
		t.Fatal("device suffix kick should apply to the whole user")
	}
	if err := s.AddShare(m.ID, "Reader@Example.com", "u-1"); err != nil {
		t.Fatal(err)
	}
	if !s.HasShare(m.ID, "reader@example.com") {
		t.Fatal("expected share")
	}
	if err := s.AddGuestEmailSource(m.ID, "reader@example.com", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveShare(m.ID, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	emails, err := s.ListGuestEmails(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 0 {
		t.Fatalf("shared email should be gone, got %v", emails)
	}
	msg, err := s.AddChat(ChatMessage{MeetingID: m.ID, SenderKey: "employee:u-1", DisplayName: "Alice", Body: "hi"})
	if err != nil || msg.ID == "" {
		t.Fatalf("AddChat = %+v, %v", msg, err)
	}
	list, err := s.ListChat(m.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListChat = %+v, %v", list, err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(m.ID)
	if !ok || !got.Ended || got.EndedAt == nil {
		t.Fatalf("ended meeting = %+v", got)
	}
}
