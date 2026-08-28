package meeting

import (
	"context"
	"testing"

	"metuai/services/gateway/internal/egress"
)

// fakeOrchestrator 让我们在不碰 LiveKit 的前提下驱动全部状态分支。
type fakeOrchestrator struct {
	enabled   bool
	starts    []egress.Started
	startCall int
	late      []egress.Started
	lateCall  int
	lateKeys  [][]string
	finalize  map[string]egress.Handle
	stopped   []string
}

func (f *fakeOrchestrator) Enabled() bool { return f.enabled }

func (f *fakeOrchestrator) Start(context.Context, string) []egress.Started {
	f.startCall++
	return f.starts
}

func (f *fakeOrchestrator) StartMissingParticipantTracks(_ context.Context, _ string, existingKeys []string) []egress.Started {
	f.lateCall++
	f.lateKeys = append(f.lateKeys, append([]string(nil), existingKeys...))
	return f.late
}

func (f *fakeOrchestrator) FinalizeOne(_ context.Context, egressID string) egress.Handle {
	f.stopped = append(f.stopped, egressID)
	if h, ok := f.finalize[egressID]; ok {
		return h
	}
	return egress.Handle{EgressID: egressID, Status: egress.StatusUnknown}
}

func newMeeting(t *testing.T, s *Store) string {
	t.Helper()
	m, _, err := s.Create("egress-test", "u-1", "password", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func artifactsByKind(t *testing.T, s *Store, meetingID string) map[string]MediaArtifact {
	t.Helper()
	list, err := s.ListMediaArtifacts(meetingID)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]MediaArtifact, len(list))
	for _, a := range list {
		out[a.Kind] = a
	}
	return out
}

func hasAudit(t *testing.T, s *Store, meetingID, action string) bool {
	t.Helper()
	events, err := s.ListAudit(meetingID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Action == action {
			return true
		}
	}
	return false
}

func TestEnsureStartedRecordsUnavailableWhenNotWired(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	rt := NewEgressRuntime(nil, "metuai-media")

	rt.EnsureStarted(context.Background(), s, id)

	if !hasAudit(t, s, id, "egress_unavailable") {
		t.Fatal("expected egress_unavailable audit")
	}
	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 0 {
		t.Fatalf("no artifacts should exist before the meeting ends, got %+v", list)
	}
}

func TestEnsureStartedIsIdempotentPerMeeting(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_a"},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")

	// 每个参会人拿令牌都会调一次，但只能真正开录一次。
	rt.EnsureStarted(context.Background(), s, id)
	rt.EnsureStarted(context.Background(), s, id)
	rt.EnsureStarted(context.Background(), s, id)

	if orch.startCall != 1 {
		t.Fatalf("want exactly 1 start, got %d", orch.startCall)
	}
	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 1 {
		t.Fatalf("want 1 artifact, got %+v", list)
	}
}

func TestEnsureStartedPersistsOutcomes(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_a", ObjectKey: "b/a.ogg"},
			{Kind: egress.KindRoomVideo, Outcome: egress.OutcomeFailed, Detail: "unreachable"},
			{Kind: egress.KindParticipantTrack, Outcome: egress.OutcomePending, Detail: "room empty"},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")

	rt.EnsureStarted(context.Background(), s, id)

	arts := artifactsByKind(t, s, id)
	if arts[egress.KindRoomAudio].Status != "started" || arts[egress.KindRoomAudio].EgressID != "eg_a" {
		t.Fatalf("audio artifact wrong: %+v", arts[egress.KindRoomAudio])
	}
	if arts[egress.KindRoomVideo].Status != "failed" {
		t.Fatalf("video artifact wrong: %+v", arts[egress.KindRoomVideo])
	}
	if _, ok := arts[egress.KindParticipantTrack]; ok {
		t.Fatal("pending track should not be persisted yet")
	}
	if !hasAudit(t, s, id, "egress_started") {
		t.Fatal("expected egress_started audit")
	}
}

func TestEnsureStartedDefersAndRetriesWhenAllPending(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomePending, Detail: "room empty"},
			{Kind: egress.KindRoomVideo, Outcome: egress.OutcomePending, Detail: "room empty"},
			{Kind: egress.KindParticipantTrack, Outcome: egress.OutcomePending, Detail: "room empty"},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")

	rt.EnsureStarted(context.Background(), s, id)
	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 0 {
		t.Fatalf("deferred start must not persist artifacts, got %+v", list)
	}
	if !hasAudit(t, s, id, "egress_deferred") {
		t.Fatal("expected egress_deferred audit")
	}

	// 第二次应能再调 Start（模拟心跳时人已进房；这里仍返回 pending 只验证去重已释放）。
	rt.EnsureStarted(context.Background(), s, id)
	if orch.startCall != 2 {
		t.Fatalf("want 2 start attempts after defer, got %d", orch.startCall)
	}
}

func TestFinalizeOrPlanWritesPendingWhenNeverStarted(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	rt := NewEgressRuntime(nil, "metuai-media")

	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 3 {
		t.Fatalf("want 3 pending plans, got %+v", list)
	}
	for _, a := range list {
		if a.Status != "pending" {
			t.Fatalf("artifact must stay pending: %+v", a)
		}
	}
	if !hasAudit(t, s, id, "egress_unavailable") {
		t.Fatal("expected egress_unavailable audit")
	}
}

func TestNilRuntimeStillWritesPendingPlans(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	var rt *EgressRuntime

	// nil runtime 是「完全未接线」的合法形态，不能 panic，也不能吞掉计划。
	rt.EnsureStarted(context.Background(), s, id)
	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 3 {
		t.Fatalf("want 3 pending plans, got %+v", list)
	}
}

func TestFinalizeOrPlanPromotesStartedToReady(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_a"},
		},
		finalize: map[string]egress.Handle{
			"eg_a": {EgressID: "eg_a", Status: egress.StatusComplete, Files: []string{"s3://metuai-media/x/room-audio.ogg"}},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")
	rt.EnsureStarted(context.Background(), s, id)

	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	arts := artifactsByKind(t, s, id)
	if arts[egress.KindRoomAudio].Status != "ready" {
		t.Fatalf("completed egress should be ready: %+v", arts[egress.KindRoomAudio])
	}
	if orch.stopped[0] != "eg_a" {
		t.Fatalf("expected eg_a to be stopped, got %+v", orch.stopped)
	}
}

func TestFinalizeOrPlanMarksFailedEgress(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_a"},
		},
		finalize: map[string]egress.Handle{
			"eg_a": {EgressID: "eg_a", Status: egress.StatusFailed, Error: "upload denied"},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")
	rt.EnsureStarted(context.Background(), s, id)

	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	arts := artifactsByKind(t, s, id)
	if arts[egress.KindRoomAudio].Status != "failed" {
		t.Fatalf("failed egress must be recorded as failed: %+v", arts[egress.KindRoomAudio])
	}
}

// 这是本轮最重要的不变量：拿不到终态时宁可停在 started，也不许猜成 ready。
func TestFinalizeOrPlanNeverFakesReadyWithoutTerminalStatus(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_a"},
		},
		finalize: map[string]egress.Handle{
			"eg_a": {EgressID: "eg_a", Status: egress.StatusUnknown, Error: "connection refused"},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")
	rt.EnsureStarted(context.Background(), s, id)

	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	arts := artifactsByKind(t, s, id)
	if arts[egress.KindRoomAudio].Status != "started" {
		t.Fatalf("unknown status must stay started, got %+v", arts[egress.KindRoomAudio])
	}
}

// FinalizeOrPlan 是一场会的终点：无论走哪条分支，去重标记都必须释放，
// 否则常驻进程的 attempted map 会随会议数量无限增长。
func TestFinalizeOrPlanReleasesDedupMarker(t *testing.T) {
	s := NewMemoryStore()
	orch := &fakeOrchestrator{enabled: true}
	rt := NewEgressRuntime(orch, "metuai-media")

	// 分支一：编排器可用但没有产物。
	started := newMeeting(t, s)
	rt.EnsureStarted(context.Background(), s, started)
	rt.FinalizeOrPlan(context.Background(), s, started, "metuai-media", "")

	// 分支二：编排器不可用，只写 pending 计划。
	offline := NewEgressRuntime(nil, "metuai-media")
	planned := newMeeting(t, s)
	offline.EnsureStarted(context.Background(), s, planned)
	offline.FinalizeOrPlan(context.Background(), s, planned, "metuai-media", "")

	for name, r := range map[string]*EgressRuntime{"started": rt, "offline": offline} {
		r.mu.Lock()
		left := len(r.attempted)
		r.mu.Unlock()
		if left != 0 {
			t.Fatalf("%s runtime leaked %d dedup entries", name, left)
		}
	}
}

// EGRESS_ENABLED=false 之外的另一种降级：凭证齐全但 LiveKit API 不可达（NoopClient）。
// ListParticipants 失败时 Manager 整批 deferred，不落伪 started；终态由 FinalizeOrPlan 写 pending。
func TestEgressRuntimeDegradesWithNoopClient(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	cfg := egress.Config{
		LiveKitURL:       "ws://127.0.0.1:17880",
		LiveKitAPIKey:    "devkey",
		LiveKitAPISecret: "secret",
		S3Endpoint:       "http://127.0.0.1:9000",
		S3Bucket:         "metuai-media",
		S3AccessKey:      "ak",
		S3SecretKey:      "sk",
	}
	rt := NewEgressRuntime(egress.NewManager(cfg, egress.NoopClient{}, 0), "metuai-media")

	rt.EnsureStarted(context.Background(), s, id)
	if !hasAudit(t, s, id, "egress_deferred") {
		t.Fatal("expected egress_deferred when list participants fails")
	}
	list, _ := s.ListMediaArtifacts(id)
	if len(list) != 0 {
		t.Fatalf("deferred start must not persist artifacts, got %+v", list)
	}

	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	list, _ = s.ListMediaArtifacts(id)
	if len(list) == 0 {
		t.Fatal("finalize should still record pending plans")
	}
	for _, a := range list {
		if a.Status == "ready" || a.Status == "started" {
			t.Fatalf("noop client must never yield %s: %+v", a.Status, a)
		}
		if a.EgressID != "" {
			t.Fatalf("nothing really started, egress_id must stay empty: %+v", a)
		}
	}
}

func TestEnsureLateTracksTopsUpMissingParticipants(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	_, _ = s.AddMediaArtifact(MediaArtifact{
		MeetingID: id,
		Kind:      egress.KindRoomAudio,
		Status:    egress.OutcomeStarted,
		ObjectKey: "metuai-media/" + id + "/room-audio.ogg",
		EgressID:  "eg_a",
	})
	_, _ = s.AddMediaArtifact(MediaArtifact{
		MeetingID: id,
		Kind:      egress.KindParticipantTrack,
		Status:    egress.OutcomeStarted,
		ObjectKey: "metuai-media/" + id + "/tracks/u-1.ogg",
		EgressID:  "eg_t1",
	})
	orch := &fakeOrchestrator{
		enabled: true,
		late: []egress.Started{
			{
				Kind:      egress.KindParticipantTrack,
				Outcome:   egress.OutcomeStarted,
				EgressID:  "eg_t2",
				ObjectKey: "metuai-media/" + id + "/tracks/u-2.ogg",
			},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")

	rt.EnsureLateTracks(context.Background(), s, id)

	if orch.lateCall != 1 {
		t.Fatalf("want 1 late call, got %d", orch.lateCall)
	}
	if len(orch.lateKeys[0]) != 1 || orch.lateKeys[0][0] != "metuai-media/"+id+"/tracks/u-1.ogg" {
		t.Fatalf("existing keys not passed through: %+v", orch.lateKeys)
	}
	if !hasAudit(t, s, id, "egress_tracks_topup") {
		t.Fatal("expected egress_tracks_topup audit")
	}
	list, _ := s.ListMediaArtifacts(id)
	var found bool
	for _, a := range list {
		if a.EgressID == "eg_t2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing late track artifact: %+v", list)
	}
}

func TestEnsureLateTracksSkipsWhenRoomNotLive(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{enabled: true, late: []egress.Started{{Kind: egress.KindParticipantTrack, Outcome: egress.OutcomeStarted}}}
	rt := NewEgressRuntime(orch, "metuai-media")

	rt.EnsureLateTracks(context.Background(), s, id)

	if orch.lateCall != 0 {
		t.Fatalf("must not top up before room egress is live, got %d", orch.lateCall)
	}
}

func TestFinalizeOrPlanLeavesPendingTracksAlone(t *testing.T) {
	s := NewMemoryStore()
	id := newMeeting(t, s)
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindParticipantTrack, Outcome: egress.OutcomePending, Detail: "room empty"},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")
	rt.EnsureStarted(context.Background(), s, id)

	rt.FinalizeOrPlan(context.Background(), s, id, "metuai-media", "")

	arts := artifactsByKind(t, s, id)
	if arts[egress.KindParticipantTrack].Status != "pending" {
		t.Fatalf("pending track should stay pending: %+v", arts[egress.KindParticipantTrack])
	}
	if len(orch.stopped) != 0 {
		t.Fatalf("nothing to stop, got %+v", orch.stopped)
	}
}
