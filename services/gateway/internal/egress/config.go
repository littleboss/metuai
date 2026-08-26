// Package egress 编排 LiveKit Egress（服务端录制）写入 S3/MinIO。
// 真实调用需要 livekit-server + redis + livekit/egress 三个容器都在跑；
// 任一缺失时上层必须优雅降级，媒体保持 pending，绝不假装 ready。
package egress

import "strings"

// Config 描述服务端录制写入对象存储所需的环境。
type Config struct {
	LiveKitURL       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	S3Endpoint       string
	S3Region         string
	S3Bucket         string
	S3AccessKey      string
	S3SecretKey      string
	S3ForcePathStyle bool
}

// 媒体产物种类，与 media_artifacts.kind 一一对应。
const (
	KindParticipantTrack = "participant_track"
	KindRoomAudio        = "room_audio"
	KindRoomVideo        = "room_video"
)

// DesiredOutputs 对齐架构：独立音轨 + 房间混音 + 房间画面（含共享）。
type DesiredOutputs struct {
	ParticipantTracks  bool // 每人独立音轨（转写权威音源）
	RoomCompositeAudio bool
	RoomCompositeVideo bool
}

func DefaultDesiredOutputs() DesiredOutputs {
	return DesiredOutputs{
		ParticipantTracks:  true,
		RoomCompositeAudio: true,
		RoomCompositeVideo: true,
	}
}

// Ready 表示对象存储与 LiveKit 凭证是否齐全，可供后续 Egress 客户端使用。
func (c Config) Ready() bool {
	return c.LiveKitURL != "" &&
		c.LiveKitAPIKey != "" &&
		c.LiveKitAPISecret != "" &&
		c.S3Endpoint != "" &&
		c.S3Bucket != "" &&
		c.S3AccessKey != "" &&
		c.S3SecretKey != ""
}

// UsesLoopbackS3 检测常见误配：宿主机上 EGRESS_ENABLED=true 却把
// S3_ENDPOINT 写成 127.0.0.1。那个地址是 egress 容器自己，不是 MinIO。
func (c Config) UsesLoopbackS3() bool {
	ep := strings.ToLower(c.S3Endpoint)
	return strings.Contains(ep, "127.0.0.1") || strings.Contains(ep, "localhost")
}

// PlannedKind 是结束会议时写入 PG 的产物种类。
type PlannedKind struct {
	Kind      string
	ObjectKey string
	Detail    string
}

// ObjectPath 是桶内路径（Egress 的 filepath 用它，不含桶名）。
func ObjectPath(meetingID, name string) string {
	return meetingID + "/" + name
}

// RoomAudioPath / RoomVideoPath / ParticipantPath 集中定义命名规则，
// 避免「计划的对象键」与「真正下发给 Egress 的 filepath」漂移。
func RoomAudioPath(meetingID string) string { return ObjectPath(meetingID, "room-audio.ogg") }

func RoomVideoPath(meetingID string) string { return ObjectPath(meetingID, "room-video.mp4") }

func ParticipantPath(meetingID, identity string) string {
	return ObjectPath(meetingID, "tracks/"+identity+".ogg")
}

// PlanObjectKeys 按会议 ID 生成预期对象键（Egress 不可用时先落 pending 元数据）。
func PlanObjectKeys(meetingID string, bucket string, out DesiredOutputs) []PlannedKind {
	plans := make([]PlannedKind, 0, 3)
	if out.ParticipantTracks {
		plans = append(plans, PlannedKind{
			Kind:      KindParticipantTrack,
			ObjectKey: bucket + "/" + ObjectPath(meetingID, "tracks/"),
			Detail:    "pending LiveKit track egress",
		})
	}
	if out.RoomCompositeAudio {
		plans = append(plans, PlannedKind{
			Kind:      KindRoomAudio,
			ObjectKey: bucket + "/" + RoomAudioPath(meetingID),
			Detail:    "pending LiveKit room composite audio",
		})
	}
	if out.RoomCompositeVideo {
		plans = append(plans, PlannedKind{
			Kind:      KindRoomVideo,
			ObjectKey: bucket + "/" + RoomVideoPath(meetingID),
			Detail:    "pending LiveKit room composite video",
		})
	}
	return plans
}
