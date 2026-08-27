package meeting

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestGenerateTranscriptAuthz(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-org", "Org")
	other := employeeJWTFor(t, secretEmp, "u-other", "Other")
	id, _ := createMeeting(t, r, organizer)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	_, _ = store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: KindParticipantTrack, Status: "ready", ObjectKey: "metuai-media/track.bin",
	})

	denied := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/transcript/generate", other, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-organizer want 403 got %d %s", denied.Code, denied.Body.String())
	}
	noTok := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/transcript/generate", "", "")
	if noTok.Code != http.StatusUnauthorized {
		t.Fatalf("no token want 401 got %d %s", noTok.Code, noTok.Body.String())
	}
}

func TestGenerateTranscriptMeetingNotEnded(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	_, _ = store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: KindParticipantTrack, Status: "ready", ObjectKey: "metuai-media/track.bin",
	})

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/transcript/generate", emp, "")
	if gen.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", gen.Code, gen.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(gen.Body.Bytes(), &body)
	if body.Error != "meeting_not_ended" {
		t.Fatalf("body %+v", body)
	}
}

func TestGenerateTranscriptNoAudio(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/transcript/generate", emp, "")
	if gen.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d %s", gen.Code, gen.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(gen.Body.Bytes(), &body)
	if body.Error != "no_audio" {
		t.Fatalf("body %+v", body)
	}
}

func TestGenerateTranscriptASRNotConfiguredNoPublicOutbound(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	t.Setenv("ASR_BASE_URL", "")
	rec := &recordingRoundTripper{}
	prev := asrHTTPClient
	asrHTTPClient = &http.Client{Transport: rec}
	t.Cleanup(func() { asrHTTPClient = prev })

	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	_, _ = store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: KindParticipantTrack, Status: "ready", ObjectKey: "metuai-media/track.bin",
	})

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/transcript/generate", emp, "")
	if gen.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d %s", gen.Code, gen.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(gen.Body.Bytes(), &body)
	if body.Error != "ASR_NOT_CONFIGURED" {
		t.Fatalf("body %+v", body)
	}
	for _, host := range rec.hosts {
		h := strings.ToLower(host)
		for blocked := range publicASRHosts {
			if h == blocked || strings.HasSuffix(h, "."+blocked) {
				t.Fatalf("must not call public ASR host %q; seen %v", host, rec.hosts)
			}
		}
	}
	if len(rec.hosts) != 0 {
		t.Fatalf("expected zero outbound ASR calls when unset, got %v", rec.hosts)
	}
}

func TestGenerateTranscriptHappyPathPersistsSegments(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	stubPrivateASR(t, `[{"track_id":"asr-1","speaker_user_id":"u-1","speaker_display_name":"Alice","language":"zh-CN","start_ms":0,"end_ms":1200,"text":"大家好","asr_model":"stub-private-asr","source":"egress"}]`)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	_, _ = store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: KindParticipantTrack, Status: "ready", ObjectKey: "metuai-media/track.bin",
	})

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/transcript/generate", emp, "")
	if gen.Code != http.StatusOK {
		t.Fatalf("generate want 200 got %d %s", gen.Code, gen.Body.String())
	}
	var payload struct {
		PipelineStage string             `json:"pipeline_stage"`
		Segments      []TranscriptSegment `json:"segments"`
	}
	if err := json.Unmarshal(gen.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PipelineStage != StageTranscriptReady {
		t.Fatalf("stage=%s", payload.PipelineStage)
	}
	if len(payload.Segments) != 1 || payload.Segments[0].Text != "大家好" {
		t.Fatalf("segments %+v", payload.Segments)
	}

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/transcript", emp, "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET transcript want 200 got %d %s", got.Code, got.Body.String())
	}
	var tr struct {
		Segments []TranscriptSegment `json:"segments"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Segments) != 1 || tr.Segments[0].Text != "大家好" {
		t.Fatalf("persisted segments %+v", tr.Segments)
	}
}

func TestIterAuthoritativeAudioSourcesPrefersTrackOverLocalMic(t *testing.T) {
	arts := []MediaArtifact{
		{Kind: KindParticipantTrack, Status: "ready", ObjectKey: "track.bin", ParticipantKey: "employee:u-1"},
		{Kind: KindLocalMic, Status: "ready", ObjectKey: "mic.bin", ParticipantKey: "employee:u-1"},
		{Kind: KindRoomAudio, Status: "ready", ObjectKey: "room.ogg"},
	}
	out := iterAuthoritativeAudioSources(arts)
	if len(out) != 1 {
		t.Fatalf("want 1 source (track only), got %+v", out)
	}
	if out[0].Kind != KindParticipantTrack {
		t.Fatalf("kind=%s", out[0].Kind)
	}
}
