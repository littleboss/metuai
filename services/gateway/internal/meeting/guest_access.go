package meeting

import (
	"context"
	"strings"

	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/knowledge"
)

func grantVerifiedGuestAccess(
	ctx context.Context,
	repo Repository,
	knowledgeIdx knowledge.Indexer,
	guestSecret []byte,
	challenge GuestEmailChallenge,
	displayName, source string,
) (string, error) {
	if source == guestEmailSourceShared {
		if err := repo.AddGuestEmailSource(challenge.MeetingID, challenge.Email, guestEmailSourceShared); err != nil {
			return "", err
		}
	} else if err := repo.AddGuestEmail(challenge.MeetingID, challenge.Email); err != nil {
		return "", err
	}
	_ = repo.AckRecording(challenge.MeetingID, PrincipalKey(identity.KindGuest, "", challenge.GuestID))
	if err := MergeGuestIdentity(repo, challenge.MeetingID, challenge.GuestID, challenge.Email); err != nil {
		return "", err
	}
	if knowledgeIdx != nil {
		if err := IndexMeetingKnowledge(ctx, repo, knowledgeIdx, challenge.MeetingID, nil); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = strings.Split(challenge.Email, "@")[0]
	}
	return identity.IssueGuestSession(identity.Principal{
		Kind:        identity.KindGuest,
		GuestID:     challenge.GuestID,
		MeetingID:   challenge.MeetingID,
		DisplayName: displayName,
		Email:       challenge.Email,
	}, guestSecret, verifiedGuestSessionTTL)
}
