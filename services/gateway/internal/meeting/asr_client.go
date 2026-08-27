package meeting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	// ErrNoAudio 表示缺少可用于转写的权威音源（participant_track / local_mic）。
	ErrNoAudio = errors.New("no_audio")
	// ErrASRNotConfigured 表示私有 ASR 未配置；会议仍可进行，禁止出站公网 ASR。
	ErrASRNotConfigured = errors.New("ASR_NOT_CONFIGURED")
)

// 公网 ASR 主机名：代码不得把它们当默认端点；配置命中时也拒绝出站。
var publicASRHosts = map[string]struct{}{
	"api.openai.com":              {},
	"speech.googleapis.com":     {},
	"googleapis.com":              {},
	"api.google.com":              {},
	"api.deepgram.com":            {},
	"api.assemblyai.com":          {},
	"stt.speech.microsoft.com":    {},
	"api.rev.ai":                  {},
	"api.speechmatics.com":        {},
	"transcribe.us-east-1.amazonaws.com": {},
	"transcribe.amazonaws.com":    {},
}

// asrHTTPClient 可在测试中替换，用于断言未出站公网 ASR。
var asrHTTPClient = &http.Client{Timeout: 120 * time.Second}

// ASRBaseURL 返回私有/内网 ASR 基址（如 http://127.0.0.1:9001）。
// 未设置时转写生成返回 503，会议其它能力不受影响。
func ASRBaseURL() string {
	return strings.TrimSpace(os.Getenv("ASR_BASE_URL"))
}

// PrivateASRConfigured 仅当显式配置了私有 ASR 端点时为真。
func PrivateASRConfigured() bool {
	return ASRBaseURL() != ""
}

func transcribeURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/transcribe") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/transcribe"
	}
	return base + "/v1/transcribe"
}

func rejectPublicASRHost(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid ASR_BASE_URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("invalid ASR_BASE_URL: empty host")
	}
	if _, blocked := publicASRHosts[host]; blocked {
		return fmt.Errorf("public ASR host refused: %s", host)
	}
	for blocked := range publicASRHosts {
		if strings.HasSuffix(host, "."+blocked) {
			return fmt.Errorf("public ASR host refused: %s", host)
		}
	}
	return nil
}

type asrAudioSource struct {
	Kind           string `json:"kind"`
	ObjectKey      string `json:"object_key"`
	AudioURL       string `json:"audio_url,omitempty"`
	ParticipantKey string `json:"participant_key,omitempty"`
}

type asrTranscribeRequest struct {
	MeetingID string           `json:"meeting_id"`
	Title     string           `json:"title"`
	Sources   []asrAudioSource `json:"sources"`
}

type asrTranscribeResponse struct {
	Backend  string           `json:"backend"`
	Segments []ASRResultInput `json:"segments"`
}

// GenerateMeetingTranscript 调用私有 ASR 生成转写并落库。
// 组织者/共同组织者经 HTTP 层校验；此处校验结束态、音源与 ASR 配置。
func GenerateMeetingTranscript(
	ctx context.Context,
	repo Repository,
	meetingID, actorKey string,
	mediaSigner MediaURLSigner,
	s3Bucket string,
) ([]TranscriptSegment, string, error) {
	current, ok := repo.Get(meetingID)
	if !ok {
		return nil, "", fmt.Errorf("meeting not found")
	}
	if !current.Ended {
		return nil, "", ErrMeetingNotEnded
	}

	arts, err := repo.ListMediaArtifacts(meetingID)
	if err != nil {
		return nil, "", err
	}
	if !HasAuthoritativeAudio(arts) {
		return nil, "", ErrNoAudio
	}
	if !PrivateASRConfigured() {
		return nil, "", ErrASRNotConfigured
	}

	sources := buildASRAudioSources(arts, mediaSigner, s3Bucket, ctx)
	if len(sources) == 0 {
		return nil, "", ErrNoAudio
	}

	backend, inputs, err := callPrivateASR(ctx, current, sources)
	if err != nil {
		if errors.Is(err, ErrASRNotConfigured) {
			return nil, "", ErrASRNotConfigured
		}
		return nil, "", err
	}
	stage, err := ApplyASRResult(repo, meetingID, actorKey, backend, inputs)
	if err != nil {
		return nil, "", err
	}
	CompleteOpenPipelineTasks(repo, meetingID, PipelineKindASR)
	segs, err := repo.ListTranscript(meetingID)
	if err != nil {
		return nil, "", err
	}
	return segs, stage, nil
}

func buildASRAudioSources(
	arts []MediaArtifact,
	mediaSigner MediaURLSigner,
	s3Bucket string,
	ctx context.Context,
) []asrAudioSource {
	selected := iterAuthoritativeAudioSources(arts)
	out := make([]asrAudioSource, 0, len(selected))
	for _, art := range selected {
		src := asrAudioSource{
			Kind:           art.Kind,
			ObjectKey:      art.ObjectKey,
			ParticipantKey: art.ParticipantKey,
		}
		if mediaSigner != nil && art.ObjectKey != "" {
			key := stripBucketPrefix(art.ObjectKey, s3Bucket)
			if signed, err := mediaSigner.SignGetURL(ctx, key, 15*time.Minute); err == nil {
				src.AudioURL = signed
			}
		}
		out = append(out, src)
	}
	return out
}

// iterAuthoritativeAudioSources 逐参会人选择权威音源：独立音轨优先，缺轨再用本机备份。房间混音永不入选。
func iterAuthoritativeAudioSources(arts []MediaArtifact) []MediaArtifact {
	ready := make([]MediaArtifact, 0, len(arts))
	for _, art := range arts {
		if art.Status == "ready" && strings.TrimSpace(art.ObjectKey) != "" {
			ready = append(ready, art)
		}
	}
	tracks := make([]MediaArtifact, 0)
	local := make([]MediaArtifact, 0)
	for _, art := range ready {
		switch art.Kind {
		case KindParticipantTrack:
			tracks = append(tracks, art)
		case KindLocalMic:
			local = append(local, art)
		}
	}
	covered := map[string]struct{}{}
	out := make([]MediaArtifact, 0, len(tracks)+len(local))
	for _, art := range tracks {
		out = append(out, art)
		key := strings.TrimSpace(art.ParticipantKey)
		if key == "" {
			key = strings.TrimSpace(art.ObjectKey)
		}
		if key != "" {
			covered[key] = struct{}{}
		}
	}
	for _, art := range local {
		key := strings.TrimSpace(art.ParticipantKey)
		if key != "" {
			if _, ok := covered[key]; ok {
				continue
			}
			covered[key] = struct{}{}
		}
		out = append(out, art)
	}
	return out
}

func callPrivateASR(ctx context.Context, meeting Meeting, sources []asrAudioSource) (string, []ASRResultInput, error) {
	base := ASRBaseURL()
	if base == "" {
		return "", nil, ErrASRNotConfigured
	}
	endpoint := transcribeURL(base)
	if err := rejectPublicASRHost(endpoint); err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrASRNotConfigured, err)
	}

	body, err := json.Marshal(asrTranscribeRequest{
		MeetingID: meeting.ID,
		Title:     meeting.Title,
		Sources:   sources,
	})
	if err != nil {
		return "", nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("ASR_API_KEY")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := asrHTTPClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("private ASR request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("private ASR HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var out asrTranscribeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, fmt.Errorf("private ASR response: %w", err)
	}
	if len(out.Segments) == 0 {
		return "", nil, fmt.Errorf("private ASR returned no segments")
	}
	backend := strings.TrimSpace(out.Backend)
	if backend == "" {
		backend = "private-asr"
	}
	return backend, out.Segments, nil
}
