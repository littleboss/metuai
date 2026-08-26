package egress

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeClient 是可编程的 Egress 替身：每个方法都能被单个用例改写。
type fakeClient struct {
	nextID        int
	composite     func(room string, spec OutputSpec) (Handle, error)
	participant   func(room, identity string, spec OutputSpec) (Handle, error)
	participants  func(room string) ([]string, error)
	describe      func(egressID string) (Handle, error)
	stop          func(egressID string) (Handle, error)
	compositeSpec []OutputSpec
}

func (f *fakeClient) StartRoomComposite(_ context.Context, room string, spec OutputSpec) (Handle, error) {
	f.compositeSpec = append(f.compositeSpec, spec)
	if f.composite != nil {
		return f.composite(room, spec)
	}
	f.nextID++
	return Handle{EgressID: fmt.Sprintf("eg_%d", f.nextID), Status: StatusStarting}, nil
}

func (f *fakeClient) StartParticipant(_ context.Context, room, identity string, spec OutputSpec) (Handle, error) {
	if f.participant != nil {
		return f.participant(room, identity, spec)
	}
	f.nextID++
	return Handle{EgressID: fmt.Sprintf("eg_%d", f.nextID), Status: StatusStarting}, nil
}

func (f *fakeClient) ListParticipants(_ context.Context, room string) ([]string, error) {
	if f.participants != nil {
		return f.participants(room)
	}
	return nil, nil
}

func (f *fakeClient) Describe(_ context.Context, egressID string) (Handle, error) {
	if f.describe != nil {
		return f.describe(egressID)
	}
	return Handle{}, fmt.Errorf("not found")
}

func (f *fakeClient) Stop(_ context.Context, egressID string) (Handle, error) {
	if f.stop != nil {
		return f.stop(egressID)
	}
	return Handle{EgressID: egressID, Status: StatusComplete}, nil
}

func testConfig() Config {
	return Config{
		LiveKitURL:       "ws://127.0.0.1:17880",
		LiveKitAPIKey:    "devkey",
		LiveKitAPISecret: "secret",
		S3Endpoint:       "http://127.0.0.1:19000",
		S3Bucket:         "metuai-media",
		S3AccessKey:      "metuai",
		S3SecretKey:      "metuai-secret",
	}
}

func byKind(results []Started, kind string) []Started {
	out := make([]Started, 0, len(results))
	for _, r := range results {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func TestManagerDisabledWithoutClient(t *testing.T) {
	m := NewManager(testConfig(), nil, 0)
	if m.Enabled() {
		t.Fatal("manager without client must be disabled")
	}
	if got := m.Start(context.Background(), "mtg_1"); got != nil {
		t.Fatalf("disabled manager must not start anything, got %+v", got)
	}
}

func TestManagerDisabledWithIncompleteConfig(t *testing.T) {
	cfg := testConfig()
	cfg.S3SecretKey = ""
	m := NewManager(cfg, &fakeClient{}, 0)
	if m.Enabled() {
		t.Fatal("missing S3 secret must disable the manager")
	}
}

func TestManagerStartRoomComposites(t *testing.T) {
	client := &fakeClient{participants: func(string) ([]string, error) { return []string{"u-1"}, nil }}
	m := NewManager(testConfig(), client, 0)

	results := m.Start(context.Background(), "mtg_1")

	if len(results) != 3 {
		t.Fatalf("want audio+video+one track, got %+v", results)
	}
	for _, r := range results {
		if r.Outcome != OutcomeStarted {
			t.Fatalf("all outputs should start: %+v", r)
		}
		if r.EgressID == "" {
			t.Fatalf("started output must carry an egress id: %+v", r)
		}
		if !strings.HasPrefix(r.ObjectKey, "metuai-media/mtg_1/") {
			t.Fatalf("object key should be bucket-qualified: %+v", r)
		}
	}
	// 混音必须 audio-only、房间画面必须带视频，否则录出来的东西不对。
	if len(client.compositeSpec) != 2 {
		t.Fatalf("want 2 composite calls, got %d", len(client.compositeSpec))
	}
	if !client.compositeSpec[0].AudioOnly || client.compositeSpec[0].FileType != FileOGG {
		t.Fatalf("first composite should be audio-only ogg: %+v", client.compositeSpec[0])
	}
	if client.compositeSpec[1].AudioOnly || client.compositeSpec[1].FileType != FileMP4 {
		t.Fatalf("second composite should be av mp4: %+v", client.compositeSpec[1])
	}
}

func TestManagerStartRecordsPerOutputFailure(t *testing.T) {
	client := &fakeClient{
		composite: func(_ string, spec OutputSpec) (Handle, error) {
			if spec.AudioOnly {
				return Handle{}, fmt.Errorf("egress service unreachable")
			}
			return Handle{EgressID: "eg_video", Status: StatusStarting}, nil
		},
		participants: func(string) ([]string, error) { return []string{"u-1"}, nil },
	}
	m := NewManager(testConfig(), client, 0)

	results := m.Start(context.Background(), "mtg_1")

	audio := byKind(results, KindRoomAudio)
	if len(audio) != 1 || audio[0].Outcome != OutcomeFailed {
		t.Fatalf("audio should be failed, got %+v", audio)
	}
	if audio[0].EgressID != "" {
		t.Fatalf("failed output must not claim an egress id: %+v", audio[0])
	}
	video := byKind(results, KindRoomVideo)
	if len(video) != 1 || video[0].Outcome != OutcomeStarted {
		t.Fatalf("video should still start, got %+v", video)
	}
}

func TestManagerStartDefersWhenRoomEmpty(t *testing.T) {
	client := &fakeClient{}
	m := NewManager(testConfig(), client, 0)

	results := m.Start(context.Background(), "mtg_1")

	if len(results) != 3 {
		t.Fatalf("want 3 deferred outputs, got %+v", results)
	}
	for _, r := range results {
		if r.Outcome != OutcomePending {
			t.Fatalf("empty room must defer all outputs: %+v", r)
		}
		if r.EgressID != "" {
			t.Fatalf("deferred output must not claim egress id: %+v", r)
		}
	}
	if len(client.compositeSpec) != 0 {
		t.Fatalf("must not call StartRoomComposite on empty room, got %d", len(client.compositeSpec))
	}
}

func TestManagerStartMissingParticipantTracksSkipsExisting(t *testing.T) {
	var started []string
	client := &fakeClient{
		participants: func(string) ([]string, error) { return []string{"u-1", "u-2"}, nil },
		participant: func(_ string, identity string, _ OutputSpec) (Handle, error) {
			started = append(started, identity)
			return Handle{EgressID: "eg_" + identity, Status: StatusStarting}, nil
		},
	}
	m := NewManager(testConfig(), client, 0)
	existing := []string{m.qualify(ParticipantPath("mtg_1", "u-1"))}

	results := m.StartMissingParticipantTracks(context.Background(), "mtg_1", existing)

	if len(results) != 1 || results[0].Outcome != OutcomeStarted {
		t.Fatalf("want one new track, got %+v", results)
	}
	if len(started) != 1 || started[0] != "u-2" {
		t.Fatalf("should only start u-2, got %v", started)
	}
}

func TestManagerFinalizeReportsCompletion(t *testing.T) {
	client := &fakeClient{
		stop: func(id string) (Handle, error) {
			return Handle{EgressID: id, Status: StatusComplete, Files: []string{"s3://metuai-media/mtg_1/room-audio.ogg"}}, nil
		},
	}
	m := NewManager(testConfig(), client, 0)

	handle := m.FinalizeOne(context.Background(), "eg_1")

	if !handle.Succeeded() || len(handle.Files) != 1 {
		t.Fatalf("want completed handle with file, got %+v", handle)
	}
}

func TestManagerFinalizeFallsBackToDescribeWhenStopFails(t *testing.T) {
	// Egress 自然结束后再 Stop 会报错，此时必须以 Describe 的真实终态为准。
	client := &fakeClient{
		stop: func(string) (Handle, error) { return Handle{}, fmt.Errorf("egress has already ended") },
		describe: func(id string) (Handle, error) {
			return Handle{EgressID: id, Status: StatusFailed, Error: "upload denied"}, nil
		},
	}
	m := NewManager(testConfig(), client, 0)

	handle := m.FinalizeOne(context.Background(), "eg_1")

	if handle.Status != StatusFailed || handle.Error != "upload denied" {
		t.Fatalf("want failed handle from describe, got %+v", handle)
	}
}

func TestManagerFinalizeStaysUnknownWhenLiveKitUnreachable(t *testing.T) {
	client := &fakeClient{
		stop:     func(string) (Handle, error) { return Handle{}, fmt.Errorf("connection refused") },
		describe: func(string) (Handle, error) { return Handle{}, fmt.Errorf("connection refused") },
	}
	m := NewManager(testConfig(), client, 0)

	handle := m.FinalizeOne(context.Background(), "eg_1")

	if handle.Succeeded() || handle.Terminal() {
		t.Fatalf("unreachable livekit must not produce a terminal verdict: %+v", handle)
	}
	if handle.Status != StatusUnknown {
		t.Fatalf("want unknown status, got %+v", handle)
	}
}

func TestManagerFinalizeKeepsNonTerminalWhenBudgetExhausted(t *testing.T) {
	// finalizeTimeout=0：Stop 后还在 ENDING，没有轮询预算，只能如实返回非终态。
	client := &fakeClient{
		stop: func(id string) (Handle, error) { return Handle{EgressID: id, Status: StatusEnding}, nil },
	}
	m := NewManager(testConfig(), client, 0)

	handle := m.FinalizeOne(context.Background(), "eg_1")

	if handle.Status != StatusEnding || handle.Succeeded() {
		t.Fatalf("want non-terminal ending handle, got %+v", handle)
	}
}

func TestManagerFinalizeDisabled(t *testing.T) {
	m := NewManager(testConfig(), nil, 0)
	if handle := m.FinalizeOne(context.Background(), "eg_1"); handle.Succeeded() {
		t.Fatalf("disabled manager must not report success: %+v", handle)
	}
}
