package meeting

import (
	"testing"
	"time"
)

func TestEnqueueClaimRetryAndDeadLetter(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("tasks", "u-1", "password", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.End(m.ID); err != nil {
		t.Fatal(err)
	}
	first, err := EnqueuePipelineTask(s, m.ID, PipelineKindFake)
	if err != nil || first.Status != PipelineTaskQueued {
		t.Fatalf("enqueue = %+v err=%v", first, err)
	}
	again, err := EnqueuePipelineTask(s, m.ID, PipelineKindFake)
	if err != nil || again.ID != first.ID {
		t.Fatalf("duplicate enqueue should reuse %s, got %+v", first.ID, again)
	}

	claimed, err := s.ClaimPipelineTasks("worker-a", PipelineKindFake, 1)
	if err != nil || len(claimed) != 1 || claimed[0].Status != PipelineTaskLeased {
		t.Fatalf("claim = %+v err=%v", claimed, err)
	}
	empty, err := s.ClaimPipelineTasks("worker-b", PipelineKindFake, 1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("second claim should be empty, got %+v err=%v", empty, err)
	}

	failed, err := FailPipelineTask(s, claimed[0].ID, "boom")
	if err != nil || failed.Status != PipelineTaskFailed || failed.Attempts != 1 {
		t.Fatalf("fail = %+v err=%v", failed, err)
	}
	got, ok := s.Get(m.ID)
	if !ok || got.PipelineStage != StageRetryableError {
		t.Fatalf("stage after fail = %+v", got)
	}

	retried, err := RetryPipeline(s, m.ID)
	if err != nil || retried.Status != PipelineTaskQueued || retried.Attempts != 0 {
		t.Fatalf("retry = %+v err=%v", retried, err)
	}
	got, _ = s.Get(m.ID)
	if got.PipelineStage != StageRecordingFinalized {
		t.Fatalf("retry stage = %s", got.PipelineStage)
	}

	deadTask := retried
	deadTask.Attempts = pipelineTaskMaxAttempts - 1
	deadTask.Status = PipelineTaskLeased
	if err := s.UpdatePipelineTask(deadTask); err != nil {
		t.Fatal(err)
	}
	dead, err := FailPipelineTask(s, deadTask.ID, "still boom")
	if err != nil || dead.Status != PipelineTaskDead {
		t.Fatalf("dead = %+v err=%v", dead, err)
	}
	got, _ = s.Get(m.ID)
	if got.PipelineStage != StageManualReview {
		t.Fatalf("dead stage = %s", got.PipelineStage)
	}

	done, err := CompletePipelineTask(s, dead.ID)
	if err != nil || done.Status != PipelineTaskDead {
		t.Fatalf("complete must not revive dead letter: %+v err=%v", done, err)
	}
}

func TestMergeGuestIdentityRewritesTranscriptAndMedia(t *testing.T) {
	s := NewMemoryStore()
	m1, _, err := s.Create("one", "u-1", "password", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m2, _, err := s.Create("two", "u-1", "password", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTranscript(m1.ID, []TranscriptSegment{{
		ID: "seg_1", MeetingID: m1.ID, SpeakerUserID: "g-temp", SpeakerDisplayName: "Bob", Text: "hello",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMediaArtifact(MediaArtifact{
		MeetingID: m1.ID, Kind: KindLocalMic, Status: "ready", ParticipantKey: "guest:g-temp",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTranscript(m2.ID, []TranscriptSegment{{
		ID: "seg_2", MeetingID: m2.ID, SpeakerUserID: "guest:g-temp", Text: "later",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGuestEmail(m1.ID, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddGuestEmail(m2.ID, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGuestEmailChallenge(GuestEmailChallenge{
		MeetingID: m1.ID, GuestID: "g-temp", Email: "bob@example.com",
		CodeHash: "x", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if err := MergeGuestIdentity(s, m1.ID, "g-temp", "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	segs, err := s.ListTranscript(m1.ID)
	if err != nil || len(segs) != 1 || segs[0].SpeakerUserID != "email:bob@example.com" {
		t.Fatalf("meeting 1 speakers = %+v err=%v", segs, err)
	}
	arts, err := s.ListMediaArtifacts(m1.ID)
	if err != nil || len(arts) != 1 || arts[0].ParticipantKey != "email:bob@example.com" {
		t.Fatalf("media key = %+v err=%v", arts, err)
	}
	segs2, err := s.ListTranscript(m2.ID)
	if err != nil || len(segs2) != 1 || segs2[0].SpeakerUserID != "email:bob@example.com" {
		t.Fatalf("meeting 2 speakers = %+v err=%v", segs2, err)
	}
}

func TestListDirectoryEmployees(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("dir", "u-1", "password", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMembers(m.ID, []MeetingMember{{
		UserID: "u-9", Role: MeetingMemberInvited, DisplayNameSnapshot: "Ada",
	}}); err != nil {
		t.Fatal(err)
	}
	people, err := ListDirectoryEmployees(s, "u-1", "ada")
	if err != nil || len(people) != 1 || people[0].UserID != "u-9" {
		t.Fatalf("directory = %+v err=%v", people, err)
	}
}
