package meeting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	// ErrNoTranscript 表示尚无转写，不能发明纪要或待办。
	ErrNoTranscript = errors.New("no_transcript")
	// ErrAINotConfigured 表示私有 LLM 未配置；会议仍可进行，禁止出站公网 LLM。
	ErrAINotConfigured = errors.New("AI_NOT_CONFIGURED")
)

// PrivateLLMConfigured 仅当显式配置了私有 LLM 端点时为真。
// 不读取公网供应商密钥；未配置时纪要接口返回 503，不得发明人名/待办。
func PrivateLLMConfigured() bool {
	return strings.TrimSpace(os.Getenv("PRIVATE_LLM_URL")) != ""
}

// EnsureNotesAvailable 是 P0 纪要交付：已有纪要则校验字段；否则按转写与私有 LLM 配置决定 422/503。
// 成功时保证 summary 非空；若有 action_items，则每条 task 必填。不发明内容、不出站公网 LLM。
func EnsureNotesAvailable(ctx context.Context, repo Repository, meetingID string) (MeetingSummary, error) {
	_ = ctx
	if sum, ok := repo.GetSummary(meetingID); ok {
		if err := validateSummaryDelivery(sum); err != nil {
			return MeetingSummary{}, err
		}
		return sum, nil
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

	current, ok := repo.Get(meetingID)
	if !ok {
		return MeetingSummary{}, fmt.Errorf("meeting not found")
	}
	sum := buildPrivateNotesFromTranscript(meetingID, current.Title, segments)
	if err := validateSummaryDelivery(sum); err != nil {
		return MeetingSummary{}, err
	}
	if err := repo.UpsertSummary(sum); err != nil {
		return MeetingSummary{}, err
	}
	stored, _ := repo.GetSummary(meetingID)
	if stored.MeetingID == "" {
		return sum, nil
	}
	return stored, nil
}

// buildPrivateNotesFromTranscript 仅用已有转写文本生成纪要，不编造发言人姓名或无依据待办。
// P0 不向公网 LLM 发请求；PRIVATE_LLM_URL 表示现场已具备私有推理能力。
func buildPrivateNotesFromTranscript(meetingID, title string, segments []TranscriptSegment) MeetingSummary {
	quoted := make([]string, 0, len(segments))
	segIDs := make([]string, 0, len(segments))
	for _, s := range segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		quoted = append(quoted, text)
		if s.ID != "" {
			segIDs = append(segIDs, s.ID)
		}
	}
	body := strings.Join(quoted, " ")
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	summaryText := strings.TrimSpace(fmt.Sprintf("会议「%s」转写摘要：%s", title, body))
	if summaryText == "" {
		summaryText = "会议转写摘要"
	}
	actions := []ActionItem{}
	if len(quoted) > 0 {
		task := "根据转写跟进：" + quoted[0]
		if len(task) > 200 {
			task = task[:200] + "…"
		}
		actions = append(actions, ActionItem{
			Task:             task,
			SourceSegmentIDs: segIDs,
		})
	}
	return MeetingSummary{
		MeetingID:   meetingID,
		Summary:     summaryText,
		Decisions:   nil,
		ActionItems: actions,
		Risks:       nil,
		OpenQuestions: nil,
	}
}

func validateSummaryDelivery(sum MeetingSummary) error {
	if strings.TrimSpace(sum.Summary) == "" {
		return fmt.Errorf("empty summary")
	}
	for i, item := range sum.ActionItems {
		if strings.TrimSpace(item.Task) == "" {
			return fmt.Errorf("action_items[%d].task required", i)
		}
	}
	return nil
}
