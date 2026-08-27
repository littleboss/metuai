package meeting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// 公网 LLM 主机名：代码不得把它们当默认端点；配置命中时也拒绝出站。
var publicLLMHosts = map[string]struct{}{
	"api.openai.com":                     {},
	"api.anthropic.com":                  {},
	"generativelanguage.googleapis.com":  {},
	"googleapis.com":                     {},
	"api.google.com":                     {},
}

// notesHTTPClient 可在测试中替换，用于断言未出站公网 LLM。
var notesHTTPClient = &http.Client{Timeout: 60 * time.Second}

// LLMBaseURL 返回私有/内网 LLM 基址（如 http://127.0.0.1:11434/v1）。
// 未设置时纪要生成返回 503，会议其它能力不受影响。
func LLMBaseURL() string {
	return strings.TrimSpace(os.Getenv("LLM_BASE_URL"))
}

// PrivateLLMConfigured 仅当显式配置了私有 LLM 端点时为真。
func PrivateLLMConfigured() bool {
	return LLMBaseURL() != ""
}

func LLMModelName() string {
	if m := strings.TrimSpace(os.Getenv("LLM_MODEL")); m != "" {
		return m
	}
	return "private-llm"
}

func chatCompletionsURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

func rejectPublicLLMHost(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid LLM_BASE_URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("invalid LLM_BASE_URL: empty host")
	}
	if _, blocked := publicLLMHosts[host]; blocked {
		return fmt.Errorf("public LLM host refused: %s", host)
	}
	// 子域：foo.googleapis.com
	for blocked := range publicLLMHosts {
		if strings.HasSuffix(host, "."+blocked) {
			return fmt.Errorf("public LLM host refused: %s", host)
		}
	}
	return nil
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Temperature float64             `json:"temperature"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIChatMessage `json:"message"`
	} `json:"choices"`
}

// llmNotesDraft 是模型应返回的结构化纪要（对齐架构 §6.3）。
type llmNotesDraft struct {
	Summary       string       `json:"summary"`
	Decisions     []CitedItem  `json:"decisions"`
	ActionItems   []ActionItem `json:"action_items"`
	Risks         []CitedItem  `json:"risks"`
	OpenQuestions []CitedItem  `json:"open_questions"`
}

func callPrivateLLM(ctx context.Context, meeting Meeting, segments []TranscriptSegment, allowedOwners []string) (llmNotesDraft, string, error) {
	base := LLMBaseURL()
	if base == "" {
		return llmNotesDraft{}, "", ErrAINotConfigured
	}
	endpoint := chatCompletionsURL(base)
	if err := rejectPublicLLMHost(endpoint); err != nil {
		return llmNotesDraft{}, "", fmt.Errorf("%w: %v", ErrAINotConfigured, err)
	}

	prompt := buildNotesPrompt(meeting, segments, allowedOwners)
	model := LLMModelName()
	body, err := json.Marshal(openAIChatRequest{
		Model: model,
		Messages: []openAIChatMessage{
			{Role: "system", Content: "You are a private meeting notes assistant. Reply with a single JSON object only. Do not invent people or todos not grounded in the transcript. owner_user_id must be empty or one of the provided internal user ids."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
	})
	if err != nil {
		return llmNotesDraft{}, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return llmNotesDraft{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("LLM_API_KEY")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := notesHTTPClient.Do(req)
	if err != nil {
		return llmNotesDraft{}, "", fmt.Errorf("private LLM request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llmNotesDraft{}, "", fmt.Errorf("private LLM HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var chat openAIChatResponse
	if err := json.Unmarshal(raw, &chat); err != nil {
		return llmNotesDraft{}, "", fmt.Errorf("private LLM response: %w", err)
	}
	content := ""
	if len(chat.Choices) > 0 {
		content = chat.Choices[0].Message.Content
	}
	draft, err := parseNotesDraft(content)
	if err != nil {
		return llmNotesDraft{}, "", err
	}
	usedModel := strings.TrimSpace(chat.Model)
	if usedModel == "" {
		usedModel = model
	}
	return draft, usedModel, nil
}

func buildNotesPrompt(meeting Meeting, segments []TranscriptSegment, allowedOwners []string) string {
	var b strings.Builder
	b.WriteString("Meeting title: ")
	b.WriteString(meeting.Title)
	b.WriteString("\nAllowed internal owner_user_id values (or empty): ")
	b.WriteString(strings.Join(allowedOwners, ", "))
	b.WriteString("\nTranscript:\n")
	for _, seg := range segments {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", seg.ID, seg.SpeakerDisplayName, seg.Text)
	}
	b.WriteString("\nReturn JSON with keys: summary (non-empty string), decisions, action_items, risks, open_questions.\n")
	b.WriteString("Each action_item needs task; owner_user_id optional and must be empty or an allowed id.\n")
	return b.String()
}

func parseNotesDraft(content string) (llmNotesDraft, error) {
	content = strings.TrimSpace(content)
	content = stripJSONFence(content)
	if content == "" {
		return llmNotesDraft{}, fmt.Errorf("empty LLM notes content")
	}
	var draft llmNotesDraft
	if err := json.Unmarshal([]byte(content), &draft); err != nil {
		return llmNotesDraft{}, fmt.Errorf("parse LLM notes JSON: %w", err)
	}
	if draft.ActionItems == nil {
		draft.ActionItems = []ActionItem{}
	}
	if draft.Decisions == nil {
		draft.Decisions = []CitedItem{}
	}
	if draft.Risks == nil {
		draft.Risks = []CitedItem{}
	}
	if draft.OpenQuestions == nil {
		draft.OpenQuestions = []CitedItem{}
	}
	return draft, nil
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "json") {
		s = strings.TrimSpace(s[4:])
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func allowedOwnerList(repo Repository, meetingID string) []string {
	ids := internalOwnerIDs(repo, meetingID)
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out
}
