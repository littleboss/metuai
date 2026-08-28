package meeting

import (
	"testing"
	"time"
)

func TestParseScheduleTimes(t *testing.T) {
	start := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339)

	s, e, err := parseScheduleTimes(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || e == nil {
		t.Fatal("expected both times")
	}
	if !e.After(*s) {
		t.Fatal("ends_at must be after starts_at")
	}

	if _, _, err := parseScheduleTimes("", end); err == nil {
		t.Fatal("ends_at without starts_at should fail")
	}
}
