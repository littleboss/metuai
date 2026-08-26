package meeting

import (
	"context"
	"testing"
	"time"

	"metuai/services/gateway/internal/egress"
)

func TestEndIdleMeetings(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("idle-me", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.meetings[m.ID]
	got.LastActiveAt = time.Now().UTC().Add(-11 * time.Minute)
	s.meetings[m.ID] = got
	s.mu.Unlock()

	endIdleMeetings(s, 10*time.Minute, nil, nil)

	after, ok := s.Get(m.ID)
	if !ok || !after.Ended {
		t.Fatalf("expected idle meeting ended, got %+v", after)
	}
	audits, err := s.ListAudit(m.ID)
	if err != nil || len(audits) == 0 || audits[0].Action != "meeting_idle_ended" {
		t.Fatalf("expected idle audit, got %+v err=%v", audits, err)
	}
}

// 自动结束和组织者手动结束必须走同一条收尾逻辑：
// 有真实录制就停掉并按终态落库，没有才退回 pending 计划。
func TestEndIdleMeetingsFinalizesEgress(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("idle-egress", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_a"},
		},
		finalize: map[string]egress.Handle{
			"eg_a": {EgressID: "eg_a", Status: egress.StatusComplete},
		},
	}
	rt := NewEgressRuntime(orch, "metuai-media")
	rt.EnsureStarted(context.Background(), s, m.ID)

	s.mu.Lock()
	got := s.meetings[m.ID]
	got.LastActiveAt = time.Now().UTC().Add(-11 * time.Minute)
	s.meetings[m.ID] = got
	s.mu.Unlock()

	endIdleMeetings(s, 10*time.Minute, rt, nil)

	if len(orch.stopped) != 1 || orch.stopped[0] != "eg_a" {
		t.Fatalf("idle end should stop the running egress, got %+v", orch.stopped)
	}
	arts := artifactsByKind(t, s, m.ID)
	if arts[egress.KindRoomAudio].Status != "ready" {
		t.Fatalf("completed egress should be ready: %+v", arts[egress.KindRoomAudio])
	}
	// 自动结束不能额外补写 pending 计划，否则会和真实产物重复。
	if list, _ := s.ListMediaArtifacts(m.ID); len(list) != 1 {
		t.Fatalf("want exactly 1 artifact, got %+v", list)
	}
}

func TestEndIdleMeetingsSkipsFresh(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("fresh", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	endIdleMeetings(s, 10*time.Minute, nil, nil)
	after, ok := s.Get(m.ID)
	if !ok || after.Ended {
		t.Fatalf("fresh meeting should stay open, got %+v", after)
	}
}

func TestEndIdleMeetingsKeepsOccupiedRoom(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("still-there", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.meetings[m.ID]
	got.LastActiveAt = time.Now().UTC().Add(-11 * time.Minute)
	s.meetings[m.ID] = got
	s.mu.Unlock()

	endIdleMeetings(s, 10*time.Minute, nil, func(context.Context, string) (bool, error) {
		return true, nil
	})
	after, ok := s.Get(m.ID)
	if !ok || after.Ended {
		t.Fatalf("occupied room must not idle-end, got %+v", after)
	}

	s.mu.Lock()
	got = s.meetings[m.ID]
	got.LastActiveAt = time.Now().UTC().Add(-11 * time.Minute)
	s.meetings[m.ID] = got
	s.mu.Unlock()

	endIdleMeetings(s, 10*time.Minute, nil, func(context.Context, string) (bool, error) {
		return false, nil
	})
	after, ok = s.Get(m.ID)
	if !ok || !after.Ended {
		t.Fatalf("empty room after heartbeat timeout should end, got %+v", after)
	}
}

func TestEndIdleMeetingsSkipsWhenOccupancyUnknown(t *testing.T) {
	s := NewMemoryStore()
	m, _, err := s.Create("lk-down", "u-1", "password")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	got := s.meetings[m.ID]
	got.LastActiveAt = time.Now().UTC().Add(-11 * time.Minute)
	s.meetings[m.ID] = got
	s.mu.Unlock()

	endIdleMeetings(s, 10*time.Minute, nil, func(context.Context, string) (bool, error) {
		return false, context.DeadlineExceeded
	})
	after, ok := s.Get(m.ID)
	if !ok || after.Ended {
		t.Fatalf("unknown occupancy should not end the meeting, got %+v", after)
	}
}
