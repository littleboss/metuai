package meeting

import (
	"fmt"
	"strings"
)

// ASRResultInput 是 Worker 提交的一条转写片段（提交前未分配 id）。
type ASRResultInput struct {
	TrackID            string   `json:"track_id"`
	SpeakerUserID      string   `json:"speaker_user_id"`
	SpeakerDisplayName string   `json:"speaker_display_name"`
	Language           string   `json:"language"`
	StartMs            int      `json:"start_ms"`
	EndMs              int      `json:"end_ms"`
	Text               string   `json:"text"`
	ASRModel           string   `json:"asr_model"`
	Source             string   `json:"source"`
	Confidence         *float64 `json:"confidence"`
}

// ApplyASRResult 用 Worker 的真/假 ASR 结果覆盖转写，并推进到 TRANSCRIPT_READY。
// 会覆盖既有假转写；不自动跑纪要/索引（那些仍走 run-fake 或后续真实 LLM）。
func ApplyASRResult(repo Repository, meetingID, actorKey, backend string, inputs []ASRResultInput) (string, error) {
	current, ok := repo.Get(meetingID)
	if !ok {
		return "", fmt.Errorf("meeting not found")
	}
	if !current.Ended {
		return "", fmt.Errorf("meeting_not_ended")
	}
	if len(inputs) == 0 {
		return "", fmt.Errorf("segments_required")
	}

	segments := make([]TranscriptSegment, 0, len(inputs))
	for i, in := range inputs {
		src := strings.TrimSpace(in.Source)
		if src != "egress" && src != "local_fallback" {
			return "", fmt.Errorf("invalid_source_at_%d", i)
		}
		text := strings.TrimSpace(in.Text)
		if text == "" {
			return "", fmt.Errorf("empty_text_at_%d", i)
		}
		if in.EndMs < in.StartMs || in.StartMs < 0 {
			return "", fmt.Errorf("invalid_time_at_%d", i)
		}
		lang := languageOrDetect(in.Language, text)
		model := in.ASRModel
		if model == "" {
			model = "unknown"
		}
		display := in.SpeakerDisplayName
		if display == "" {
			display = in.SpeakerUserID
		}
		if display == "" {
			display = "speaker"
		}
		track := in.TrackID
		if track == "" {
			track = fmt.Sprintf("asr-track-%d", i)
		}
		segments = append(segments, TranscriptSegment{
			MeetingID:          meetingID,
			TrackID:            track,
			SpeakerUserID:      in.SpeakerUserID,
			SpeakerDisplayName: display,
			Language:           lang,
			StartMs:            in.StartMs,
			EndMs:              in.EndMs,
			Text:               text,
			ASRModel:           model,
			Source:             src,
			Confidence:         in.Confidence,
		})
	}

	if err := repo.SetPipelineStage(meetingID, StageTranscribing); err != nil {
		return "", err
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "pipeline_stage",
		Detail:    StageTranscribing,
	})

	members, _ := repo.ListMembers(meetingID)
	current, _ = repo.Get(meetingID)
	segments = BindTranscriptSpeakers(members, current.OrganizerID, segments)
	if err := repo.ReplaceTranscript(meetingID, segments); err != nil {
		_ = repo.SetPipelineStage(meetingID, StageRetryableError)
		return "", err
	}
	if err := repo.SetPipelineStage(meetingID, StageTranscriptReady); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("segments=%d backend=%s", len(segments), backend)
	if backend == "" {
		detail = fmt.Sprintf("segments=%d", len(segments))
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "asr_result_applied",
		Detail:    detail,
	})
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "pipeline_stage",
		Detail:    StageTranscriptReady,
	})
	return StageTranscriptReady, nil
}
