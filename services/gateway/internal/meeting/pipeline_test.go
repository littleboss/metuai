package meeting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"metuai/services/gateway/internal/egress"
	"metuai/services/gateway/internal/knowledge"
)

func TestRunFakePipelineProducesTranscriptAndSummary(t *testing.T) {
	stubPrivateLLM(t, "")
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)

	_, _ = store.AddChat(ChatMessage{
		MeetingID: id, SenderKey: "employee:u-1", DisplayName: "Alice", Body: "先做假流水线",
	})

	end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, "")
	if end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}

	run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", emp, "")
	if run.Code != http.StatusOK {
		t.Fatalf("run-fake %d %s", run.Code, run.Body.String())
	}
	var stageBody struct {
		Stage string `json:"pipeline_stage"`
	}
	_ = json.Unmarshal(run.Body.Bytes(), &stageBody)
	if stageBody.Stage != StageReady {
		t.Fatalf("want READY got %q", stageBody.Stage)
	}

	tr := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/transcript", emp, "")
	if tr.Code != http.StatusOK {
		t.Fatalf("transcript %d %s", tr.Code, tr.Body.String())
	}
	var trBody struct {
		Segments []TranscriptSegment `json:"segments"`
	}
	_ = json.Unmarshal(tr.Body.Bytes(), &trBody)
	if len(trBody.Segments) < 2 {
		t.Fatalf("expected fake segments, got %+v", trBody.Segments)
	}

	sum := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
	if sum.Code != http.StatusOK {
		t.Fatalf("summary %d %s", sum.Code, sum.Body.String())
	}

	media := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", emp, "")
	var mediaBody struct {
		Artifacts []MediaArtifact `json:"artifacts"`
	}
	_ = json.Unmarshal(media.Body.Bytes(), &mediaBody)
	for _, a := range mediaBody.Artifacts {
		if a.Status != "ready" {
			t.Fatalf("media should be ready after fake pipeline, got %+v", a)
		}
	}
}

// 接线 Egress 后，媒体不再只有 pending 一种非 ready 状态：
// 真实录制留下的 started / failed 也必须被假流水线推进，否则 PoC 会卡在 MEDIA_READY。
func TestRunFakePipelinePromotesStartedAndFailedMedia(t *testing.T) {
	stubPrivateLLM(t, "")
	s := NewMemoryStore()
	id := newMeeting(t, s)
	for _, a := range []MediaArtifact{
		{MeetingID: id, Kind: egress.KindRoomAudio, Status: egress.OutcomeStarted, EgressID: "eg_a"},
		{MeetingID: id, Kind: egress.KindRoomVideo, Status: egress.OutcomeFailed},
		{MeetingID: id, Kind: egress.KindParticipantTrack, Status: egress.OutcomePending},
	} {
		if _, err := s.AddMediaArtifact(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.End(id); err != nil {
		t.Fatal(err)
	}

	stage, err := RunFakePipeline(s, id, nil)
	if err != nil {
		t.Fatalf("run fake pipeline: %v", err)
	}
	if stage != StageReady {
		t.Fatalf("want %s got %q", StageReady, stage)
	}

	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 3 {
		t.Fatalf("want 3 artifacts, got %+v", list)
	}
	for _, a := range list {
		if a.Status != "ready" {
			t.Fatalf("fake pipeline must promote %s to ready: %+v", a.Kind, a)
		}
	}
}

func TestRunFakePipelineMarksLocalFallbackWhenLocalMicReady(t *testing.T) {
	stubPrivateLLM(t, "")
	s := NewMemoryStore()
	id := newMeeting(t, s)
	if _, err := s.AddMediaArtifact(MediaArtifact{
		MeetingID: id,
		Kind:      KindLocalMic,
		Status:    "ready",
		ObjectKey: "local-recording/" + id + "/u-1/up_1/merged.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.End(id); err != nil {
		t.Fatal(err)
	}
	if _, err := RunFakePipeline(s, id, nil); err != nil {
		t.Fatal(err)
	}
	segs, err := s.ListTranscript(id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, seg := range segs {
		if seg.Source == "local_fallback" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a local_fallback segment when local_mic is ready, got %+v", segs)
	}
}

func TestRunFakePipelineIndexesIntoKnowledge(t *testing.T) {
	stubPrivateLLM(t, "")
	s := NewMemoryStore()
	id := newMeeting(t, s)
	if err := s.End(id); err != nil {
		t.Fatal(err)
	}
	idx := knowledge.NewMemoryIndex()
	if _, err := RunFakePipeline(s, id, idx); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.Search(context.Background(), "转写摘要", "u-1", "", knowledge.SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected knowledge hits after fake pipeline INDEXING")
	}
	audits, _ := s.ListAudit(id)
	found := false
	for _, a := range audits {
		if a.Action == "index_upserted" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected index_upserted audit, got %+v", audits)
	}
}

func TestRunFakePipelineBindsOrganizerAndCitesSegments(t *testing.T) {
	stubPrivateLLM(t, "")
	s := NewMemoryStore()
	id := newMeeting(t, s)
	if err := s.MarkMemberJoined(id, "u-1", "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.End(id); err != nil {
		t.Fatal(err)
	}
	if _, err := RunFakePipeline(s, id, nil); err != nil {
		t.Fatal(err)
	}
	segs, err := s.ListTranscript(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) == 0 || segs[0].SpeakerUserID != "u-1" || segs[0].SpeakerDisplayName != "Alice" {
		t.Fatalf("expected organizer snapshot, got %+v", segs)
	}
	sum, ok := s.GetSummary(id)
	if !ok {
		t.Fatal("summary missing")
	}
	if strings.TrimSpace(sum.Summary) == "" {
		t.Fatal("expected non-empty summary grounded in transcript")
	}
	if len(sum.ActionItems) == 0 || strings.TrimSpace(sum.ActionItems[0].Task) == "" {
		t.Fatalf("expected action_items[].task, got %+v", sum.ActionItems)
	}
	if len(sum.ActionItems[0].SourceSegmentIDs) == 0 {
		t.Fatalf("expected grounded source_segment_ids, got %+v", sum.ActionItems)
	}
	if sum.OriginalJSON == "" {
		t.Fatal("expected AI original snapshot")
	}
}
