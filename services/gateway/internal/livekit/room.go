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

// RemoveParticipant 从房间踢出指定 identity；房间不存在或人不在房时视为成功。
func RemoveParticipant(ctx context.Context, livekitURL, apiKey, apiSecret, room, identity string) error {
	client := lksdk.NewRoomServiceClient(httpHost(livekitURL), apiKey, apiSecret)
	_, err := client.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{
		Room:     room,
		Identity: identity,
	})
	if err == nil {
		return nil
	}
	msg := err.Error()
	// 开发环境常见：房间尚未创建或参会人已离开。
	if strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist") {
		return nil
	}
	return fmt.Errorf("remove participant: %w", err)
}
