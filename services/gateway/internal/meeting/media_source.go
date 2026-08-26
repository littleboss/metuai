package meeting

import (
	"fmt"
	"strings"
)

// HasAuthoritativeAudio 表示至少有一路可用于转写的音源：
// 独立参会人音轨，或该人员工本机备份。房间混音不算权威音源。
func HasAuthoritativeAudio(arts []MediaArtifact) bool {
	for _, art := range arts {
		if art.Status != "ready" {
			continue
		}
		if art.Kind == KindParticipantTrack || art.Kind == KindLocalMic {
			return true
		}
	}
	return false
}

// MarkManualReview 在缺轨且无本机备份时进入人工复核，禁止静默 READY。
func MarkManualReview(repo Repository, meetingID, actorKey, reason string) (string, error) {
	if _, ok := repo.Get(meetingID); !ok {
		return "", fmt.Errorf("meeting not found")
	}
	if err := repo.SetPipelineStage(meetingID, StageManualReview); err != nil {
		return "", err
	}
	detail := strings.TrimSpace(reason)
	if detail == "" {
		detail = "authoritative audio missing"
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "pipeline_manual_review",
		Detail:    detail,
	})
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "pipeline_stage",
		Detail:    StageManualReview,
	})
	return StageManualReview, nil
}
