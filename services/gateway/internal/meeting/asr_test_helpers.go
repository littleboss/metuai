package meeting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubPrivateASR 启动私有 ASR httptest，并设置 ASR_BASE_URL。
// segmentsJSON 是响应体 segments 数组的 JSON 字符串。
func stubPrivateASR(t *testing.T, segmentsJSON string) *httptest.Server {
	t.Helper()
	if segmentsJSON == "" {
		segmentsJSON = `[{"track_id":"asr-1","speaker_user_id":"u-1","speaker_display_name":"Alice","language":"zh-CN","start_ms":0,"end_ms":1200,"text":"私有 ASR 转写片段","asr_model":"stub-private-asr","source":"egress"}]`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var segments []ASRResultInput
		if err := json.Unmarshal([]byte(segmentsJSON), &segments); err != nil {
			http.Error(w, "bad segments fixture", http.StatusInternalServerError)
			return
		}
		payload := map[string]any{
			"backend":  "stub-private-asr",
			"segments": segments,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ASR_BASE_URL", srv.URL)
	return srv
}
