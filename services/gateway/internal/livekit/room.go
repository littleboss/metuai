package livekit

import (
	"context"
	"fmt"
	"strings"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// httpHost 把 ws(s):// 换成 http(s)://，供 RoomService 使用。
func httpHost(livekitURL string) string {
	u := strings.TrimSpace(livekitURL)
	u = strings.Replace(u, "wss://", "https://", 1)
	u = strings.Replace(u, "ws://", "http://", 1)
	return u
}

// ParticipantCount 返回房间当前在线人数。房间不存在视为 0 人，不报错。
func ParticipantCount(ctx context.Context, livekitURL, apiKey, apiSecret, room string) (int, error) {
	client := lksdk.NewRoomServiceClient(httpHost(livekitURL), apiKey, apiSecret)
	listed, err := client.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") {
			return 0, nil
		}
		return 0, fmt.Errorf("list participants: %w", err)
	}
	return len(listed.GetParticipants()), nil
}

// RemoveParticipant 从房间踢出指定 identity；房间不存在或人不在房时视为成功。
func RemoveParticipant(ctx context.Context, livekitURL, apiKey, apiSecret, room, identity string) error {
	client := lksdk.NewRoomServiceClient(httpHost(livekitURL), apiKey, apiSecret)
	return removeOne(ctx, client, room, identity)
}

// RemoveByUserKey 踢出该用户所有设备连接（多端观看时踢人应整户离开）。
func RemoveByUserKey(ctx context.Context, livekitURL, apiKey, apiSecret, room, identity string) error {
	client := lksdk.NewRoomServiceClient(httpHost(livekitURL), apiKey, apiSecret)
	userKey := UserKey(identity)
	listed, err := client.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") {
			return removeOne(ctx, client, room, userKey)
		}
		_ = removeOne(ctx, client, room, identity)
		_ = removeOne(ctx, client, room, userKey)
		return fmt.Errorf("list participants: %w", err)
	}
	var first error
	for _, p := range listed.GetParticipants() {
		if UserKey(p.GetIdentity()) != userKey {
			continue
		}
		if err := removeOne(ctx, client, room, p.GetIdentity()); err != nil && first == nil {
			first = err
		}
	}
	if err := removeOne(ctx, client, room, userKey); err != nil && first == nil {
		first = err
	}
	return first
}

func removeOne(ctx context.Context, client *lksdk.RoomServiceClient, room, identity string) error {
	if identity == "" {
		return nil
	}
	_, err := client.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
		Room:     room,
		Identity: identity,
	})
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") {
		return nil
	}
	return fmt.Errorf("remove participant: %w", err)
}
