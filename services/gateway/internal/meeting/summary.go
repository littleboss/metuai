package meeting

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrSummaryNotReady     = errors.New("summary_not_ready")
	ErrOwnerMustBeInternal = errors.New("owner_must_be_internal")
)

func captureOriginalJSON(summary MeetingSummary) string {
	draft := struct {
		Summary       string       `json:"summary"`
		Decisions     []CitedItem  `json:"decisions"`
		ActionItems   []ActionItem `json:"action_items"`
		Risks         []CitedItem  `json:"risks"`
		OpenQuestions []CitedItem  `json:"open_questions"`
	}{
		Summary:       summary.Summary,
		Decisions:     summary.Decisions,
		ActionItems:   summary.ActionItems,
		Risks:         summary.Risks,
		OpenQuestions: summary.OpenQuestions,
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return ""
	}
	return string(raw)
}

func internalOwnerIDs(repo Repository, meetingID string) map[string]struct{} {
	allowed := map[string]struct{}{}
	if current, ok := repo.Get(meetingID); ok {
		allowed[current.OrganizerID] = struct{}{}
	}
	if members, err := repo.ListMembers(meetingID); err == nil {
		for _, member := range members {
			if member.UserID != "" {
				allowed[member.UserID] = struct{}{}
			}
		}
	}
	if parts, err := repo.ListEmployeeParticipantIDs(meetingID); err == nil {
		for _, uid := range parts {
			allowed[uid] = struct{}{}
		}
	}
	return allowed
}

func validateActionOwners(repo Repository, meetingID string, items []ActionItem) error {
	allowed := internalOwnerIDs(repo, meetingID)
	for _, item := range items {
		owner := strings.TrimSpace(item.OwnerUserID)
		if owner == "" {
			continue
		}
		if _, ok := allowed[owner]; !ok {
			return fmt.Errorf("%w", ErrOwnerMustBeInternal)
		}
	}
	return nil
}

// ApplySummaryEdit 让内部参会人修订纪要；AI 原稿保留，修订只追加。
func ApplySummaryEdit(repo Repository, meetingID, actorKey string, next MeetingSummary) (MeetingSummary, error) {
	current, ok := repo.GetSummary(meetingID)
	if !ok {
		return MeetingSummary{}, ErrSummaryNotReady
	}
	if err := validateActionOwners(repo, meetingID, next.ActionItems); err != nil {
		return MeetingSummary{}, err
	}
	now := time.Now().UTC()
	next.MeetingID = meetingID
	next.CreatedAt = current.CreatedAt
	next.OriginalJSON = current.OriginalJSON
	if next.OriginalJSON == "" {
		next.OriginalJSON = captureOriginalJSON(current)
	}
	next.RevisedAt = &now
	patch, _ := json.Marshal(struct {
		Summary       string       `json:"summary"`
		Decisions     []CitedItem  `json:"decisions"`
		ActionItems   []ActionItem `json:"action_items"`
		Risks         []CitedItem  `json:"risks"`
		OpenQuestions []CitedItem  `json:"open_questions"`
	}{next.Summary, next.Decisions, next.ActionItems, next.Risks, next.OpenQuestions})
	if err := repo.UpsertSummary(next); err != nil {
		return MeetingSummary{}, err
	}
	if _, err := repo.AppendSummaryRevision(SummaryRevision{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		PatchJSON: string(patch),
		CreatedAt: now,
	}); err != nil {
		return MeetingSummary{}, err
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "summary_revised",
		Detail:    fmt.Sprintf("decisions=%d actions=%d", len(next.Decisions), len(next.ActionItems)),
	})
	return next, nil
}

var (
	ErrActionIndexInvalid = errors.New("action_index_invalid")
	ErrActionAlreadyDone  = errors.New("action_already_completed")
)

// CompleteActionItem 把一条待办标为完成（架构 §8.2：创建/修改/完成待办）。
func CompleteActionItem(repo Repository, meetingID, actorKey string, index int) (MeetingSummary, error) {
	current, ok := repo.GetSummary(meetingID)
	if !ok {
		return MeetingSummary{}, ErrSummaryNotReady
	}
	if index < 0 || index >= len(current.ActionItems) {
		return MeetingSummary{}, ErrActionIndexInvalid
	}
	if current.ActionItems[index].CompletedAt != nil {
		return current, ErrActionAlreadyDone
	}
	now := time.Now().UTC()
	current.ActionItems[index].CompletedAt = &now
	current.RevisedAt = &now
	if err := repo.UpsertSummary(current); err != nil {
		return MeetingSummary{}, err
	}
	patch, _ := json.Marshal(map[string]any{
		"complete_index": index,
		"task":           current.ActionItems[index].Task,
	})
	if _, err := repo.AppendSummaryRevision(SummaryRevision{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		PatchJSON: string(patch),
		CreatedAt: now,
	}); err != nil {
		return MeetingSummary{}, err
	}
	_ = repo.AppendAudit(AuditEvent{
		MeetingID: meetingID,
		ActorKey:  actorKey,
		Action:    "action_item_completed",
		Detail:    current.ActionItems[index].Task,
	})
	return current, nil
}
