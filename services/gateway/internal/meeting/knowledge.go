package meeting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"metuai/services/gateway/internal/knowledge"
)

// IndexMeetingKnowledge 把纪要与转写写入检索副本（ACL：组织者 + 已确认录音的员工 + 嘉宾邮箱）。
func IndexMeetingKnowledge(ctx context.Context, repo Repository, idx knowledge.Indexer, meetingID string, guestEmails []string) error {
	if idx == nil {
		return nil
	}
	current, ok := repo.Get(meetingID)
	if !ok {
		return fmt.Errorf("meeting not found")
	}
	allowedUsers := []string{current.OrganizerID}
	if parts, err := repo.ListEmployeeParticipantIDs(meetingID); err == nil {
		for _, uid := range parts {
			if uid == current.OrganizerID {
				continue
			}
			allowedUsers = append(allowedUsers, uid)
		}
	}
	emails := uniqueEmails(guestEmails)
	if stored, err := repo.ListGuestEmails(meetingID); err == nil {
		emails = uniqueEmails(append(emails, stored...))
	}

	if sum, ok := repo.GetSummary(meetingID); ok {
		if err := idx.Upsert(ctx, knowledge.Document{
			MeetingID:          meetingID,
			Title:              current.Title,
			Text:               sum.Summary + " " + strings.Join(sum.Decisions, " "),
			SourceType:         "summary",
			SourceID:           meetingID + ":summary",
			AllowedUserIDs:     allowedUsers,
			AllowedGuestEmails: emails,
			Timestamp:          time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("index summary: %w", err)
		}
	}

	segs, err := repo.ListTranscript(meetingID)
	if err != nil {
		return err
	}
	if len(segs) > 0 {
		var parts []string
		for _, s := range segs {
			parts = append(parts, s.Text)
		}
		text := strings.Join(parts, " ")
		if len(text) > 4000 {
			text = text[:4000]
		}
		if err := idx.Upsert(ctx, knowledge.Document{
			MeetingID:          meetingID,
			Title:              current.Title,
			Text:               text,
			SourceType:         "transcript",
			SourceID:           meetingID + ":transcript",
			AllowedUserIDs:     allowedUsers,
			AllowedGuestEmails: emails,
			Timestamp:          time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("index transcript: %w", err)
		}
	}
	return nil
}

func uniqueEmails(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}
