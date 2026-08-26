package meeting

import "time"

// 媒体产物种类。前三种来自 LiveKit Egress；local_mic 是员工本机麦克风备份。
const (
	KindParticipantTrack = "participant_track"
	KindRoomAudio        = "room_audio"
	KindRoomVideo        = "room_video"
	KindLocalMic         = "local_mic" // 缺轨时 ASR 可标 source=local_fallback
)

// MediaArtifact 记录一场会计划/已落盘的录制产物（Egress 或本机上传）。
type MediaArtifact struct {
	ID        string    `json:"id"`
	MeetingID string    `json:"meeting_id"`
	Kind      string    `json:"kind"`   // 见上方 Kind* 常量
	Status    string    `json:"status"` // pending | started | ready | failed
	ObjectKey string    `json:"object_key"`
	Detail    string    `json:"detail"`
	EgressID  string    `json:"egress_id"` // 空表示这一路从未真正开录（本机上传也为空）
	CreatedAt time.Time `json:"created_at"`
}
