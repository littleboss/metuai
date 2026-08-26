package egress

import (
	"context"
	"fmt"
	"time"
)

// 一路 Egress 的启动结果，直接对应 media_artifacts.status 的取值。
const (
	OutcomeStarted = "started"
	OutcomeFailed  = "failed"
	OutcomePending = "pending"
)

// Started 是「尝试启动某一路录制」的结果。Outcome=pending 表示这条路径
// 本轮没能真正开录（例如房里还没人），必须保持 pending 等后续处理。
type Started struct {
	Kind      string
	ObjectKey string // 含桶名，便于直接展示给前端
	EgressID  string
	Outcome   string
	Detail    string
}

// Manager 把「一场会议」翻译成若干路 LiveKit Egress。
type Manager struct {
	cfg             Config
	client          Client
	outputs         DesiredOutputs
	finalizeTimeout time.Duration
	pollInterval    time.Duration
}

// NewManager 构造编排器。client 为 nil 时 Enabled() 恒为 false。
func NewManager(cfg Config, client Client, finalizeTimeout time.Duration) *Manager {
	if finalizeTimeout < 0 {
		finalizeTimeout = 0
	}
	return &Manager{
		cfg:             cfg,
		client:          client,
		outputs:         DefaultDesiredOutputs(),
		finalizeTimeout: finalizeTimeout,
		pollInterval:    500 * time.Millisecond,
	}
}

// Enabled 表示凭证齐全且客户端已注入；不代表 egress 容器一定可达。
func (m *Manager) Enabled() bool {
	if m == nil || m.client == nil {
		return false
	}
	return m.cfg.Ready()
}

func (m *Manager) bucket() string {
	if m.cfg.S3Bucket == "" {
		return "metuai-media"
	}
	return m.cfg.S3Bucket
}

func (m *Manager) qualify(path string) string {
	return m.bucket() + "/" + path
}

// Start 为一场会议拉起服务端录制。返回值逐路描述成败，调用方按 Outcome 落库。
// 任何一路失败都不会影响其他路，也不会伪造成功。
//
// 房里还没人时：只返回 pending，**不要** StartRoomComposite——空房 Chrome 会长期卡在
// EGRESS_STARTING，Docker Desktop 上尤其明显。等有人进房后再由 EnsureStarted 重试。
func (m *Manager) Start(ctx context.Context, meetingID string) []Started {
	if !m.Enabled() {
		return nil
	}
	identities, err := m.client.ListParticipants(ctx, meetingID)
	if err != nil || len(identities) == 0 {
		detail := "waiting for first participant in livekit room"
		if err != nil {
			detail = "list participants: " + err.Error()
		}
		return m.pendingAll(meetingID, detail)
	}

	out := make([]Started, 0, 3)
	if m.outputs.RoomCompositeAudio {
		out = append(out, m.startComposite(ctx, meetingID, KindRoomAudio, OutputSpec{
			AudioOnly: true,
			FileType:  FileOGG,
			Filepath:  RoomAudioPath(meetingID),
		}))
	}
	if m.outputs.RoomCompositeVideo {
		out = append(out, m.startComposite(ctx, meetingID, KindRoomVideo, OutputSpec{
			FileType: FileMP4,
			Filepath: RoomVideoPath(meetingID),
		}))
	}
	if m.outputs.ParticipantTracks {
		out = append(out, m.startParticipantTracks(ctx, meetingID, identities)...)
	}
	return out
}

func (m *Manager) pendingAll(meetingID, detail string) []Started {
	plans := PlanObjectKeys(meetingID, m.bucket(), m.outputs)
	out := make([]Started, 0, len(plans))
	for _, p := range plans {
		out = append(out, Started{
			Kind:      p.Kind,
			ObjectKey: p.ObjectKey,
			Outcome:   OutcomePending,
			Detail:    detail,
		})
	}
	return out
}

func (m *Manager) startComposite(ctx context.Context, meetingID, kind string, spec OutputSpec) Started {
	handle, err := m.client.StartRoomComposite(ctx, meetingID, spec)
	if err != nil {
		return Started{
			Kind:      kind,
			ObjectKey: m.qualify(spec.Filepath),
			Outcome:   OutcomeFailed,
			Detail:    "egress start failed: " + err.Error(),
		}
	}
	return Started{
		Kind:      kind,
		ObjectKey: m.qualify(spec.Filepath),
		EgressID:  handle.EgressID,
		Outcome:   OutcomeStarted,
		Detail:    fmt.Sprintf("livekit egress %s (%s)", handle.EgressID, handle.Status),
	}
}

// startParticipantTracks 为给定 identity 列表开独立音轨。
// 后加入的人由 StartMissingParticipantTracks（心跳补开）处理。
func (m *Manager) startParticipantTracks(ctx context.Context, meetingID string, identities []string) []Started {
	if len(identities) == 0 {
		return []Started{{
			Kind:      KindParticipantTrack,
			ObjectKey: m.qualify(ObjectPath(meetingID, "tracks/")),
			Outcome:   OutcomePending,
			Detail:    "participant egress deferred: room empty at start",
		}}
	}
	out := make([]Started, 0, len(identities))
	for _, identity := range identities {
		path := ParticipantPath(meetingID, identity)
		handle, err := m.client.StartParticipant(ctx, meetingID, identity, OutputSpec{
			AudioOnly: true,
			FileType:  FileOGG,
			Filepath:  path,
		})
		if err != nil {
			out = append(out, Started{
				Kind:      KindParticipantTrack,
				ObjectKey: m.qualify(path),
				Outcome:   OutcomeFailed,
				Detail:    "egress start failed for " + identity + ": " + err.Error(),
			})
			continue
		}
		out = append(out, Started{
			Kind:      KindParticipantTrack,
			ObjectKey: m.qualify(path),
			EgressID:  handle.EgressID,
			Outcome:   OutcomeStarted,
			Detail:    fmt.Sprintf("livekit egress %s for %s (%s)", handle.EgressID, identity, handle.Status),
		})
	}
	return out
}

// StartMissingParticipantTracks 为「已开房间录制之后才进房」的人补开独立音轨。
// existingKeys 是已落库的 object_key（含桶前缀），命中则跳过，避免重复 StartParticipant。
func (m *Manager) StartMissingParticipantTracks(ctx context.Context, meetingID string, existingKeys []string) []Started {
	if !m.Enabled() || !m.outputs.ParticipantTracks {
		return nil
	}
	identities, err := m.client.ListParticipants(ctx, meetingID)
	if err != nil || len(identities) == 0 {
		return nil
	}
	skip := make(map[string]struct{}, len(existingKeys))
	for _, k := range existingKeys {
		if k != "" {
			skip[k] = struct{}{}
		}
	}
	need := make([]string, 0, len(identities))
	for _, identity := range identities {
		key := m.qualify(ParticipantPath(meetingID, identity))
		if _, ok := skip[key]; ok {
			continue
		}
		need = append(need, identity)
	}
	if len(need) == 0 {
		return nil
	}
	return m.startParticipantTracks(ctx, meetingID, need)
}

// FinalizeOne 停止一路 Egress 并在超时预算内轮询终态。
// 拿不到终态时返回非终态 Handle，调用方必须保持 started 而不是标 ready。
func (m *Manager) FinalizeOne(ctx context.Context, egressID string) Handle {
	if !m.Enabled() {
		return Handle{EgressID: egressID, Status: StatusUnknown, Error: "egress disabled"}
	}
	handle, err := m.client.Stop(ctx, egressID)
	if err != nil {
		// 可能已经自然结束；再查一次拿真实终态，查不到才算未知。
		described, describeErr := m.client.Describe(ctx, egressID)
		if describeErr != nil {
			return Handle{EgressID: egressID, Status: StatusUnknown, Error: err.Error()}
		}
		handle = described
	}
	if handle.Terminal() {
		return handle
	}
	deadline := time.Now().Add(m.finalizeTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(m.pollInterval)
		described, err := m.client.Describe(ctx, egressID)
		if err != nil {
			continue
		}
		handle = described
		if handle.Terminal() {
			return handle
		}
	}
	return handle
}
