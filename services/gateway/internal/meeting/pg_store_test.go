package meeting

import (
	"context"
	"os"
	"testing"
)

func TestPGStoreMeetingLifecycle(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	s, err := NewPGStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Close()

	m, plain, err := s.Create("pg-meet", "u-9", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(m.ID)
	if !ok || got.OrganizerID != "u-9" {
		t.Fatalf("Get() = %+v, %v; want organizer u-9", got, ok)
	}
	if !s.CheckPassword(m.ID, plain) {
		t.Fatal("generated password should match")
	}
	if s.CheckPassword(m.ID, "nope") {
		t.Fatal("incorrect password should not match")
	}

	if err := s.SetLocked(m.ID, true); err != nil {
		t.Fatal(err)
	}
	got, ok = s.Get(m.ID)
	if !ok || !got.Locked {
		t.Fatalf("Get() after SetLocked() = %+v, %v; want locked meeting", got, ok)
	}

	principal := PrincipalKey("employee", "u-9", "")
	if s.HasAck(m.ID, principal) {
		t.Fatal("recording should not be acknowledged initially")
	}
	if err := s.AckRecording(m.ID, principal); err != nil {
		t.Fatal(err)
	}
	if !s.HasAck(m.ID, principal) {
		t.Fatal("recording acknowledgment was not persisted")
	}
}

func TestPGStoreSetLockedMissingMeeting(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	s, err := NewPGStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Close()

	if err := s.SetLocked("missing", true); err == nil {
		t.Fatal("expected an error for a missing meeting")
	}
}
