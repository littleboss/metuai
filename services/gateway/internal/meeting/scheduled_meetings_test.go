package meeting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func futureRFC3339(d time.Duration) string {
	return time.Now().UTC().Add(d).Format(time.RFC3339)
}

func pastRFC3339(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(time.RFC3339)
}

func createScheduledMeeting(t *testing.T, r *gin.Engine, empToken, startsAt string) (id, password string) {
	t.Helper()
	body := `{"title":"scheduled","starts_at":"` + startsAt + `"}`
	w := doJSON(t, r, http.MethodPost, "/v1/meetings", empToken, body)
	if w.Code != http.StatusOK {
		t.Fatalf("create scheduled %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Password string `json:"password"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != MeetingStatusScheduled {
		t.Fatalf("status want scheduled got %q", created.Status)
	}
	return created.ID, created.Password
}

func assertErrorCode(t *testing.T, w *httptest.ResponseRecorder, code int, errCode string) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("want %d got %d %s", code, w.Code, w.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != errCode {
		t.Fatalf("error want %q got %q body=%s", errCode, payload.Error, w.Body.String())
	}
}

func TestScheduledMeetings_NoToken401(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	starts := futureRFC3339(2 * time.Hour)
	id, _ := createScheduledMeeting(t, r, employeeJWT(t, secretEmp), starts)

	for _, path := range []string{
		"/v1/meetings/" + id + "/recording-ack",
		"/v1/meetings/" + id + "/livekit-token",
	} {
		w := doJSON(t, r, http.MethodPost, path, "", `{}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s want 401 got %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestScheduledMeetings_CreateValidation400(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	token := employeeJWT(t, secretEmp)

	past := doJSON(t, r, http.MethodPost, "/v1/meetings", token,
		`{"title":"bad","starts_at":"`+pastRFC3339(time.Hour)+`"}`)
	assertErrorCode(t, past, http.StatusBadRequest, "starts_at must be in the future")

	invalid := doJSON(t, r, http.MethodPost, "/v1/meetings", token,
		`{"title":"bad","starts_at":"not-a-date"}`)
	assertErrorCode(t, invalid, http.StatusBadRequest, "invalid starts_at")

	start := futureRFC3339(2 * time.Hour)
	end := futureRFC3339(time.Hour)
	badEnd := doJSON(t, r, http.MethodPost, "/v1/meetings", token,
		`{"title":"bad","starts_at":"`+start+`","ends_at":"`+end+`"}`)
	assertErrorCode(t, badEnd, http.StatusBadRequest, "ends_at must be after starts_at")
}

func TestScheduledMeetings_GuestBlockedBeforeStart403(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	token := employeeJWT(t, secretEmp)
	starts := futureRFC3339(2 * time.Hour)
	id, password := createScheduledMeeting(t, r, token, starts)

	gw := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/guest-session", "",
		`{"password":"`+password+`","display_name":"Bob"}`)
	assertErrorCode(t, gw, http.StatusForbidden, "meeting_not_started")
}

func TestScheduledMeetings_InvitedEmployeeBlockedBeforeStart403(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-1", "Alice")
	invited := employeeJWTFor(t, secretEmp, "u-2", "Bob")
	starts := futureRFC3339(2 * time.Hour)

	created := doJSON(t, r, http.MethodPost, "/v1/meetings", organizer,
		`{"title":"plan","starts_at":"`+starts+`","employee_ids":["u-2"]}`)
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

	ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/recording-ack", invited,
		`{"password":"`+meeting.Password+`"}`)
	assertErrorCode(t, ack, http.StatusForbidden, "meeting_not_started")

	lk := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/livekit-token", invited, `{}`)
	assertErrorCode(t, lk, http.StatusForbidden, "meeting_not_started")
}

func TestScheduledMeetings_OrganizerBlockedBeforeStart403(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	token := employeeJWT(t, secretEmp)
	starts := futureRFC3339(2 * time.Hour)
	id, _ := createScheduledMeeting(t, r, token, starts)

	ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/recording-ack", token, "")
	assertErrorCode(t, ack, http.StatusForbidden, "meeting_not_started")

	lk := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/livekit-token", token, `{}`)
	assertErrorCode(t, lk, http.StatusForbidden, "meeting_not_started")
}

func TestScheduledMeetings_CoOrganizerBlockedBeforeStart403(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	organizer := employeeJWTFor(t, secretEmp, "u-1", "Alice")
	coOrg := employeeJWTFor(t, secretEmp, "u-2", "Bob")
	starts := futureRFC3339(2 * time.Hour)

	created := doJSON(t, r, http.MethodPost, "/v1/meetings", organizer,
		`{"title":"plan","starts_at":"`+starts+`","co_organizer_ids":["u-2"]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var meeting struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &meeting); err != nil {
		t.Fatal(err)
	}

	ack := doJSON(t, r, http.MethodPost, "/v1/meetings/"+meeting.ID+"/recording-ack", coOrg, "")
	assertErrorCode(t, ack, http.StatusForbidden, "meeting_not_started")
}

func TestScheduledMeetings_UnknownMeeting404(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	token := employeeJWT(t, secretEmp)

	for _, path := range []string{
		"/v1/meetings/mtg_nope/guest-session",
		"/v1/meetings/mtg_nope/recording-ack",
		"/v1/meetings/mtg_nope/livekit-token",
	} {
		w := doJSON(t, r, http.MethodPost, path, token, `{}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s want 404 got %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestScheduledMeetings_ImmediateMeetingLiveStatus(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	token := employeeJWT(t, secretEmp)

	w := doJSON(t, r, http.MethodPost, "/v1/meetings", token, `{"title":"now"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != MeetingStatusLive {
		t.Fatalf("status want live got %q", created.Status)
	}

	list := doJSON(t, r, http.MethodGet, "/v1/meetings", token, "")
	if list.Code != http.StatusOK {
		t.Fatalf("list %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `"status":"live"`) {
		t.Fatalf("list missing live status: %s", list.Body.String())
	}
}

func TestMeetingStatusHelpers(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	start := now.Add(time.Hour)
	m := Meeting{Ended: false, StartsAt: &start}
	if MeetingStatus(m, now) != MeetingStatusScheduled {
		t.Fatalf("want scheduled")
	}
	if MeetingStatus(Meeting{Ended: true}, now) != MeetingStatusEnded {
		t.Fatalf("want ended")
	}
	if MeetingStatus(Meeting{Ended: false}, now) != MeetingStatusLive {
		t.Fatalf("want live")
	}
}
