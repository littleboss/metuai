package meeting

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNoTranscript 表示尚无转写，不能发明纪要或待办。
	ErrNoTranscript = errors.New("no_transcript")
	// ErrAINotConfigured 表示私有 LLM 未配置；会议仍可进行，禁止出站公网 LLM。
	ErrAINotConfigured = errors.New("AI_NOT_CONFIGURED")
	// ErrMeetingNotEnded 表示会议尚未结束，不能生成纪要。
	ErrMeetingNotEnded = errors.New("meeting_not_ended")
)

// GenerateMeetingSummary 调用私有 LLM 生成纪要并落库。
// 组织者/共同组织者经 HTTP 层校验；此处校验结束态、转写与 LLM 配置。
// owner_user_id 非空且非本场内部用户时返回 ErrOwnerMustBeInternal。
func GenerateMeetingSummary(ctx context.Context, repo Repository, meetingID string) (MeetingSummary, error) {
	current, ok := repo.Get(meetingID)
	if !ok {
		return MeetingSummary{}, fmt.Errorf("meeting not found")
	}
	if !current.Ended {
		return MeetingSummary{}, ErrMeetingNotEnded
	}

	segments, err := repo.ListTranscript(meetingID)
	if err != nil {
		return MeetingSummary{}, err
	}
	if len(segments) == 0 {
		return MeetingSummary{}, ErrNoTranscript
	}
	if !PrivateLLMConfigured() {
		return MeetingSummary{}, ErrAINotConfigured
	}

	owners := allowedOwnerList(repo, meetingID)
	draft, model, err := callPrivateLLM(ctx, current, segments, owners)
	if err != nil {
		if errors.Is(err, ErrAINotConfigured) {
			return MeetingSummary{}, ErrAINotConfigured
		}
		return MeetingSummary{}, err
	}
	if err := validateActionOwners(repo, meetingID, draft.ActionItems); err != nil {
		return MeetingSummary{}, err
	}

	sum := MeetingSummary{
		MeetingID:     meetingID,
		Summary:       draft.Summary,
		Decisions:     draft.Decisions,
		ActionItems:   draft.ActionItems,
		Risks:         draft.Risks,
		OpenQuestions: draft.OpenQuestions,
		Model:         model,
	}
	if err := validateSummaryDelivery(sum); err != nil {
		return MeetingSummary{}, err
	}
	sum.OriginalJSON = captureOriginalJSON(sum)
	if err := repo.UpsertSummary(sum); err != nil {
		return MeetingSummary{}, err
	}
	if stored, ok := repo.GetSummary(meetingID); ok {
		return stored, nil
	}
	return sum, nil
}

// EnsureNotesAvailable 保留给假流水线：已有纪要则返回；否则尝试私有 LLM 生成。
// GET /summary 不再调用本函数（未就绪返回 404 summary_not_ready）。
func EnsureNotesAvailable(ctx context.Context, repo Repository, meetingID string) (MeetingSummary, error) {
	if sum, ok := repo.GetSummary(meetingID); ok {
		if err := validateSummaryDelivery(sum); err != nil {
			return MeetingSummary{}, err
		}
		return sum, nil
	}
	return GenerateMeetingSummary(ctx, repo, meetingID)
}

func validateSummaryDelivery(sum MeetingSummary) error {
	if strings.TrimSpace(sum.Summary) == "" {
		return fmt.Errorf("empty summary")
	}
	if sum.ActionItems == nil {
		return fmt.Errorf("action_items required")
	}
	for i, item := range sum.ActionItems {
		if strings.TrimSpace(item.Task) == "" {
			return fmt.Errorf("action_items[%d].task required", i)
		}
	}
	return nil
}
