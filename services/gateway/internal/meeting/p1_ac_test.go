package meeting

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P1-1: 有转写 + 私有 LLM（httptest stub）→ generate 200，GET summary 非空，action_items 为数组。
func TestP1_1_GenerateWithTranscriptAndPrivateLLM(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	notes := `{"summary":"讨论了 PoC 交付","decisions":[],"action_items":[{"task":"下周交付 PoC","owner_user_id":"u-1"}],"risks":[],"open_questions":[]}`
	stubPrivateLLM(t, notes)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if err := store.ReplaceTranscript(id, []TranscriptSegment{{
		MeetingID: id, ID: "seg-1", Text: "下周交付 PoC", SpeakerUserID: "u-1", SpeakerDisplayName: "Alice",
	}}); err != nil {
		t.Fatal(err)
	}

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/summary/generate", emp, "")
	if gen.Code != http.StatusOK && gen.Code != http.StatusAccepted {
		t.Fatalf("generate want 200/202 got %d %s", gen.Code, gen.Body.String())
	}
	if gen.Code == http.StatusAccepted {
		for i := 0; i < 60; i++ {
			got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
			if got.Code == http.StatusOK {
				break
			}
			if i == 59 {
				t.Fatalf("GET summary not ready within 60s: %d %s", got.Code, got.Body.String())
			}
		}
	}

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET want 200 got %d %s", got.Code, got.Body.String())
	}
	var sum MeetingSummary
	if err := json.Unmarshal(got.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(sum.Summary) == "" {
		t.Fatal("summary must be non-empty")
	}
	if sum.ActionItems == nil {
		t.Fatal("action_items must be array")
	}
	if sum.OriginalJSON == "" {
		t.Fatal("original_json required")
	}
	if sum.Model == "" {
		t.Fatal("model required")
	}
}

// P1-2: 无转写 → generate 422；不新建 summary 行；GET 仍 404。
func TestP1_2_GenerateNoTranscript(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/summary/generate", emp, "")
	if gen.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d %s", gen.Code, gen.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(gen.Body.Bytes(), &body)
	if body.Error != "no_transcript" {
		t.Fatalf("body %+v", body)
	}
	if _, ok := store.GetSummary(id); ok {
		t.Fatal("must not create summary row")
	}
	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
	if got.Code != http.StatusNotFound {
		t.Fatalf("GET want 404 got %d", got.Code)
	}
}

// P1-3: 未设置 LLM_BASE_URL → 503；断言无出站到公网 LLM 主机。
func TestP1_3_UnsetLLMBaseURLNoPublicOutbound(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	t.Setenv("LLM_BASE_URL", "")
	rec := &recordingRoundTripper{}
	prev := notesHTTPClient
	notesHTTPClient = &http.Client{Transport: rec}
	t.Cleanup(func() { notesHTTPClient = prev })

	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if err := store.ReplaceTranscript(id, []TranscriptSegment{{
		MeetingID: id, Text: "有转写但无 LLM",
	}}); err != nil {
		t.Fatal(err)
	}

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/summary/generate", emp, "")
	if gen.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d %s", gen.Code, gen.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(gen.Body.Bytes(), &body)
	if body.Error != "AI_NOT_CONFIGURED" {
		t.Fatalf("body %+v", body)
	}
	for _, host := range rec.hosts {
		h := strings.ToLower(host)
		for blocked := range publicLLMHosts {
			if h == blocked || strings.HasSuffix(h, "."+blocked) {
				t.Fatalf("must not call public LLM host %q; seen %v", host, rec.hosts)
			}
		}
	}
	if len(rec.hosts) != 0 {
		t.Fatalf("expected zero outbound LLM calls when unset, got %v", rec.hosts)
	}
}

// P1-4: 非组织者 generate → 403；无令牌 → 401。
func TestP1_4_GenerateAuthz(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-org", "Org")
	other := employeeJWTFor(t, secretEmp, "u-other", "Other")
	id, _ := createMeeting(t, r, organizer)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	_ = store.ReplaceTranscript(id, []TranscriptSegment{{MeetingID: id, Text: "hi"}})

	denied := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/summary/generate", other, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-organizer want 403 got %d %s", denied.Code, denied.Body.String())
	}
	noTok := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/summary/generate", "", "")
	if noTok.Code != http.StatusUnauthorized {
		t.Fatalf("no token want 401 got %d %s", noTok.Code, noTok.Body.String())
	}
}

// P1-5: LLM 返回非本场内部 owner_user_id → 400 owner_must_be_internal。
func TestP1_5_BadOwnerMustBeInternal(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	notes := `{"summary":"摘要","decisions":[],"action_items":[{"task":"坏事","owner_user_id":"stranger-not-in-meeting"}],"risks":[],"open_questions":[]}`
	stubPrivateLLM(t, notes)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	_ = store.ReplaceTranscript(id, []TranscriptSegment{{MeetingID: id, Text: "内容"}})

	gen := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/summary/generate", emp, "")
	if gen.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", gen.Code, gen.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(gen.Body.Bytes(), &body)
	if body.Error != "owner_must_be_internal" {
		t.Fatalf("body %+v", body)
	}
	if _, ok := store.GetSummary(id); ok {
		t.Fatal("must not store summary with bad owner")
	}
}

// P1-6: 用户路径 apps/web 不再暴露 run-fake / 假纪要按钮。
func TestP1_6_NoRunFakeOnUserPath(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "apps", "web", "src")
	entries := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx":
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			text := string(raw)
			if strings.Contains(text, "pipeline/run-fake") || strings.Contains(text, "runFakePipeline") {
				entries = append(entries, path)
			}
			if strings.Contains(text, "pipeline/run-asr-stub") || strings.Contains(text, "runAsrStub") {
				entries = append(entries, path)
			}
			if strings.Contains(text, "假纪要") || strings.Contains(strings.ToLower(text), "fake-summary") {
				entries = append(entries, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk web src: %v", err)
	}
	if len(entries) > 0 {
		t.Fatalf("user path must not retain run-fake / run-asr-stub / fake-summary: %v", entries)
	}
}
