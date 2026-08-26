package knowledge

import (
	"context"
	"testing"
)

func TestMemoryIndexACLBlocksOtherUsers(t *testing.T) {
	idx := NewMemoryIndex()
	ctx := context.Background()
	_ = idx.Upsert(ctx, Document{
		MeetingID:      "m1",
		Title:          "预算会",
		Text:           "讨论明年预算与招聘",
		SourceType:     "summary",
		AllowedUserIDs: []string{"u-org"},
	})

	hits, err := idx.Search(ctx, "预算", "u-other", "", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("other user should see 0 hits, got %d", len(hits))
	}

	hits, err = idx.Search(ctx, "预算", "u-org", "", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("organizer should see 1 hit, got %d", len(hits))
	}
}

func TestMemoryIndexGuestEmailACL(t *testing.T) {
	idx := NewMemoryIndex()
	ctx := context.Background()
	_ = idx.Upsert(ctx, Document{
		MeetingID:          "m2",
		Title:              "客户会",
		Text:               "产品演示",
		SourceType:         "transcript",
		AllowedUserIDs:     []string{"u-org"},
		AllowedGuestEmails: []string{"guest@example.com"},
	})

	hits, err := idx.Search(ctx, "演示", "", "guest@example.com", SearchOpts{AllowedMeetingIDs: []string{"m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("guest email should match, got %d", len(hits))
	}

	hits, err = idx.Search(ctx, "演示", "", "other@example.com", SearchOpts{AllowedMeetingIDs: []string{"m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("wrong guest email blocked, got %d", len(hits))
	}
}

func TestMemoryIndexMeetingAllowList(t *testing.T) {
	idx := NewMemoryIndex()
	ctx := context.Background()
	_ = idx.Upsert(ctx, Document{
		MeetingID: "m-a", Title: "A", Text: "alpha", SourceType: "summary", AllowedUserIDs: []string{"u"},
	})
	_ = idx.Upsert(ctx, Document{
		MeetingID: "m-b", Title: "B", Text: "alpha", SourceType: "summary", AllowedUserIDs: []string{"u"},
	})

	hits, err := idx.Search(ctx, "alpha", "u", "", SearchOpts{AllowedMeetingIDs: []string{"m-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Document.MeetingID != "m-a" {
		t.Fatalf("allow-list should constrain meetings: %+v", hits)
	}
}

func TestMemoryIndexDeleteMeeting(t *testing.T) {
	idx := NewMemoryIndex()
	ctx := context.Background()
	_ = idx.Upsert(ctx, Document{MeetingID: "m", Text: "x", SourceType: "summary", AllowedUserIDs: []string{"u"}})
	_ = idx.Upsert(ctx, Document{MeetingID: "m", Text: "y", SourceType: "transcript", AllowedUserIDs: []string{"u"}})
	_ = idx.DeleteMeeting(ctx, "m")
	hits, _ := idx.Search(ctx, "", "u", "", SearchOpts{})
	if len(hits) != 0 {
		t.Fatalf("expected empty after delete, got %d", len(hits))
	}
}

func TestMemoryIndexBreakGlassElevation(t *testing.T) {
	idx := NewMemoryIndex()
	ctx := context.Background()
	_ = idx.Upsert(ctx, Document{
		MeetingID: "m-secret", Title: "机密", Text: "破窗可搜到", SourceType: "summary", AllowedUserIDs: []string{"u-org"},
	})
	hits, _ := idx.Search(ctx, "破窗", "u-audit", "", SearchOpts{})
	if len(hits) != 0 {
		t.Fatal("without elevation must be empty")
	}
	hits, _ = idx.Search(ctx, "破窗", "u-audit", "", SearchOpts{ElevatedMeetingIDs: []string{"m-secret"}})
	if len(hits) != 1 {
		t.Fatalf("elevated user should see hit, got %d", len(hits))
	}
}
