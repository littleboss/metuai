package meeting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubPrivateLLM 启动 OpenAI 兼容的私有 LLM httptest，并设置 LLM_BASE_URL。
// notesJSON 是 message.content 里的纪要 JSON 字符串。
func stubPrivateLLM(t *testing.T, notesJSON string) *httptest.Server {
	t.Helper()
	if notesJSON == "" {
		notesJSON = `{"summary":"会议转写摘要（私有 LLM stub）","decisions":[],"action_items":[{"task":"跟进转写中的事项"}],"risks":[],"open_questions":[]}`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		payload := map[string]any{
			"model": "stub-private-llm",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": notesJSON}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LLM_BASE_URL", srv.URL+"/v1")
	return srv
}

// recordingRoundTripper 记录出站 Host，用于断言未访问公网 LLM。
type recordingRoundTripper struct {
	hosts []string
	next  http.RoundTripper
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Host)
	if r.next != nil {
		return r.next.RoundTrip(req)
	}
	return (&http.Transport{}).RoundTrip(req)
}
