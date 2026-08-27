package meeting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/ready"
)

// AC2: GET /readyz 仅在密钥已设且数据库可达时 200；否则 503 + missing。
func TestAC2_ReadyzRequiresSecretAndDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ready.HandleReadyz(&ready.Checker{
		EmployeeSecretSet: false,
		GuestSecretSet:    false,
		DatabaseURL:       "",
	}))
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz want 200 got %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz want 503 got %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Error   string   `json:"error"`
		Message string   `json:"message"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "not_ready" || body.Message == "" {
		t.Fatalf("body %+v", body)
	}
	joined := strings.Join(body.Missing, ",")
	if !strings.Contains(joined, "EMPLOYEE_JWT_SECRET") || !strings.Contains(joined, "DATABASE_URL") {
		t.Fatalf("missing=%v", body.Missing)
	}

	okChecker := ready.AlwaysReady()
	r2 := gin.New()
	r2.GET("/readyz", ready.HandleReadyz(okChecker))
	w = httptest.NewRecorder()
	r2.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready ok want 200 got %d %s", w.Code, w.Body.String())
	}
}

// AC9: 未设置 JWT 密钥时 /readyz=503（missing 含 EMPLOYEE_JWT_SECRET），入会 API 失败关闭。
func TestAC9_MissingJWTSecretReadyzAndFailClosed(t *testing.T) {
	t.Setenv("EMPLOYEE_JWT_SECRET", "")
	t.Setenv("GUEST_JWT_SECRET", "")
	t.Setenv("DATABASE_URL", "postgres://metuai:metuai@127.0.0.1:1/metuai?sslmode=disable")

	checker := ready.FromEnv()
	// 数据库探测用假 Ping，只验证密钥缺失语义。
	checker.Ping = func(context.Context) error { return nil }
	checker.DatabaseURL = "postgres://ok"

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ready.HandleReadyz(checker))
	store := NewMemoryStore()
	RegisterRoutes(r, store, nil, nil, "ws://127.0.0.1:17880", "devkey", "secret", true, "metuai-media", nil, nil, NewBreakGlassStore(), NewGuestEmailVerifier(store, nil), nil, checker)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz want 503 got %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Error   string   `json:"error"`
		Missing []string `json:"missing"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	joined := strings.Join(body.Missing, ",")
	if body.Error != "not_ready" || !strings.Contains(joined, "EMPLOYEE_JWT_SECRET") {
		t.Fatalf("body %+v", body)
	}
	if !strings.Contains(joined, "GUEST_JWT_SECRET") {
		t.Fatalf("missing should include GUEST_JWT_SECRET: %v", body.Missing)
	}

	create := doJSON(t, r, http.MethodPost, "/v1/meetings", "unused", `{"title":"x"}`)
	if create.Code != http.StatusServiceUnavailable {
		t.Fatalf("create want 503 got %d %s", create.Code, create.Body.String())
	}
	guest := doJSON(t, r, http.MethodPost, "/v1/meetings/m1/guest-session", "", `{"password":"p","display_name":"G"}`)
	if guest.Code != http.StatusServiceUnavailable {
		t.Fatalf("guest-session want 503 got %d %s", guest.Code, guest.Body.String())
	}
	lk := doJSON(t, r, http.MethodPost, "/v1/meetings/m1/livekit-token", "unused", `{}`)
	if lk.Code != http.StatusServiceUnavailable {
		t.Fatalf("livekit-token want 503 got %d %s", lk.Code, lk.Body.String())
	}
}

// AC2b: 未就绪时建会 / 嘉宾会话 / LiveKit 令牌失败关闭。
func TestAC2_FailClosedWhenNotReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secretEmp := []byte("emp")
	secretGst := []byte("gst")
	store := NewMemoryStore()
	r := gin.New()
	blocked := &ready.Checker{EmployeeSecretSet: false, GuestSecretSet: false, DatabaseURL: ""}
	RegisterRoutes(r, store, secretEmp, secretGst, "ws://127.0.0.1:17880", "devkey", "secret", true, "metuai-media", nil, nil, NewBreakGlassStore(), NewGuestEmailVerifier(store, nil), nil, blocked)

	emp := employeeJWT(t, secretEmp)
	create := doJSON(t, r, http.MethodPost, "/v1/meetings", emp, `{"title":"x"}`)
	if create.Code != http.StatusServiceUnavailable {
		t.Fatalf("create want 503 got %d %s", create.Code, create.Body.String())
	}
	guest := doJSON(t, r, http.MethodPost, "/v1/meetings/m1/guest-session", "", `{"password":"p","display_name":"G"}`)
	if guest.Code != http.StatusServiceUnavailable {
		t.Fatalf("guest-session want 503 got %d %s", guest.Code, guest.Body.String())
	}
	lk := doJSON(t, r, http.MethodPost, "/v1/meetings/m1/livekit-token", emp, `{}`)
	if lk.Code != http.StatusServiceUnavailable {
		t.Fatalf("livekit-token want 503 got %d %s", lk.Code, lk.Body.String())
	}
}

// AC3: 无转写 → 422 no_transcript，不发明待办。
func TestAC3_SummaryNoTranscript(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}
	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
	if got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 got %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &body)
	if body.Error != "no_transcript" {
		t.Fatalf("body %+v", body)
	}
	if _, ok := store.GetSummary(id); ok {
		t.Fatal("must not invent summary")
	}
}

// AC4: 有转写但未配置私有 LLM → 503 AI_NOT_CONFIGURED；会议其它能力不受影响。
func TestAC4_SummaryAINotConfigured(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	t.Setenv("PRIVATE_LLM_URL", "")
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if err := store.ReplaceTranscript(id, []TranscriptSegment{{
		MeetingID: id, Text: "我们讨论预算", SpeakerDisplayName: "Alice",
	}}); err != nil {
		t.Fatal(err)
	}
	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
	if got.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 got %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &body)
	if body.Error != "AI_NOT_CONFIGURED" {
		t.Fatalf("body %+v", body)
	}
	// 会议心跳类能力仍可用：列表应 200。
	list := doJSON(t, r, http.MethodGet, "/v1/meetings", emp, "")
	if list.Code != http.StatusOK {
		t.Fatalf("meetings list %d", list.Code)
	}
}

// AC4b: 有转写且配置了私有 LLM → summary 非空且 action_items[].task 必填。
func TestAC4b_SummaryFromTranscriptWhenPrivateLLMConfigured(t *testing.T) {
	t.Setenv("PRIVATE_LLM_URL", "http://127.0.0.1:9/private-llm")
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if err := store.ReplaceTranscript(id, []TranscriptSegment{{
		MeetingID: id, ID: "seg-1", Text: "下周交付 PoC", SpeakerDisplayName: "Alice",
	}}); err != nil {
		t.Fatal(err)
	}
	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", emp, "")
	if got.Code != http.StatusOK {
		t.Fatalf("want 200 got %d %s", got.Code, got.Body.String())
	}
	var sum MeetingSummary
	if err := json.Unmarshal(got.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(sum.Summary) == "" {
		t.Fatal("summary must be non-empty")
	}
	if len(sum.ActionItems) == 0 {
		t.Fatal("expected grounded action items from transcript")
	}
	for _, item := range sum.ActionItems {
		if strings.TrimSpace(item.Task) == "" {
			t.Fatal("action_items[].task required")
		}
	}
}

// AC8: 结束会议 — 组织者与共同组织者允许；其他员工 403；嘉宾/缺令牌 401；错误体不泄露会议字段。
func TestAC8_EndMeetingAuthorization(t *testing.T) {
	r, _, secretEmp, secretGst := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-org", "Org")
	coOrg := employeeJWTFor(t, secretEmp, "u-co", "Co")
	other := employeeJWTFor(t, secretEmp, "u-other", "Other")

	create := doJSON(t, r, http.MethodPost, "/v1/meetings", organizer, `{"title":"secret-title","co_organizer_ids":["u-co"]}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	_ = json.Unmarshal(create.Body.Bytes(), &created)

	denied := doJSON(t, r, http.MethodPost, "/v1/meetings/"+created.ID+"/end", other, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("other employee want 403 got %d %s", denied.Code, denied.Body.String())
	}
	if strings.Contains(denied.Body.String(), "secret-title") {
		t.Fatalf("error must not leak meeting title: %s", denied.Body.String())
	}

	noTok := doJSON(t, r, http.MethodPost, "/v1/meetings/"+created.ID+"/end", "", "")
	if noTok.Code != http.StatusUnauthorized {
		t.Fatalf("missing token want 401 got %d", noTok.Code)
	}

	guest := doJSON(t, r, http.MethodPost, "/v1/meetings/"+created.ID+"/guest-session", "",
		`{"password":"`+created.Password+`","display_name":"G"}`)
	if guest.Code != http.StatusOK {
		t.Fatalf("guest-session %d %s", guest.Code, guest.Body.String())
	}
	gTok := guestToken(t, guest.Body.Bytes())
	guestEnd := doJSON(t, r, http.MethodPost, "/v1/meetings/"+created.ID+"/end", gTok, "")
	if guestEnd.Code != http.StatusUnauthorized {
		t.Fatalf("guest end want 401 got %d %s", guestEnd.Code, guestEnd.Body.String())
	}
	_ = secretGst

	coEnd := doJSON(t, r, http.MethodPost, "/v1/meetings/"+created.ID+"/end", coOrg, "")
	if coEnd.Code != http.StatusOK {
		t.Fatalf("co-organizer end want 200 got %d %s", coEnd.Code, coEnd.Body.String())
	}
}

// RAP: recording-ack 必需后才能拿 livekit-token。
func TestRAP_RecordingAckRequiredBeforeLivekitToken(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, emp)
	guest := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if guest.Code != http.StatusOK {
		t.Fatalf("guest %d %s", guest.Code, guest.Body.String())
	}
	tok := guestToken(t, guest.Body.Bytes())
	lk := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", tok, `{}`)
	if lk.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d %s", lk.Code, lk.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(lk.Body.Bytes(), &body)
	if body.Error != "recording_ack_required" {
		t.Fatalf("body %+v", body)
	}
}

// AC10: 错误嘉宾密码 403 invalid_password 且无 LiveKit 令牌；结束后 guest-session/livekit-token 403 meeting_ended。
func TestAC10_InvalidPasswordAndMeetingEnded(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, emp)

	bad := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"wrong","display_name":"Bob"}`)
	if bad.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d %s", bad.Code, bad.Body.String())
	}
	var badBody struct {
		Error string `json:"error"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(bad.Body.Bytes(), &badBody)
	if badBody.Error != "invalid_password" || badBody.Token != "" {
		t.Fatalf("body %+v", badBody)
	}

	guest := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	tok := guestToken(t, guest.Body.Bytes())
	_ = doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", tok, `{}`)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	afterGuest := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob2"}`)
	if afterGuest.Code != http.StatusForbidden {
		t.Fatalf("guest after end want 403 got %d", afterGuest.Code)
	}
	var endedBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(afterGuest.Body.Bytes(), &endedBody)
	if endedBody.Error != "meeting_ended" {
		t.Fatalf("body %+v", endedBody)
	}
	lk := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", tok, `{}`)
	if lk.Code != http.StatusForbidden {
		t.Fatalf("livekit after end want 403 got %d", lk.Code)
	}
	_ = json.Unmarshal(lk.Body.Bytes(), &endedBody)
	if endedBody.Error != "meeting_ended" {
		t.Fatalf("livekit body %+v", endedBody)
	}
}

// AC12: 跨会 ACL 403；未签名媒体访问 401/403。
func TestAC12_CrossMeetingACLAndUnsignedMedia(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	empA := employeeJWTFor(t, secretEmp, "u-a", "A")
	empB := employeeJWTFor(t, secretEmp, "u-b", "B")
	idA, passA := createMeeting(t, r, empA)
	idB, _ := createMeeting(t, r, empB)

	guest := doJSON(t, r, http.MethodPost, "/v1/meetings/"+idA+"/guest-session", "",
		`{"password":"`+passA+`","display_name":"G"}`)
	tokA := guestToken(t, guest.Body.Bytes())
	cross := doJSON(t, r, http.MethodPost, "/v1/meetings/"+idB+"/recording-ack", tokA, `{}`)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-meeting ack want 403 got %d %s", cross.Code, cross.Body.String())
	}
	crossLK := doJSON(t, r, http.MethodPost, "/v1/meetings/"+idB+"/livekit-token", tokA, `{}`)
	if crossLK.Code != http.StatusForbidden {
		t.Fatalf("cross-meeting livekit want 403 got %d %s", crossLK.Code, crossLK.Body.String())
	}

	unsigned := doJSON(t, r, http.MethodGet, "/v1/meetings/"+idA+"/media", "", "")
	if unsigned.Code != http.StatusUnauthorized && unsigned.Code != http.StatusForbidden {
		t.Fatalf("unsigned media want 401/403 got %d %s", unsigned.Code, unsigned.Body.String())
	}
	strangerMedia := doJSON(t, r, http.MethodGet, "/v1/meetings/"+idA+"/media", empB, "")
	if strangerMedia.Code != http.StatusForbidden {
		t.Fatalf("stranger media want 403 got %d %s", strangerMedia.Code, strangerMedia.Body.String())
	}
	_ = store
	_ = context.Background()
}
