package meeting

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"metuai/services/gateway/internal/knowledge"
)

// 会后流水线阶段（架构 §6.1）。权威状态在 PostgreSQL / 内存仓库，不靠 Dapr 状态。
const (
	StageRecordingFinalized  = "RECORDING_FINALIZED"
	StageMediaReady          = "MEDIA_READY"
	StageTranscribing        = "TRANSCRIBING"
	StageTranscriptReady     = "TRANSCRIPT_READY"
	StageExtractingArtifacts = "EXTRACTING_ARTIFACTS"
	StageIndexing            = "INDEXING"
	StageReady               = "READY"
	StageRetryableError      = "RETRYABLE_ERROR"
	StageManualReview        = "MANUAL_REVIEW"
)

// RunFakePipeline 在无真 ASR/Egress 时干跑整条会后链，供 PoC 验收状态机。
// idx 非 nil 时在 INDEXING 写入知识检索副本（默认 Memory / 可选 Vespa）。
// 不得在失败时静默标 READY；本函数同步执行，出错返回并写入 RETRYABLE_ERROR。
func RunFakePipeline(repo Repository, meetingID string, idx knowledge.Indexer) (string, error) {
	current, ok := repo.Get(meetingID)
	if !ok {
		return "", fmt.Errorf("meeting not found")
	}
	if !current.Ended {
		return "", fmt.Errorf("meeting_not_ended")
	}

	setStage := func(stage string) error {
		if err := repo.SetPipelineStage(meetingID, stage); err != nil {
			_ = repo.SetPipelineStage(meetingID, StageRetryableError)
			return err
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:worker",
			Action:    "pipeline_stage",
			Detail:    stage,
		})
		return nil
	}

	if current.PipelineStage == "" || current.PipelineStage == StageRecordingFinalized {
		if err := setStage(StageRecordingFinalized); err != nil {
			return "", err
		}
	}

	// MEDIA_READY：把 pending 媒体标为 ready（假对象键，非真 Egress）。
	if err := setStage(StageMediaReady); err != nil {
		return "", err
	}
	arts, err := repo.ListMediaArtifacts(meetingID)
	if err != nil {
		_ = repo.SetPipelineStage(meetingID, StageRetryableError)
		return "", err
	}
	for _, a := range arts {
		// 假流水线允许把 pending/started/failed 推进到 ready（明确标注 fake）。
		if a.Status != "ready" {
			if err := repo.UpdateMediaArtifactStatus(a.ID, "ready", "fake media ready (no LiveKit Egress yet)"); err != nil {
				_ = repo.SetPipelineStage(meetingID, StageRetryableError)
				return "", err
			}
		}
	}

	if err := setStage(StageTranscribing); err != nil {
		return "", err
	}

	chats, _ := repo.ListChat(meetingID)
	members, _ := repo.ListMembers(meetingID)
	segments := buildFakeTranscript(meetingID, current.Title, current.OrganizerID, members, chats, arts)
	if err := repo.ReplaceTranscript(meetingID, segments); err != nil {
		_ = repo.SetPipelineStage(meetingID, StageRetryableError)
		return "", err
	}
	if stored, err := repo.ListTranscript(meetingID); err == nil {
		segments = stored
	}
	if err := setStage(StageTranscriptReady); err != nil {
		return "", err
	}

	if err := setStage(StageExtractingArtifacts); err != nil {
		return "", err
	}
	// P1：不再调用 buildFakeSummary。纪要走 EnsureNotesAvailable → 私有 LLM（LLM_BASE_URL）。
	if _, err := EnsureNotesAvailable(context.Background(), repo, meetingID); err != nil {
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:worker",
			Action:    "notes_skipped",
			Detail:    err.Error(),
		})
		return StageTranscriptReady, err
	}

	// INDEXING：写入知识检索副本（ACL 在索引侧过滤）。
	if err := setStage(StageIndexing); err != nil {
		return "", err
	}
	if idx != nil {
		if err := IndexMeetingKnowledge(context.Background(), repo, idx, meetingID, nil); err != nil {
			_ = repo.SetPipelineStage(meetingID, StageRetryableError)
			_ = repo.AppendAudit(AuditEvent{
				MeetingID: meetingID,
				ActorKey:  "system:worker",
				Action:    "index_failed",
				Detail:    err.Error(),
			})
			return "", err
		}
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:worker",
			Action:    "index_upserted",
			Detail:    knowledge.BackendName(idx),
		})
	} else {
		_ = repo.AppendAudit(AuditEvent{
			MeetingID: meetingID,
			ActorKey:  "system:worker",
			Action:    "index_skipped",
			Detail:    "knowledge indexer not configured",
		})
	}

	if err := setStage(StageReady); err != nil {
		return "", err
	}
	return StageReady, nil
}

func buildFakeTranscript(meetingID, title, organizerID string, members []MeetingMember, chats []ChatMessage, arts []MediaArtifact) []TranscriptSegment {
	fallbacks := localFallbackKeys(arts)
	hasTrack := false
	for _, art := range arts {
		if art.Kind == KindParticipantTrack && art.Status == "ready" {
			hasTrack = true
			break
		}
	}
	organizerName := "组织者"
	for _, member := range members {
		if member.UserID == organizerID && strings.TrimSpace(member.DisplayNameSnapshot) != "" {
			organizerName = member.DisplayNameSnapshot
			break
		}
	}
	speakerID := organizerID
	if speakerID == "" {
		speakerID = "organizer"
	}
	organizerText := fmt.Sprintf("我们开始讨论「%s」。", title)
	segments := []TranscriptSegment{
		{
			MeetingID:          meetingID,
			TrackID:            "fake-track-organizer",
			SpeakerUserID:      speakerID,
			SpeakerDisplayName: organizerName,
			Language:           DetectSpokenLanguage(organizerText),
			StartMs:            0,
			EndMs:              2500,
			Text:               organizerText,
			ASRModel:           "fake-asr-poc",
			Source:             segmentSourceForSpeaker(speakerID, fallbacks, hasTrack),
		},
	}
	t := 3000
	for i, chat := range chats {
		end := t + 2000
		uid := strings.TrimPrefix(chat.SenderKey, "employee:")
		uid = strings.TrimPrefix(uid, "guest:")
		segments = append(segments, TranscriptSegment{
			MeetingID:          meetingID,
			TrackID:            fmt.Sprintf("fake-track-chat-%d", i),
			SpeakerUserID:      uid,
			SpeakerDisplayName: chat.DisplayName,
			Language:           DetectSpokenLanguage(chat.Body),
			StartMs:            t,
			EndMs:              end,
			Text:               chat.Body,
			ASRModel:           "fake-asr-poc",
			Source:             segmentSourceForSpeaker(uid, fallbacks, hasTrack),
		})
		t = end + 500
	}
	if len(chats) == 0 {
		secondID, secondName := "participant", "参会人"
		for _, member := range members {
			if member.UserID == organizerID {
				continue
			}
			secondID = member.UserID
			secondName = cmp.Or(strings.TrimSpace(member.DisplayNameSnapshot), member.UserID)
			break
		}
		secondText := "同意先落地会后假流水线，再换真实 ASR。"
		segments = append(segments, TranscriptSegment{
			MeetingID:          meetingID,
			TrackID:            "fake-track-2",
			SpeakerUserID:      secondID,
			SpeakerDisplayName: secondName,
			Language:           DetectSpokenLanguage(secondText),
			StartMs:            3000,
			EndMs:              5500,
			Text:               secondText,
			ASRModel:           "fake-asr-poc",
			Source:             segmentSourceForSpeaker(secondID, fallbacks, hasTrack),
		})
	}
	return BindTranscriptSpeakers(members, organizerID, segments)
}

