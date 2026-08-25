package livekit

import (
	"time"

	"github.com/livekit/protocol/auth"
)

func IssueRoomToken(apiKey, apiSecret, room, identity, name string) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret)
	canPublish := true
	canSubscribe := true
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         room,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
	}
	at.AddGrant(grant).SetIdentity(identity).SetName(name).SetValidFor(2 * time.Hour)
	return at.ToJWT()
}
