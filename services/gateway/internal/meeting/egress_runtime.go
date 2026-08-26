package meeting

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"metuai/services/gateway/internal/egress"
)

// EgressOrchestrator 是 *egress.Manager 的最小接口，便于单测注入替身。
type EgressOrchestrator interface {
	Enabled() bool
	Start(ctx context.Context, meetingID string) []egress.Started
	StartMissingParticipantTracks(ctx context.Context, meetingID string, existingKeys []string) []egress.Started
	FinalizeOne(ctx context.Context, egressID string) egress.Handle
}

// EgressRuntime 把 Egress 编排结果落到会议仓库（媒体元数据 + 审计）。
// 允许为 nil：此时全部路径降级为「只写 pending 计划」，与接线前行为一致。
type EgressRuntime struct {
	orch   EgressOrchestrator
	bucket string

	mu        sync.Mutex
	attempted map[string]bool
}

func NewEgressRuntime(orch EgressOrchestrator, bucket string) *EgressRuntime {
	if bucket == "" {
		bucket = "metuai-media"
	}
	return &EgressRuntime{orch: orch, bucket: bucket, attempted: map[string]bool{}}
}

// markAttempted 保证同一时刻一场会议只有一次 Start 在飞；deferred 后会 forget 允许重试。
func (rt *EgressRuntime) markAttempted(meetingID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.attempted[meetingID] {
		return false
	}
	rt.attempted[meetingID] = true
	return true
}

// EnsureStarted 在参会人拿 LiveKit 令牌（或心跳）时尝试拉起服务端录制。
// 房里还没人时只记 deferred 审计并释放去重锁，方便下次重试；绝不阻塞入会。
func (rt *EgressRuntime) EnsureStarted(ctx context.Context, repo Repository, meetingID string) {
	if rt == nil || !rt.markAttempted(meetingID) {
		return
	}
	if rt.orch == nil || !rt.orch.Enabled() {
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:egress",
			Action:    "egress_unavailable",
			Detail:    "livekit egress not configured; media stays pending",
		})
		return
	}
	results := rt.orch.Start(ctx, meetingID)
	if len(results) == 0 {
		rt.forget(meetingID)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:egress",
			Action:    "egress_unavailable",
			Detail:    "orchestrator returned no outputs",
		})
		return
	}

	var started, failed, pending int
	for _, r := range results {
		switch r.Outcome {
		case egress.OutcomeStarted:
			started++
		case egress.OutcomeFailed:
			failed++
		default:
			pending++
		}
	}
	// 全是 pending（典型：令牌已发但 WebRTC 还没进房）→ 不落库、不占坑，下次心跳再试。
	if started == 0 && failed == 0 {
		rt.forget(meetingID)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:egress",
			Action:    "egress_deferred",
			Detail:    results[0].Detail,
		})
		return
	}

	for _, r := range results {
		if r.Outcome == egress.OutcomePending {
			continue
		}
		_, _ = repo.AddMediaArtifact(MediaArtifact{
			MeetingID:      meetingID,
			Kind:           r.Kind,
			Status:         r.Outcome,
			ObjectKey:      r.ObjectKey,
			Detail:         r.Detail,
			EgressID:       r.EgressID,
			ParticipantKey: r.Identity,
		})
	}
	action := "egress_started"
	if started == 0 {
		action = "egress_start_failed"
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  "system:egress",
		Action:    action,
		Detail:    fmt.Sprintf("started=%d failed=%d pending=%d", started, failed, pending),
	})
}

// EnsureLateTracks 在房间录制已经 started 之后，为后进房的人补开独立音轨。
// 与 EnsureStarted 去重无关：可每次心跳调用；已有 object_key 的不会重复 Start。
func (rt *EgressRuntime) EnsureLateTracks(ctx context.Context, repo Repository, meetingID string) {
	if rt == nil || rt.orch == nil || !rt.orch.Enabled() {
		return
	}
	artifacts, err := repo.ListMediaArtifacts(meetingID)
	if err != nil {
		return
	}
	roomLive := false
	existingKeys := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		if a.Kind == egress.KindRoomAudio || a.Kind == egress.KindRoomVideo {
			if a.Status == egress.OutcomeStarted || a.Status == "ready" {
				roomLive = true
			}
		}
		if a.Kind == egress.KindParticipantTrack && a.ObjectKey != "" {
			existingKeys = append(existingKeys, a.ObjectKey)
		}
	}
	if !roomLive {
		return
	}
	results := rt.orch.StartMissingParticipantTracks(ctx, meetingID, existingKeys)
	if len(results) == 0 {
		return
	}
	var started, failed int
	for _, r := range results {
		if r.Outcome == egress.OutcomePending {
			continue
		}
		switch r.Outcome {
		case egress.OutcomeStarted:
			started++
		case egress.OutcomeFailed:
			failed++
		}
		_, _ = repo.AddMediaArtifact(MediaArtifact{
			MeetingID:      meetingID,
			Kind:           r.Kind,
			Status:         r.Outcome,
			ObjectKey:      r.ObjectKey,
			Detail:         r.Detail,
			EgressID:       r.EgressID,
			ParticipantKey: r.Identity,
		})
	}
	if started == 0 && failed == 0 {
		return
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  "system:egress",
		Action:    "egress_tracks_topup",
		Detail:    fmt.Sprintf("started=%d failed=%d", started, failed),
	})
}

// FinalizeOrPlan 在会议结束时收尾真实 Egress。
// 若本场根本没有真实录制（未接线 / 启动失败），退回写 pending 计划，
// 让会后假流水线仍可跑 PoC——但不会把任何一路谎报成 ready。
func (rt *EgressRuntime) FinalizeOrPlan(ctx context.Context, repo Repository, meetingID, bucket, note string) {
	// 会议已结束，无论收尾走哪条分支都要释放去重标记，
	// 否则长期运行的 gateway 会把每场会的 ID 永久留在 attempted 里。
	defer rt.forget(meetingID)
	if bucket == "" && rt != nil {
		bucket = rt.bucket
	}
	if bucket == "" {
		bucket = "metuai-media"
	}
	if rt == nil || rt.orch == nil || !rt.orch.Enabled() {
		writePendingPlans(repo, meetingID, bucket, note)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:egress",
			Action:    "egress_unavailable",
			Detail:    "meeting ended without server-side recording",
		})
		return
	}

	artifacts, err := repo.ListMediaArtifacts(meetingID)
	if err != nil {
		writePendingPlans(repo, meetingID, bucket, note)
		return
	}
	if len(artifacts) == 0 {
		writePendingPlans(repo, meetingID, bucket, note)
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:egress",
			Action:    "egress_unavailable",
			Detail:    "no egress was started for this meeting",
		})
		return
	}

	var ready, failed, unresolved int
	for _, a := range artifacts {
		if a.Status != egress.OutcomeStarted || a.EgressID == "" {
			continue
		}
		handle := rt.orch.FinalizeOne(ctx, a.EgressID)
		switch {
		case handle.Succeeded():
			ready++
			_ = repo.UpdateMediaArtifactStatus(a.ID, "ready", describeFiles(handle))
		case handle.Terminal():
			failed++
			detail := "egress " + handle.Status
			if handle.Error != "" {
				detail += ": " + handle.Error
			}
			_ = repo.UpdateMediaArtifactStatus(a.ID, "failed", detail)
		default:
			// 停止已下发但 LiveKit 还没给终态：保持 started，等人工或后续轮询。
			unresolved++
			_ = repo.UpdateMediaArtifactStatus(a.ID, egress.OutcomeStarted,
				"stop requested, awaiting final status ("+handle.Status+")")
		}
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  "system:egress",
		Action:    "egress_finalized",
		Detail:    fmt.Sprintf("ready=%d failed=%d unresolved=%d%s", ready, failed, unresolved, noteSuffix(note)),
	})
}

// forget 释放已结束会议的去重标记，避免长时间运行的进程无限增长。
// 允许 rt 为 nil：未接线时同样会被 defer 调用。
func (rt *EgressRuntime) forget(meetingID string) {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.attempted, meetingID)
}

func writePendingPlans(repo Repository, meetingID, bucket, note string) {
	for _, plan := range egress.PlanObjectKeys(meetingID, bucket, egress.DefaultDesiredOutputs()) {
		_, _ = repo.AddMediaArtifact(MediaArtifact{
			MeetingID: meetingID,
			Kind:      plan.Kind,
			Status:    egress.OutcomePending,
			ObjectKey: plan.ObjectKey,
			Detail:    plan.Detail + noteSuffix(note),
		})
	}
}

func describeFiles(handle egress.Handle) string {
	if len(handle.Files) == 0 {
		return "egress complete (no file location reported)"
	}
	return "egress complete: " + strings.Join(handle.Files, ", ")
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}
