package meeting

import "testing"

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
