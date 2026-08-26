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
	summary := buildFakeSummary(meetingID, current.Title, current.OrganizerID, segments, chats)
	if err := repo.UpsertSummary(summary); err != nil {
		_ = repo.SetPipelineStage(meetingID, StageRetryableError)
		return "", err
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

func buildFakeSummary(meetingID, title, organizerID string, segments []TranscriptSegment, chats []ChatMessage) MeetingSummary {
	quoted := make([]string, 0, len(segments))
	segIDs := make([]string, 0, len(segments))
	for _, s := range segments {
		quoted = append(quoted, s.Text)
		if s.ID != "" {
			segIDs = append(segIDs, s.ID)
		}
	}
	body := strings.Join(quoted, " ")
	if len(body) > 240 {
		body = body[:240] + "…"
	}
	msgIDs := make([]string, 0, len(chats))
	for _, chat := range chats {
		if chat.ID != "" {
			msgIDs = append(msgIDs, chat.ID)
		}
	}
	actions := []ActionItem{{
		Task:             "跟进会后假流水线切换为真实 ASR",
		OwnerUserID:      organizerID,
		SourceSegmentIDs: segIDs,
	}}
	if len(chats) > 0 {
		actions = append(actions, ActionItem{
			Task:             "整理会中落库聊天中的待办",
			OwnerUserID:      organizerID,
			SourceMessageIDs: msgIDs,
		})
	}
	return MeetingSummary{
		MeetingID: meetingID,
		Summary:   fmt.Sprintf("【假纪要】会议「%s」已结束。内容摘要：%s", title, body),
		Decisions: []CitedItem{
			{Text: "采用会后处理而非实时 AI 助手", SourceSegmentIDs: segIDs},
			{Text: "转写权威音源为 Egress 独立音轨", SourceSegmentIDs: segIDs},
		},
		ActionItems: actions,
		Risks: []CitedItem{
			{Text: "当前转写为假数据，不可用于生产决策", SourceSegmentIDs: segIDs},
		},
		OpenQuestions: []CitedItem{
			{Text: "何时切换 FunASR / WhisperX 评测？", SourceSegmentIDs: segIDs},
		},
	}
}
