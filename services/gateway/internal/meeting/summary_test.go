package meeting

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCitedItemUnmarshalAcceptsLegacyString(t *testing.T) {
	var items []CitedItem
	if err := json.Unmarshal([]byte(`["先做假流水线",{"text":"结构化","source_segment_ids":["seg_1"]}]`), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Text != "先做假流水线" || items[1].Text != "结构化" {
		t.Fatalf("got %+v", items)
	}
	if len(items[1].SourceSegmentIDs) != 1 || items[1].SourceSegmentIDs[0] != "seg_1" {
		t.Fatalf("citations %+v", items[1])
	}
}

func TestActionItemUnmarshalAcceptsLegacyString(t *testing.T) {
	var items []ActionItem
	if err := json.Unmarshal([]byte(`["跟进ASR",{"task":"写测试","owner_user_id":"u-1"}]`), &items); err != nil {
		t.Fatal(err)
	}
	if items[0].Task != "跟进ASR" || items[1].OwnerUserID != "u-1" {
		t.Fatalf("got %+v", items)
	}
}

func TestApplySummaryEditKeepsOriginalAndRejectsGuestOwner(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("修订", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSummary(MeetingSummary{
		MeetingID: m.ID,
		Summary:   "原稿",
		Decisions: []CitedItem{{Text: "原决策"}},
		ActionItems: []ActionItem{{
			Task:        "原待办",
			OwnerUserID: "u-1",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetSummary(m.ID)
	if !ok || got.OriginalJSON == "" {
		t.Fatalf("expected original json, got %+v", got)
	}
	_, err = ApplySummaryEdit(s, m.ID, "employee:u-1", MeetingSummary{
		Summary:     "修订后",
		Decisions:   []CitedItem{{Text: "新决策", SourceSegmentIDs: []string{"seg_1"}}},
		ActionItems: []ActionItem{{Task: "外部人负责", OwnerUserID: "guest-x"}},
	})
	if !errors.Is(err, ErrOwnerMustBeInternal) {
		t.Fatalf("want owner_must_be_internal, got %v", err)
	}
	updated, err := ApplySummaryEdit(s, m.ID, "employee:u-1", MeetingSummary{
		Summary:     "修订后",
		Decisions:   []CitedItem{{Text: "新决策", SourceSegmentIDs: []string{"seg_1"}}},
		ActionItems: []ActionItem{{Task: "内部负责", OwnerUserID: "u-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Summary != "修订后" || updated.OriginalJSON != got.OriginalJSON {
		t.Fatalf("original must be preserved: %+v", updated)
	}
	if updated.RevisedAt == nil {
		t.Fatal("expected revised_at")
	}
	revs, err := s.ListSummaryRevisions(m.ID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions %+v err=%v", revs, err)
	}
}

func TestBindTranscriptSpeakersUsesJoinSnapshot(t *testing.T) {
	members := []MeetingMember{
		{UserID: "u-1", DisplayNameSnapshot: "Alice"},
		{UserID: "u-2", DisplayNameSnapshot: "Bob"},
	}
	segs := []TranscriptSegment{
		{SpeakerUserID: "speaker-1", SpeakerDisplayName: "说话人1", Text: "hello"},
		{SpeakerUserID: "speaker-2", SpeakerDisplayName: "说话人2", Text: "world"},
	}
	out := BindTranscriptSpeakers(members, "u-1", segs)
	if out[0].SpeakerUserID != "u-1" || out[0].SpeakerDisplayName != "Alice" {
		t.Fatalf("seg0 %+v", out[0])
	}
	if out[1].SpeakerUserID != "u-2" || out[1].SpeakerDisplayName != "Bob" {
		t.Fatalf("seg1 %+v", out[1])
	}
}

func TestHasAuthoritativeAudioIgnoresRoomMix(t *testing.T) {
	if HasAuthoritativeAudio([]MediaArtifact{{Kind: KindRoomAudio, Status: "ready"}}) {
		t.Fatal("room mix is not authoritative")
	}
	if !HasAuthoritativeAudio([]MediaArtifact{{Kind: KindParticipantTrack, Status: "ready"}}) {
		t.Fatal("participant track should count")
	}
	if !HasAuthoritativeAudio([]MediaArtifact{{Kind: KindLocalMic, Status: "ready"}}) {
		t.Fatal("local mic should count")
	}
}

func TestCompleteActionItem(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("todos", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSummary(MeetingSummary{
		MeetingID:   m.ID,
		Summary:     "有待办",
		ActionItems: []ActionItem{{Task: "写测试", OwnerUserID: "u-1"}},
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := CompleteActionItem(s, m.ID, "employee:u-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ActionItems[0].CompletedAt == nil {
		t.Fatal("expected completed_at")
	}
	if _, err := CompleteActionItem(s, m.ID, "employee:u-1", 0); !errors.Is(err, ErrActionAlreadyDone) {
		t.Fatalf("expected already done, got %v", err)
	}
}
