package egress

import (
	"context"
	"fmt"
	"strings"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Egress 生命周期状态（对齐 LiveKit EgressStatus 字符串）。
const (
	StatusStarting = "EGRESS_STARTING"
	StatusActive   = "EGRESS_ACTIVE"
	StatusEnding   = "EGRESS_ENDING"
	StatusComplete = "EGRESS_COMPLETE"
	StatusFailed   = "EGRESS_FAILED"
	StatusAborted  = "EGRESS_ABORTED"
	StatusLimit    = "EGRESS_LIMIT_REACHED"
	StatusUnknown  = "EGRESS_UNKNOWN"
)

type FileType string

const (
	FileOGG FileType = "ogg"
	FileMP4 FileType = "mp4"
)

type OutputSpec struct {
	AudioOnly bool
	VideoOnly bool
	FileType  FileType
	Filepath  string
}

type Handle struct {
	EgressID string
	Status   string
	Error    string
	Files    []string
}

func (h Handle) Terminal() bool {
	switch h.Status {
	case StatusComplete, StatusFailed, StatusAborted, StatusLimit:
		return true
	default:
		return false
	}
}

func (h Handle) Succeeded() bool {
	return h.Status == StatusComplete && h.Error == ""
}

// Client 抽象 LiveKit Egress 的最小能力，便于单测替身。
type Client interface {
	StartRoomComposite(ctx context.Context, room string, spec OutputSpec) (Handle, error)
	StartParticipant(ctx context.Context, room, identity string, spec OutputSpec) (Handle, error)
	ListParticipants(ctx context.Context, room string) ([]string, error)
	Describe(ctx context.Context, egressID string) (Handle, error)
	Stop(ctx context.Context, egressID string) (Handle, error)
}

type liveKitClient struct {
	cfg    Config
	egress *lksdk.EgressClient
	rooms  *lksdk.RoomServiceClient
	layout string
}

func httpHost(livekitURL string) string {
	u := strings.TrimSpace(livekitURL)
	u = strings.Replace(u, "wss://", "https://", 1)
	u = strings.Replace(u, "ws://", "http://", 1)
	return u
}

func NewLiveKitClient(cfg Config) Client {
	host := httpHost(cfg.LiveKitURL)
	return &liveKitClient{
		cfg:    cfg,
		egress: lksdk.NewEgressClient(host, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret),
		rooms:  lksdk.NewRoomServiceClient(host, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret),
		layout: "grid",
	}
}

// NoopClient：Egress 未启用或不可达时的显式降级。
type NoopClient struct{}

func (NoopClient) StartRoomComposite(context.Context, string, OutputSpec) (Handle, error) {
	return Handle{}, fmt.Errorf("egress_unavailable")
}
func (NoopClient) StartParticipant(context.Context, string, string, OutputSpec) (Handle, error) {
	return Handle{}, fmt.Errorf("egress_unavailable")
}
func (NoopClient) ListParticipants(context.Context, string) ([]string, error) {
	return nil, fmt.Errorf("egress_unavailable")
}
func (NoopClient) Describe(context.Context, string) (Handle, error) {
	return Handle{Status: StatusUnknown}, fmt.Errorf("egress_unavailable")
}
func (NoopClient) Stop(context.Context, string) (Handle, error) {
	return Handle{}, fmt.Errorf("egress_unavailable")
}

func (c *liveKitClient) fileOutput(spec OutputSpec) *livekit.EncodedFileOutput {
	fileType := livekit.EncodedFileType_DEFAULT_FILETYPE
	switch spec.FileType {
	case FileOGG:
		fileType = livekit.EncodedFileType_OGG
	case FileMP4:
		fileType = livekit.EncodedFileType_MP4
	}
	region := c.cfg.S3Region
	if region == "" {
		region = "us-east-1"
	}
	return &livekit.EncodedFileOutput{
		FileType: fileType,
		Filepath: spec.Filepath,
		Output: &livekit.EncodedFileOutput_S3{
			S3: &livekit.S3Upload{
				AccessKey:      c.cfg.S3AccessKey,
				Secret:         c.cfg.S3SecretKey,
				Region:         region,
				Endpoint:       c.cfg.S3Endpoint,
				Bucket:         c.cfg.S3Bucket,
				ForcePathStyle: c.cfg.S3ForcePathStyle,
			},
		},
	}
}

func (c *liveKitClient) StartRoomComposite(ctx context.Context, room string, spec OutputSpec) (Handle, error) {
	info, err := c.egress.StartRoomCompositeEgress(ctx, &livekit.RoomCompositeEgressRequest{
		RoomName:    room,
		Layout:      c.layout,
		AudioOnly:   spec.AudioOnly,
		VideoOnly:   spec.VideoOnly,
		FileOutputs: []*livekit.EncodedFileOutput{c.fileOutput(spec)},
	})
	if err != nil {
		return Handle{}, fmt.Errorf("start room composite: %w", err)
	}
	return fromInfo(info), nil
}

func (c *liveKitClient) StartParticipant(ctx context.Context, room, identity string, spec OutputSpec) (Handle, error) {
	info, err := c.egress.StartParticipantEgress(ctx, &livekit.ParticipantEgressRequest{
		RoomName:    room,
		Identity:    identity,
		FileOutputs: []*livekit.EncodedFileOutput{c.fileOutput(spec)},
	})
	if err != nil {
		return Handle{}, fmt.Errorf("start participant egress: %w", err)
	}
	return fromInfo(info), nil
}

func (c *liveKitClient) ListParticipants(ctx context.Context, room string) ([]string, error) {
	resp, err := c.rooms.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	out := make([]string, 0, len(resp.GetParticipants()))
	for _, p := range resp.GetParticipants() {
		if p.GetIdentity() != "" {
			out = append(out, p.GetIdentity())
		}
	}
	return out, nil
}

func (c *liveKitClient) Describe(ctx context.Context, egressID string) (Handle, error) {
	resp, err := c.egress.ListEgress(ctx, &livekit.ListEgressRequest{EgressId: egressID})
	if err != nil {
		return Handle{}, fmt.Errorf("list egress: %w", err)
	}
	for _, info := range resp.GetItems() {
		if info.GetEgressId() == egressID {
			return fromInfo(info), nil
		}
	}
	return Handle{}, fmt.Errorf("egress %s not found", egressID)
}

func (c *liveKitClient) Stop(ctx context.Context, egressID string) (Handle, error) {
	info, err := c.egress.StopEgress(ctx, &livekit.StopEgressRequest{EgressId: egressID})
	if err != nil {
		return Handle{}, fmt.Errorf("stop egress: %w", err)
	}
	return fromInfo(info), nil
}

func fromInfo(info *livekit.EgressInfo) Handle {
	if info == nil {
		return Handle{Status: StatusUnknown}
	}
	files := make([]string, 0, len(info.GetFileResults()))
	for _, f := range info.GetFileResults() {
		location := f.GetLocation()
		if location == "" {
			location = f.GetFilename()
		}
		if location != "" {
			files = append(files, location)
		}
	}
	return Handle{
		EgressID: info.GetEgressId(),
		Status:   info.GetStatus().String(),
		Error:    info.GetError(),
		Files:    files,
	}
}
