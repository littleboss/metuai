package meeting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"metuai/services/gateway/internal/knowledge"
)

func knowledgeACL(repo Repository, meetingID string, extraEmails []string) (users, emails []string, title string, err error) {
	current, ok := repo.Get(meetingID)
	if !ok {
		return nil, nil, "", fmt.Errorf("meeting not found")
	}
	users = []string{current.OrganizerID}
	if parts, err := repo.ListEmployeeParticipantIDs(meetingID); err == nil {
		for _, uid := range parts {
			if uid == current.OrganizerID {
				continue
			}
			users = append(users, uid)
		}
	}
	if members, err := repo.ListMembers(meetingID); err == nil {
		seen := map[string]struct{}{}
		for _, uid := range users {
			seen[uid] = struct{}{}
		}
		for _, member := range members {
			if _, ok := seen[member.UserID]; ok {
				continue
			}
			users = append(users, member.UserID)
			seen[member.UserID] = struct{}{}
		}
	}
	emails = uniqueEmails(extraEmails)
	if stored, err := repo.ListGuestEmails(meetingID); err == nil {
		emails = uniqueEmails(append(emails, stored...))
	}
	return users, emails, current.Title, nil
}

func upsertKnowledgeDoc(ctx context.Context, idx knowledge.Indexer, meetingID, title, text, sourceType, sourceID string, users, emails []string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return idx.Upsert(ctx, knowledge.Document{
		MeetingID:          meetingID,
		Title:              title,
		Text:               text,
		SourceType:         sourceType,
		SourceID:           sourceID,
		AllowedUserIDs:     users,
		AllowedGuestEmails: emails,
		Timestamp:          time.Now().UTC(),
	})
}

// IndexMeetingKnowledge 把纪要、决策、待办、转写与聊天写入检索副本。
func IndexMeetingKnowledge(ctx context.Context, repo Repository, idx knowledge.Indexer, meetingID string, guestEmails []string) error {
	if idx == nil {
		return nil
	}
	users, emails, title, err := knowledgeACL(repo, meetingID, guestEmails)
	if err != nil {
		return err
	}

	if sum, ok := repo.GetSummary(meetingID); ok {
		summaryText := sum.Summary
		for _, d := range sum.Decisions {
			summaryText += " " + d.Text
		}
		for _, a := range sum.ActionItems {
			summaryText += " " + a.Task
		}
		if err := upsertKnowledgeDoc(ctx, idx, meetingID, title, summaryText, "summary", meetingID+":summary", users, emails); err != nil {
			return fmt.Errorf("index summary: %w", err)
		}
		for i, d := range sum.Decisions {
			if err := upsertKnowledgeDoc(ctx, idx, meetingID, title, d.Text, "decision", fmt.Sprintf("%s:decision:%d", meetingID, i), users, emails); err != nil {
				return fmt.Errorf("index decision: %w", err)
			}
		}
		for i, a := range sum.ActionItems {
			if err := upsertKnowledgeDoc(ctx, idx, meetingID, title, a.Task, "action_item", fmt.Sprintf("%s:action:%d", meetingID, i), users, emails); err != nil {
				return fmt.Errorf("index action: %w", err)
			}
		}
	}

	segs, err := repo.ListTranscript(meetingID)
	if err != nil {
		return err
	}
	if len(segs) > 0 {
		parts := make([]string, 0, len(segs))
		for _, s := range segs {
			parts = append(parts, s.Text)
		}
		text := strings.Join(parts, " ")
		if len(text) > 4000 {
			text = text[:4000]
		}
		if err := upsertKnowledgeDoc(ctx, idx, meetingID, title, text, "transcript", meetingID+":transcript", users, emails); err != nil {
			return fmt.Errorf("index transcript: %w", err)
		}
	}

	chats, err := repo.ListChat(meetingID)
	if err != nil {
		return err
	}
	if len(chats) > 0 {
		parts := make([]string, 0, len(chats))
		for _, chat := range chats {
			parts = append(parts, chat.DisplayName+": "+chat.Body)
		}
		if err := upsertKnowledgeDoc(ctx, idx, meetingID, title, strings.Join(parts, "\n"), "chat", meetingID+":chat", users, emails); err != nil {
			return fmt.Errorf("index chat: %w", err)
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
