package meeting

import (
	"context"
	"testing"
	"time"

	"metuai/services/gateway/internal/knowledge"
)

func TestSweepRetentionSeparatesVideoMediaAndKnowledge(t *testing.T) {
	s := NewMemoryStore()
	idx := knowledge.NewMemoryIndex()
	m, _, err := s.Create("retain", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.meetings[m.ID]
	ended := time.Now().UTC().Add(-48 * time.Hour)
	got.EndedAt = &ended
	s.meetings[m.ID] = got
	s.mu.Unlock()

	if _, err := s.AddMediaArtifact(MediaArtifact{MeetingID: m.ID, Kind: KindRoomVideo, Status: "ready", ObjectKey: "video.mp4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMediaArtifact(MediaArtifact{MeetingID: m.ID, Kind: KindRoomAudio, Status: "ready", ObjectKey: "audio.ogg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSummary(MeetingSummary{MeetingID: m.ID, Summary: "纪要正文", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Upsert(context.Background(), knowledge.Document{
		MeetingID: m.ID, Text: "纪要正文", SourceType: "summary", AllowedUserIDs: []string{"u-1"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetRetentionPolicy(RetentionPolicy{
		MediaTTLSeconds:     int64((72 * time.Hour) / time.Second),
		VideoTTLSeconds:     int64((24 * time.Hour) / time.Second),
		KnowledgeTTLSeconds: int64((96 * time.Hour) / time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := SweepRetention(context.Background(), s, idx, nil); err != nil {
		t.Fatal(err)
	}
	arts, _ := s.ListMediaArtifacts(m.ID)
	byKind := map[string]MediaArtifact{}
	for _, a := range arts {
		byKind[a.Kind] = a
	}
	if byKind[KindRoomVideo].Status != "purged" {
		t.Fatalf("video should expire first: %+v", byKind[KindRoomVideo])
	}
	if byKind[KindRoomAudio].Status == "purged" {
		t.Fatalf("audio should still be within media clock: %+v", byKind[KindRoomAudio])
	}
	if _, ok := s.GetSummary(m.ID); !ok {
		t.Fatal("knowledge should still be present")
	}

	s.mu.Lock()
	got = s.meetings[m.ID]
	ended = time.Now().UTC().Add(-100 * time.Hour)
	got.EndedAt = &ended
	s.meetings[m.ID] = got
	s.mu.Unlock()
	if err := SweepRetention(context.Background(), s, idx, nil); err != nil {
		t.Fatal(err)
	}
	arts, _ = s.ListMediaArtifacts(m.ID)
	for _, a := range arts {
		if a.Status != "purged" {
			t.Fatalf("all media should purge: %+v", a)
		}
	}
	if _, ok := s.GetSummary(m.ID); ok {
		t.Fatal("knowledge should be purged")
	}
	hits, err := idx.Search(context.Background(), "纪要", "u-1", "", knowledge.SearchOpts{AllowedMeetingIDs: []string{m.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("index leftover: %+v", hits)
	}
}

func TestRetentionHTTPRequiresSystemAdmin(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	user := employeeJWT(t, secretEmp)
	if got := doJSON(t, r, "GET", "/v1/retention", user, ""); got.Code != 403 {
		t.Fatalf("plain employee GET %d %s", got.Code, got.Body.String())
	}
	admin := employeeJWTForRoles(t, secretEmp, "u-sys", "Sys", "system_admin")
	get := doJSON(t, r, "GET", "/v1/retention", admin, "")
	if get.Code != 200 {
		t.Fatalf("admin GET %d %s", get.Code, get.Body.String())
	}
	put := doJSON(t, r, "PUT", "/v1/retention", admin, `{"media_ttl_seconds":3600,"video_ttl_seconds":600,"knowledge_ttl_seconds":7200}`)
	if put.Code != 200 {
		t.Fatalf("admin PUT %d %s", put.Code, put.Body.String())
	}
}
