package meeting

import (
	"testing"
)

func TestApplyASRResultRequiresEndedMeeting(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("asr", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyASRResult(s, m.ID, "employee:u-1", "stub", []ASRResultInput{{
		Text: "hello", Source: "egress", StartMs: 0, EndMs: 1000,
	}})
	if err == nil || err.Error() != "meeting_not_ended" {
		t.Fatalf("want meeting_not_ended, got %v", err)
	}
}

func TestApplyASRResultRejectsBadSource(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("asr", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	_, err = ApplyASRResult(s, m.ID, "employee:u-1", "stub", []ASRResultInput{{
		Text: "hello", Source: "webcam", StartMs: 0, EndMs: 1000,
	}})
	if err == nil || err.Error() != "invalid_source_at_0" {
		t.Fatalf("want invalid_source_at_0, got %v", err)
	}
}

func TestApplyASRResultWritesTranscriptAndStage(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("asr", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	conf := 0.91
	stage, err := ApplyASRResult(s, m.ID, "employee:u-1", "stub", []ASRResultInput{
		{
			TrackID: "t1", SpeakerUserID: "u-1", SpeakerDisplayName: "Alice",
			Language: "zh-CN", StartMs: 0, EndMs: 1200, Text: "大家好",
			ASRModel: "stub-asr", Source: "local_fallback", Confidence: &conf,
		},
		{
			TrackID: "t2", SpeakerUserID: "u-2", SpeakerDisplayName: "Bob",
			StartMs: 1500, EndMs: 3000, Text: "开始吧",
			ASRModel: "stub-asr", Source: "egress",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stage != StageTranscriptReady {
		t.Fatalf("stage=%s", stage)
	}
	current, ok := s.Get(m.ID)
	if !ok || current.PipelineStage != StageTranscriptReady {
		t.Fatalf("pipeline stage wrong: %+v", current)
	}
	segs, err := s.ListTranscript(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %+v", segs)
	}
	if segs[0].Source != "local_fallback" || segs[0].Text != "大家好" {
		t.Fatalf("seg0 %+v", segs[0])
	}
	if segs[0].Confidence == nil || *segs[0].Confidence != conf {
		t.Fatalf("confidence %+v", segs[0].Confidence)
	}
	events, _ := s.ListAudit(m.ID)
	var saw bool
	for _, e := range events {
		if e.Action == "asr_result_applied" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected asr_result_applied audit")
	}
}

func TestApplyASRResultDetectsLanguageWhenMissing(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("asr-lang", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyASRResult(s, m.ID, "employee:u-1", "stub", []ASRResultInput{{
		Text: "Hello everyone, thanks for joining the review.", Source: "egress", StartMs: 0, EndMs: 1000,
	}}); err != nil {
		t.Fatal(err)
	}
	segs, err := s.ListTranscript(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Language != "en" {
		t.Fatalf("want detected en, got %+v", segs)
	}
}

func TestRunASRStubUsesLocalFallbackWhenMicReady(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("asr-stub", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddMediaArtifact(MediaArtifact{
		MeetingID: m.ID,
		Kind:      KindLocalMic,
		Status:    "ready",
		ObjectKey: "metuai-media/local-recording/x/merged.bin",
	})
	stage, err := RunASRStub(s, m.ID, "employee:u-1")
	if err != nil {
		t.Fatal(err)
	}
	if stage != StageTranscriptReady {
		t.Fatalf("stage=%s", stage)
	}
	segs, _ := s.ListTranscript(m.ID)
	if len(segs) < 1 || segs[0].Source != "local_fallback" {
		t.Fatalf("want local_fallback segments, got %+v", segs)
	}
}

func TestRunASRStubMarksManualReviewWithoutAuthoritativeAudio(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("asr-stub", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddMediaArtifact(MediaArtifact{
		MeetingID: m.ID,
		Kind:      KindRoomAudio,
		Status:    "ready",
		ObjectKey: "metuai-media/room.ogg",
	})
	stage, err := RunASRStub(s, m.ID, "employee:u-1")
	if err != nil {
		t.Fatal(err)
	}
	if stage != StageManualReview {
		t.Fatalf("stage=%s", stage)
	}
	current, _ := s.Get(m.ID)
	if current.PipelineStage != StageManualReview {
		t.Fatalf("pipeline %+v", current)
	}
}
