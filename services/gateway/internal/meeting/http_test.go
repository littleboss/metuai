package meeting

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"metuai/services/gateway/internal/egress"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/knowledge"
	"metuai/services/gateway/internal/ready"
)

func employeeJWT(t *testing.T, secret []byte) string {
	t.Helper()
	return employeeJWTFor(t, secret, "u-1", "Alice")
}

func employeeJWTFor(t *testing.T, secret []byte, sub, name string) string {
	return employeeJWTForRoles(t, secret, sub, name)
}

func employeeJWTForRoles(t *testing.T, secret []byte, sub, name string, roles ...string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          sub,
		"kind":         identity.KindEmployee,
		"email":        sub + "@corp.local",
		"display_name": name,
		"roles":        roles,
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func guestToken(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Token
}

func testRouter(t *testing.T) (*gin.Engine, *Store, []byte, []byte) {
	t.Helper()
	return testRouterWithWeb(t, true)
}

func testRouterWithWeb(t *testing.T, allowEmployeeWeb bool) (*gin.Engine, *Store, []byte, []byte) {
	t.Helper()
	return testRouterWithEgress(t, allowEmployeeWeb, nil)
}

// testRouterWithEgress 允许注入 Egress 编排替身；orch 为 nil 表示未接线（默认）。
func testRouterWithEgress(t *testing.T, allowEmployeeWeb bool, orch EgressOrchestrator) (*gin.Engine, *Store, []byte, []byte) {
	return testRouterWithEgressAndVerification(t, allowEmployeeWeb, orch, nil)
}

func testRouterWithEgressAndVerification(t *testing.T, allowEmployeeWeb bool, orch EgressOrchestrator, sender GuestVerificationSender) (*gin.Engine, *Store, []byte, []byte) {
	t.Helper()
	// 单测默认视为已配置私有 LLM，便于会后链路跑通纪要；AC4 等用例会显式清空。
	t.Setenv("PRIVATE_LLM_URL", "http://127.0.0.1:9/private-llm")
	gin.SetMode(gin.TestMode)
	secretEmp := []byte("emp")
	secretGst := []byte("gst")
	store := NewMemoryStore()
	r := gin.New()
	var rt *EgressRuntime
	if orch != nil {
		rt = NewEgressRuntime(orch, "metuai-media")
	}
	RegisterRoutes(r, store, secretEmp, secretGst, "ws://127.0.0.1:17880", "devkey", "secret", allowEmployeeWeb, "metuai-media", rt, knowledge.NewMemoryIndex(), NewBreakGlassStore(), NewGuestEmailVerifier(store, sender), nil, ready.AlwaysReady())
	return r, store, secretEmp, secretGst
}

type capturedVerificationSender struct {
	mail GuestVerificationMail
}

func (s *capturedVerificationSender) Send(_ context.Context, mail GuestVerificationMail) error {
	s.mail = mail
	return nil
}

func createMeeting(t *testing.T, r *gin.Engine, empToken string) (id, password string) {
	t.Helper()
	body := bytes.NewBufferString(`{"title":"t1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/meetings", body)
	req.Header.Set("Authorization", "Bearer "+empToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID, created.Password
}

func doJSON(t *testing.T, r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Buffer
	if body == "" {
		reader = bytes.NewBuffer(nil)
	} else {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCreateMeetingAndGuestCannotGetLivekitBeforeAck(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	id, password := createMeeting(t, r, employeeJWT(t, secretEmp))

	gbody := bytes.NewBufferString(`{"password":"` + password + `","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	if gw.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", gw.Code, gw.Body.String())
	}
	var gs struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(gw.Body.Bytes(), &gs); err != nil {
		t.Fatal(err)
	}

	lt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	lt.Header.Set("Authorization", "Bearer "+gs.Token)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, lt)
	if lw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 before ack, got %d %s", lw.Code, lw.Body.String())
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &errBody)
	if errBody.Error != "recording_ack_required" {
		t.Fatalf("error want recording_ack_required got %q body=%s", errBody.Error, lw.Body.String())
	}
}

func TestWrongPasswordRejected(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	id, _ := createMeeting(t, r, employeeJWT(t, secretEmp))

	gbody := bytes.NewBufferString(`{"password":"wrongpass","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	if gw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 invalid password, got %d %s", gw.Code, gw.Body.String())
	}
}

func TestInvitedEmployeeJoinsWithoutPasswordAndMeetingListIsScoped(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-1", "Alice")
	invited := employeeJWTFor(t, secretEmp, "u-2", "Bob")
	uninvited := employeeJWTFor(t, secretEmp, "u-3", "Carol")

	created := doJSON(t, r, http.MethodPost, "/v1/meetings", organizer,
		`{"title":"planning","employee_ids":["u-2"]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var meeting struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &meeting); err != nil {
		t.Fatal(err)
	}

	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/recording-ack", invited, ""); got.Code != http.StatusOK {
		t.Fatalf("invited ack %d %s", got.Code, got.Body.String())
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/recording-ack", uninvited, ""); got.Code != http.StatusForbidden {
		t.Fatalf("uninvited ack without password %d %s", got.Code, got.Body.String())
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/recording-ack", uninvited,
		`{"password":"`+meeting.Password+`"}`); got.Code != http.StatusOK {
		t.Fatalf("uninvited ack with password %d %s", got.Code, got.Body.String())
	}

	listed := doJSON(t, r, http.MethodGet, "/v1/meetings", invited, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), meeting.ID) {
		t.Fatalf("invited list %d %s", listed.Code, listed.Body.String())
	}
	outsider := employeeJWTFor(t, secretEmp, "u-4", "Dana")
	listed = doJSON(t, r, http.MethodGet, "/v1/meetings", outsider, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), meeting.ID) {
		t.Fatalf("outsider list %d %s", listed.Code, listed.Body.String())
	}
}

func TestCoOrganizerCanControlMeetingButParticipantCannot(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-1", "Alice")
	coOrganizer := employeeJWTFor(t, secretEmp, "u-2", "Bob")
	participant := employeeJWTFor(t, secretEmp, "u-3", "Carol")

	created := doJSON(t, r, http.MethodPost, "/v1/meetings", organizer,
		`{"title":"review","employee_ids":["u-2"],"co_organizer_ids":["u-2"]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var meeting struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &meeting); err != nil {
		t.Fatal(err)
	}

	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/lock", participant, ""); got.Code != http.StatusForbidden {
		t.Fatalf("participant lock %d %s", got.Code, got.Body.String())
	}
	for _, endpoint := range []string{"lock", "unlock", "reset-password"} {
		if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/"+endpoint, coOrganizer, ""); got.Code != http.StatusOK {
			t.Fatalf("co-organizer %s %d %s", endpoint, got.Code, got.Body.String())
		}
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/kick", coOrganizer,
		`{"identity":"guest:g-1"}`); got.Code != http.StatusOK {
		t.Fatalf("co-organizer kick %d %s", got.Code, got.Body.String())
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/end", coOrganizer, ""); got.Code != http.StatusOK {
		t.Fatalf("co-organizer end %d %s", got.Code, got.Body.String())
	}
}

func TestGuestEmailVerificationGrantsOnlyVerifiedPostMeetingAccess(t *testing.T) {
	sender := &capturedVerificationSender{}
	r, _, secretEmp, _ := testRouterWithEgressAndVerification(t, true, nil, sender)
	organizer := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, organizer)

	guestSession := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if guestSession.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", guestSession.Code, guestSession.Body.String())
	}
	guest := guestToken(t, guestSession.Body.Bytes())
	if ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", guest, ""); ack.Code != http.StatusOK {
		t.Fatalf("guest ack %d %s", ack.Code, ack.Body.String())
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification", guest,
		`{"email":"Bob@Example.com"}`); got.Code != http.StatusOK {
		t.Fatalf("request verification %d %s", got.Code, got.Body.String())
	}
	if sender.mail.To != "bob@example.com" || len(sender.mail.Code) != 6 {
		t.Fatalf("mail = %+v", sender.mail)
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification/confirm", guest,
		`{"email":"bob@example.com","code":"000000"}`); got.Code != http.StatusForbidden {
		t.Fatalf("invalid code %d %s", got.Code, got.Body.String())
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run fake %d %s", run.Code, run.Body.String())
	}
	if got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", guest, ""); got.Code != http.StatusForbidden {
		t.Fatalf("unverified summary %d %s", got.Code, got.Body.String())
	}

	verified := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification/confirm", guest,
		`{"email":"bob@example.com","code":"`+sender.mail.Code+`"}`)
	if verified.Code != http.StatusOK {
		t.Fatalf("confirm verification %d %s", verified.Code, verified.Body.String())
	}
	var payload struct {
		Token string `json:"access_token"`
	}
	if err := json.Unmarshal(verified.Body.Bytes(), &payload); err != nil || payload.Token == "" {
		t.Fatalf("verified token = %q, err=%v body=%s", payload.Token, err, verified.Body.String())
	}
	if got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", payload.Token, ""); got.Code != http.StatusOK {
		t.Fatalf("verified summary %d %s", got.Code, got.Body.String())
	}
}

func TestAckThenLivekitTokenOK(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, emp)

	ack := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/recording-ack", nil)
	ack.Header.Set("Authorization", "Bearer "+emp)
	aw := httptest.NewRecorder()
	r.ServeHTTP(aw, ack)
	if aw.Code != http.StatusOK {
		t.Fatalf("ack %d %s", aw.Code, aw.Body.String())
	}

	lt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	lt.Header.Set("Authorization", "Bearer "+emp)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, lt)
	if lw.Code != http.StatusOK {
		t.Fatalf("livekit token %d %s", lw.Code, lw.Body.String())
	}
	var tok struct {
		Token      string `json:"token"`
		LivekitURL string `json:"livekit_url"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	if tok.Token == "" || tok.LivekitURL == "" {
		t.Fatalf("empty token response %+v", tok)
	}

	gbody := bytes.NewBufferString(`{"password":"` + password + `","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	var gs struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(gw.Body.Bytes(), &gs)
	gack := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/recording-ack", nil)
	gack.Header.Set("Authorization", "Bearer "+gs.Token)
	r.ServeHTTP(httptest.NewRecorder(), gack)
	glt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	glt.Header.Set("Authorization", "Bearer "+gs.Token)
	glw := httptest.NewRecorder()
	r.ServeHTTP(glw, glt)
	if glw.Code != http.StatusOK {
		t.Fatalf("guest livekit %d %s", glw.Code, glw.Body.String())
	}
}

func TestUnauthorizedCreate(t *testing.T) {
	r, _, _, _ := testRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/meetings", bytes.NewBufferString(`{"title":"x"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 got %d", w.Code)
	}
}

func TestLockBlocksGuestAndUnlockRestores(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, emp)

	lock := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/lock", emp, "")
	if lock.Code != http.StatusOK {
		t.Fatalf("lock %d %s", lock.Code, lock.Body.String())
	}

	gw := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if gw.Code != http.StatusForbidden {
		t.Fatalf("expected locked guest reject, got %d %s", gw.Code, gw.Body.String())
	}

	unlock := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/unlock", emp, "")
	if unlock.Code != http.StatusOK {
		t.Fatalf("unlock %d %s", unlock.Code, unlock.Body.String())
	}
	gw2 := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if gw2.Code != http.StatusOK {
		t.Fatalf("guest after unlock %d %s", gw2.Code, gw2.Body.String())
	}
}

func TestLockBlocksOrganizerFromIssuingNewJoinToken(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	if ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", emp, ""); ack.Code != http.StatusOK {
		t.Fatalf("ack %d %s", ack.Code, ack.Body.String())
	}
	if lock := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/lock", emp, ""); lock.Code != http.StatusOK {
		t.Fatalf("lock %d %s", lock.Code, lock.Body.String())
	}
	join := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", emp, "")
	if join.Code != http.StatusForbidden || !strings.Contains(join.Body.String(), "meeting_locked") {
		t.Fatalf("locked organizer must not get a new join token: %d %s", join.Code, join.Body.String())
	}
}

func TestUninvitedEmployeeMustSupplyMeetingPassword(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, organizer)
	other := employeeJWTFor(t, secretEmp, "u-2", "Carol")

	missing := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", other, "")
	if missing.Code != http.StatusForbidden || !strings.Contains(missing.Body.String(), "meeting_password_required") {
		t.Fatalf("missing password should fail: %d %s", missing.Code, missing.Body.String())
	}
	wrong := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", other, `{"password":"wrong"}`)
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("wrong password should fail: %d %s", wrong.Code, wrong.Body.String())
	}
	ok := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", other, `{"password":"`+password+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("correct password should authorize participant: %d %s", ok.Code, ok.Body.String())
	}
}

func TestNonOrganizerCannotLock(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	other := employeeJWTFor(t, secretEmp, "u-2", "Carol")
	w := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/lock", other, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected organizer_required, got %d %s", w.Code, w.Body.String())
	}
}

func TestResetPasswordInvalidatesOld(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, oldPassword := createMeeting(t, r, emp)

	w := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/reset-password", emp, "")
	if w.Code != http.StatusOK {
		t.Fatalf("reset %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Password == "" || body.Password == oldPassword {
		t.Fatalf("expected new password, got %q", body.Password)
	}

	old := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+oldPassword+`","display_name":"Bob"}`)
	if old.Code != http.StatusForbidden {
		t.Fatalf("old password should fail, got %d", old.Code)
	}
	neu := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+body.Password+`","display_name":"Bob"}`)
	if neu.Code != http.StatusOK {
		t.Fatalf("new password should work, got %d %s", neu.Code, neu.Body.String())
	}
}

func TestEndMeetingBlocksJoin(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, emp)
	end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, "")
	if end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}
	gw := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if gw.Code != http.StatusForbidden {
		t.Fatalf("expected meeting_ended, got %d %s", gw.Code, gw.Body.String())
	}

	media := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", emp, "")
	if media.Code != http.StatusOK {
		t.Fatalf("media %d %s", media.Code, media.Body.String())
	}
	var payload struct {
		Artifacts []MediaArtifact `json:"artifacts"`
	}
	_ = json.Unmarshal(media.Body.Bytes(), &payload)
	if len(payload.Artifacts) != 3 {
		t.Fatalf("expected 3 media plans, got %+v", payload.Artifacts)
	}
	// Egress 未接线：三路都应停在 pending，绝不能出现假 ready。
	for _, a := range payload.Artifacts {
		if a.Status != "pending" {
			t.Fatalf("end path without egress must stay pending: %+v", a)
		}
	}
}

func TestEgressStartsOnJoinAndFinalizesOnEnd(t *testing.T) {
	orch := &fakeOrchestrator{
		enabled: true,
		starts: []egress.Started{
			{Kind: egress.KindRoomAudio, Outcome: egress.OutcomeStarted, EgressID: "eg_audio"},
			{Kind: egress.KindRoomVideo, Outcome: egress.OutcomeStarted, EgressID: "eg_video"},
		},
		finalize: map[string]egress.Handle{
			"eg_audio": {EgressID: "eg_audio", Status: egress.StatusComplete, Files: []string{"s3://metuai-media/a.ogg"}},
			"eg_video": {EgressID: "eg_video", Status: egress.StatusFailed, Error: "disk full"},
		},
	}
	r, store, secretEmp, _ := testRouterWithEgress(t, true, orch)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))

	// 入会即开录：拿令牌这一步必须触发 Egress。
	if lt := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", emp, ""); lt.Code != http.StatusOK {
		t.Fatalf("livekit token %d %s", lt.Code, lt.Body.String())
	}
	if orch.startCall != 1 {
		t.Fatalf("joining should start egress once, got %d", orch.startCall)
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", emp, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}

	arts := artifactsByKind(t, store, id)
	if arts[egress.KindRoomAudio].Status != "ready" {
		t.Fatalf("audio should be ready after a completed egress: %+v", arts[egress.KindRoomAudio])
	}
	if arts[egress.KindRoomVideo].Status != "failed" {
		t.Fatalf("video should be failed: %+v", arts[egress.KindRoomVideo])
	}
}

func TestKickBlocksRelivekitToken(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))

	other := employeeJWTFor(t, secretEmp, "u-2", "Carol")
	_ = store.AckRecording(id, PrincipalKey("employee", "u-2", ""))

	kick := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/kick", emp, `{"identity":"u-2"}`)
	if kick.Code != http.StatusOK {
		t.Fatalf("kick %d %s", kick.Code, kick.Body.String())
	}
	lt := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", other, "")
	if lt.Code != http.StatusForbidden {
		t.Fatalf("kicked user should be blocked, got %d %s", lt.Code, lt.Body.String())
	}
}

func TestEmployeeWebForbiddenWhenDisabled(t *testing.T) {
	r, store, secretEmp, _ := testRouterWithWeb(t, false)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))

	lt := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", emp, "")
	if lt.Code != http.StatusForbidden {
		t.Fatalf("expected employee_web_forbidden, got %d %s", lt.Code, lt.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	req.Header.Set("Authorization", "Bearer "+emp)
	req.Header.Set("X-Metuai-Client", "tauri")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("tauri client should be allowed, got %d %s", w.Code, w.Body.String())
	}
}

func TestChatPersists(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)

	post := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/chat", emp, `{"body":"hello team"}`)
	if post.Code != http.StatusOK {
		t.Fatalf("chat post %d %s", post.Code, post.Body.String())
	}
	list := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/chat", emp, "")
	if list.Code != http.StatusOK {
		t.Fatalf("chat list %d %s", list.Code, list.Body.String())
	}
	var payload struct {
		Messages []ChatMessage `json:"messages"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &payload)
	if len(payload.Messages) != 1 || payload.Messages[0].Body != "hello team" {
		t.Fatalf("unexpected messages %+v", payload.Messages)
	}
}

func TestLocalRecordingAuditRoundTrip(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)

	bad := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/local-recording/audit", emp,
		`{"events":[{"action":"meeting_ended","detail":"nope"}]}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("non local_recording action must be rejected, got %d %s", bad.Code, bad.Body.String())
	}

	ok := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/local-recording/audit", emp,
		`{"events":[
			{"action":"local_recording_started","detail":"upload_id=up_1"},
			{"action":"local_recording_acked","detail":"parts=2"}
		]}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("audit post %d %s", ok.Code, ok.Body.String())
	}

	auditor := employeeJWTForRoles(t, secretEmp, "u-auditor", "Auditor", "audit_admin")
	list := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/audit", auditor, "")
	if list.Code != http.StatusOK {
		t.Fatalf("audit list %d %s", list.Code, list.Body.String())
	}
	var payload struct {
		Events []AuditEvent `json:"events"`
	}
	_ = json.Unmarshal(list.Body.Bytes(), &payload)
	found := 0
	for _, e := range payload.Events {
		if strings.HasPrefix(e.Action, "local_recording_") {
			found++
		}
	}
	if found < 2 {
		t.Fatalf("want at least 2 local_recording audits, got %+v", payload.Events)
	}
}

func TestStrangerCannotReadMeetingData(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run %d %s", run.Code, run.Body.String())
	}
	stranger := employeeJWTFor(t, secretEmp, "u-stranger", "Stranger")
	for _, path := range []string{
		"/v1/meetings/" + id,
		"/v1/meetings/" + id + "/chat",
		"/v1/meetings/" + id + "/media",
		"/v1/meetings/" + id + "/pipeline",
		"/v1/meetings/" + id + "/transcript",
		"/v1/meetings/" + id + "/summary",
	} {
		got := doJSON(t, r, http.MethodGet, path, stranger, "")
		if got.Code != http.StatusForbidden {
			t.Errorf("GET %s should reject stranger, got %d %s", path, got.Code, got.Body.String())
		}
	}
}

func TestGuestSuppliedEmailIsNotTreatedAsVerified(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, organizer)
	session := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob","email":"victim@example.com"}`)
	if session.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", session.Code, session.Body.String())
	}
	guest := guestToken(t, session.Body.Bytes())
	if ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", guest, ""); ack.Code != http.StatusOK {
		t.Fatalf("guest ack %d %s", ack.Code, ack.Body.String())
	}
	if emails, err := store.ListGuestEmails(id); err != nil || len(emails) != 0 {
		t.Fatalf("unverified input must not enter ACL: emails=%v err=%v", emails, err)
	}
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run %d", run.Code)
	}
	view := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", guest, "")
	if view.Code != http.StatusForbidden {
		t.Fatalf("unverified guest must not read post-meeting artifacts: %d %s", view.Code, view.Body.String())
	}
}

func TestEmployeeCanReviseSummaryAndGuestCannot(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, organizer)
	guestSession := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if guestSession.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", guestSession.Code, guestSession.Body.String())
	}
	guest := guestToken(t, guestSession.Body.Bytes())
	if ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", guest, ""); ack.Code != http.StatusOK {
		t.Fatalf("guest ack %d %s", ack.Code, ack.Body.String())
	}
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run-fake %d %s", run.Code, run.Body.String())
	}

	patch := doJSON(t, r, http.MethodPatch, "/v1/meetings/"+id+"/summary", organizer, `{
		"summary":"人工修订后的摘要",
		"decisions":[{"text":"保留原稿","source_segment_ids":[]}],
		"action_items":[{"task":"内部跟进","owner_user_id":"u-1"}],
		"risks":[],
		"open_questions":[]
	}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch %d %s", patch.Code, patch.Body.String())
	}
	var sum MeetingSummary
	if err := json.Unmarshal(patch.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Summary != "人工修订后的摘要" || sum.OriginalJSON == "" {
		t.Fatalf("revised summary %+v", sum)
	}

	revs := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary/revisions", organizer, "")
	if revs.Code != http.StatusOK {
		t.Fatalf("revisions %d %s", revs.Code, revs.Body.String())
	}

	denied := doJSON(t, r, http.MethodPatch, "/v1/meetings/"+id+"/summary", guest, `{"summary":"嘉宾篡改"}`)
	if denied.Code == http.StatusOK {
		t.Fatalf("guest must not revise summary: %d %s", denied.Code, denied.Body.String())
	}

	stranger := employeeJWTFor(t, secretEmp, "u-stranger", "Eve")
	blocked := doJSON(t, r, http.MethodPatch, "/v1/meetings/"+id+"/summary", stranger, `{"summary":"路人修订"}`)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("stranger patch %d %s", blocked.Code, blocked.Body.String())
	}
}

func TestGuestMagicLinkAndLeaveAndArtifactView(t *testing.T) {
	sender := &capturedVerificationSender{}
	r, store, secretEmp, _ := testRouterWithEgressAndVerification(t, true, nil, sender)
	organizer := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, organizer)

	guestSession := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if guestSession.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", guestSession.Code, guestSession.Body.String())
	}
	guest := guestToken(t, guestSession.Body.Bytes())
	if ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", guest, ""); ack.Code != http.StatusOK {
		t.Fatalf("guest ack %d %s", ack.Code, ack.Body.String())
	}
	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification", guest,
		`{"email":"bob@example.com"}`); got.Code != http.StatusOK {
		t.Fatalf("request verification %d %s", got.Code, got.Body.String())
	}
	if sender.mail.MagicToken == "" || !strings.Contains(sender.mail.MagicURL, sender.mail.MagicToken) {
		t.Fatalf("magic mail = %+v", sender.mail)
	}

	leave := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/leave", guest, "")
	if leave.Code != http.StatusOK {
		t.Fatalf("leave %d %s", leave.Code, leave.Body.String())
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}
	tasks := doJSON(t, r, http.MethodGet, "/v1/pipeline/tasks?meeting_id="+id, organizer, "")
	if tasks.Code != http.StatusOK || !strings.Contains(tasks.Body.String(), `"status":"queued"`) {
		t.Fatalf("queued task %d %s", tasks.Code, tasks.Body.String())
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run fake %d %s", run.Code, run.Body.String())
	}

	magic := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification/magic", "",
		`{"token":"`+sender.mail.MagicToken+`"}`)
	if magic.Code != http.StatusOK {
		t.Fatalf("magic confirm %d %s", magic.Code, magic.Body.String())
	}
	var payload struct {
		Token string `json:"access_token"`
	}
	if err := json.Unmarshal(magic.Body.Bytes(), &payload); err != nil || payload.Token == "" {
		t.Fatalf("magic token body=%s err=%v", magic.Body.String(), err)
	}
	summary := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", payload.Token, "")
	if summary.Code != http.StatusOK {
		t.Fatalf("verified summary %d %s", summary.Code, summary.Body.String())
	}

	audits, err := store.ListAudit(id)
	if err != nil {
		t.Fatal(err)
	}
	var sawLeave, sawView, sawVerified bool
	for _, event := range audits {
		switch event.Action {
		case "meeting_left":
			sawLeave = true
		case "artifact_view":
			sawView = event.Detail == "summary" || sawView
		case "guest_email_verified":
			sawVerified = true
		}
	}
	if !sawLeave || !sawView || !sawVerified {
		t.Fatalf("audits leave=%v view=%v verified=%v events=%+v", sawLeave, sawView, sawVerified, audits)
	}
}

func TestPipelineClaimRetryAndDirectory(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	created := doJSON(t, r, http.MethodPost, "/v1/meetings", organizer,
		`{"title":"dir","employee_ids":["u-9"]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var meeting struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &meeting); err != nil {
		t.Fatal(err)
	}
	dir := doJSON(t, r, http.MethodGet, "/v1/directory/employees?q=u-9", organizer, "")
	if dir.Code != http.StatusOK || !strings.Contains(dir.Body.String(), `"user_id":"u-9"`) {
		t.Fatalf("directory %d %s", dir.Code, dir.Body.String())
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}
	claimed := doJSON(t, r, http.MethodPost, "/v1/pipeline/tasks/claim", organizer, `{"owner":"worker-1","limit":1}`)
	if claimed.Code != http.StatusOK || !strings.Contains(claimed.Body.String(), `"status":"leased"`) {
		t.Fatalf("claim %d %s", claimed.Code, claimed.Body.String())
	}
	var payload struct {
		Tasks []PipelineTask `json:"tasks"`
	}
	if err := json.Unmarshal(claimed.Body.Bytes(), &payload); err != nil || len(payload.Tasks) != 1 {
		t.Fatalf("claim payload %s err=%v", claimed.Body.String(), err)
	}
	failed := doJSON(t, r, http.MethodPost, "/v1/pipeline/tasks/"+payload.Tasks[0].ID+"/fail", organizer,
		`{"error":"no audio"}`)
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"status":"failed"`) {
		t.Fatalf("fail %d %s", failed.Code, failed.Body.String())
	}
	if mark := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/pipeline/manual-review", organizer,
		`{"reason":"no tracks"}`); mark.Code != http.StatusOK {
		t.Fatalf("manual review %d %s", mark.Code, mark.Body.String())
	}
	queue := doJSON(t, r, http.MethodGet, "/v1/pipeline/manual-review", organizer, "")
	if queue.Code != http.StatusOK || !strings.Contains(queue.Body.String(), meeting.ID) {
		t.Fatalf("queue %d %s", queue.Code, queue.Body.String())
	}
	retry := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/pipeline/retry", organizer, "")
	if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), `"pipeline_stage":"RECORDING_FINALIZED"`) {
		t.Fatalf("retry %d %s", retry.Code, retry.Body.String())
	}
}

func TestInRoomGuestCodeWorksWithoutSMTP(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, organizer)

	guestSession := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	if guestSession.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", guestSession.Code, guestSession.Body.String())
	}
	var session struct {
		GuestID string `json:"guest_id"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(guestSession.Body.Bytes(), &session); err != nil || session.GuestID == "" {
		t.Fatalf("session body=%s err=%v", guestSession.Body.String(), err)
	}
	if ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", session.Token, ""); ack.Code != http.StatusOK {
		t.Fatalf("ack %d %s", ack.Code, ack.Body.String())
	}

	if got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification", session.Token,
		`{"email":"bob@example.com"}`); got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), "use_in_room_code") {
		t.Fatalf("smtp-less request %d %s", got.Code, got.Body.String())
	}

	issued := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification/in-room", organizer,
		`{"email":"bob@example.com","guest_id":"guest:`+session.GuestID+`"}`)
	if issued.Code != http.StatusOK {
		t.Fatalf("in-room %d %s", issued.Code, issued.Body.String())
	}
	var challenge struct {
		Code     string `json:"code"`
		MagicURL string `json:"magic_url"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &challenge); err != nil || len(challenge.Code) != 6 || challenge.MagicURL == "" {
		t.Fatalf("issued %+v err=%v body=%s", challenge, err, issued.Body.String())
	}

	listed := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/guest-participants", organizer, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), session.GuestID) || !strings.Contains(listed.Body.String(), "Bob") {
		t.Fatalf("guest list %d %s", listed.Code, listed.Body.String())
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run-fake %d %s", run.Code, run.Body.String())
	}

	confirmed := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-email-verification/confirm", session.Token,
		`{"email":"bob@example.com","code":"`+challenge.Code+`"}`)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm %d %s", confirmed.Code, confirmed.Body.String())
	}
	var payload struct {
		Token string `json:"access_token"`
	}
	if err := json.Unmarshal(confirmed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/summary", payload.Token, ""); got.Code != http.StatusOK {
		t.Fatalf("summary %d %s", got.Code, got.Body.String())
	}

	login := doJSON(t, r, http.MethodPost, "/v1/session/login", organizer, "")
	logout := doJSON(t, r, http.MethodPost, "/v1/session/logout", organizer, "")
	if login.Code != http.StatusOK || logout.Code != http.StatusOK {
		t.Fatalf("session %d %d", login.Code, logout.Code)
	}
	events, err := store.ListAudit("")
	if err != nil {
		t.Fatal(err)
	}
	var sawLogin, sawLogout bool
	for _, event := range events {
		if event.Action == "employee_login" {
			sawLogin = true
		}
		if event.Action == "employee_logout" {
			sawLogout = true
		}
	}
	if !sawLogin || !sawLogout {
		t.Fatalf("session audits login=%v logout=%v events=%+v", sawLogin, sawLogout, events)
	}
}

func TestShareAddReturnsOfflineCodeWithoutSMTP(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)

	add := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers", organizer,
		`{"email":"reader@example.com"}`)
	if add.Code != http.StatusOK {
		t.Fatalf("add %d %s", add.Code, add.Body.String())
	}
	var payload struct {
		Code     string `json:"code"`
		MagicURL string `json:"magic_url"`
	}
	if err := json.Unmarshal(add.Body.Bytes(), &payload); err != nil || len(payload.Code) != 6 {
		t.Fatalf("offline share code %+v err=%v body=%s", payload, err, add.Body.String())
	}

	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run-fake %d", run.Code)
	}
	confirm := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/shared-readers/confirm", "",
		`{"email":"reader@example.com","code":"`+payload.Code+`"}`)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm %d %s", confirm.Code, confirm.Body.String())
	}
}

func TestExportTranscriptAndSummaryWritesAudit(t *testing.T) {
	r, store, secretEmp, _ := testRouter(t)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d", end.Code)
	}
	if run := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/pipeline/run-fake", organizer, ""); run.Code != http.StatusOK {
		t.Fatalf("run-fake %d %s", run.Code, run.Body.String())
	}
	got := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/artifacts/download", organizer, `{"kind":"export"}`)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"kind":"export"`) || !strings.Contains(got.Body.String(), `"segments"`) {
		t.Fatalf("export %d %s", got.Code, got.Body.String())
	}
	events, err := store.ListAudit(id)
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, event := range events {
		if event.Action == "artifact_export" {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatalf("expected artifact_export, got %+v", events)
	}
}
