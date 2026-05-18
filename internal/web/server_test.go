package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"sinau/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sinau.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(Config{
		Store:                st,
		Templates:            "../../templates",
		StaticDir:            "../../static",
		NotificationsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestNotificationsDisabledHidesEverything(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sinau.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(Config{
		Store:                st,
		Templates:            "../../templates",
		StaticDir:            "../../static",
		NotificationsEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()

	// /settings 404s when the flag is off.
	settingsReq := httptest.NewRequest(http.MethodGet, "/settings", nil)
	settingsRR := httptest.NewRecorder()
	handler.ServeHTTP(settingsRR, settingsReq)
	if settingsRR.Code != http.StatusNotFound {
		t.Fatalf("/settings expected 404 when disabled, got %d", settingsRR.Code)
	}

	// /help is reachable but must not contain the Notifications section.
	helpReq := httptest.NewRequest(http.MethodGet, "/help", nil)
	helpRR := httptest.NewRecorder()
	handler.ServeHTTP(helpRR, helpReq)
	if helpRR.Code != http.StatusOK {
		t.Fatalf("/help expected 200, got %d", helpRR.Code)
	}
	body := helpRR.Body.String()
	if strings.Contains(body, ">Notifications<") {
		t.Fatal("help page should not show Notifications section when disabled")
	}
	if strings.Contains(body, `href="/settings"`) {
		t.Fatal("help page must not link to /settings when notifications are disabled")
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("missing strict CSP, got %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("missing frame protection, got %q", got)
	}
}

// classroomHarness sets up a fresh server with one mentor + one mentee
// already joined into a single classroom room. Returns the harness ready
// for HTTP-level tests of the classroom routes.
type classroomHarness struct {
	t             *testing.T
	handler       http.Handler
	mentorCookies []*http.Cookie
	menteeCookies []*http.Cookie
	mentorCSRF    string
	menteeCSRF    string
	roomID        string
}

func newClassroomHarness(t *testing.T) *classroomHarness {
	t.Helper()
	srv := newTestServer(t)
	h := srv.Handler()

	post := func(path string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	get := func(path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	between := func(body, start, end string) string {
		i := strings.Index(body, start)
		if i < 0 {
			t.Fatalf("could not find %q", start)
		}
		rest := body[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			t.Fatalf("could not find %q after %q", end, start)
		}
		return rest[:j]
	}

	// Mentor setup.
	setupRR := post("/setup", nil, url.Values{
		"name":     {"Teacher"},
		"email":    {"teacher@example.com"},
		"password": {"verysecurepass123"},
	})
	if setupRR.Code != http.StatusSeeOther {
		t.Fatalf("setup status = %d", setupRR.Code)
	}
	mentorCookies := setupRR.Result().Cookies()
	if len(mentorCookies) == 0 {
		t.Fatal("setup issued no cookie")
	}

	// Pull CSRF from mentor's home.
	mentorCSRF := between(get("/", mentorCookies).Body.String(), `name="csrf" value="`, `"`)

	// Mentor creates a classroom room.
	roomRR := post("/rooms", mentorCookies, url.Values{
		"csrf": {mentorCSRF}, "name": {"Data Science 101"}, "mode": {"classroom"},
	})
	if roomRR.Code != http.StatusSeeOther {
		t.Fatalf("room create status = %d body=%s", roomRR.Code, roomRR.Body.String())
	}
	roomID := strings.TrimPrefix(roomRR.Result().Header.Get("Location"), "/rooms/")

	// Mentor creates a mentee invite.
	inviteRR := post("/rooms/"+roomID+"/invites", mentorCookies, url.Values{
		"csrf": {mentorCSRF}, "role": {"mentee"},
	})
	if inviteRR.Code != http.StatusOK {
		t.Fatalf("invite status = %d body=%s", inviteRR.Code, inviteRR.Body.String())
	}
	// invite_created renders the full join URL first (most prominent); the
	// bare code is in a <details> fallback. Extract from the URL query.
	inviteCode := between(inviteRR.Body.String(), "?code=", "</code>")

	// Mentee joins.
	joinRR := post("/join", nil, url.Values{
		"code": {inviteCode}, "name": {"Student"},
		"email": {"student@example.com"}, "password": {"verysecurepass123"},
	})
	if joinRR.Code != http.StatusSeeOther {
		t.Fatalf("join status = %d body=%s", joinRR.Code, joinRR.Body.String())
	}
	menteeCookies := joinRR.Result().Cookies()
	menteeCSRF := between(get("/rooms/"+roomID, menteeCookies).Body.String(), `name="csrf" value="`, `"`)

	return &classroomHarness{
		t:             t,
		handler:       h,
		mentorCookies: mentorCookies,
		menteeCookies: menteeCookies,
		mentorCSRF:    mentorCSRF,
		menteeCSRF:    menteeCSRF,
		roomID:        roomID,
	}
}

func (h *classroomHarness) post(path string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, req)
	return rr
}

func TestClassroomTaskFlowEnforcesRoleAndMode(t *testing.T) {
	h := newClassroomHarness(t)

	// Mentee cannot create a task.
	bad := h.post("/rooms/"+h.roomID+"/tasks", h.menteeCookies, url.Values{
		"csrf":     {h.menteeCSRF},
		"title":    {"Mentee-published"},
		"detail":   {"should fail"},
		"due_date": {"2026-12-01"},
	})
	if bad.Code != http.StatusForbidden {
		t.Fatalf("mentee publishing task: want 403, got %d", bad.Code)
	}

	// Mentor publishes a valid task (classroom auto-broadcasts).
	createRR := h.post("/rooms/"+h.roomID+"/tasks", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"title":    {"Build a small notebook"},
		"detail":   {"Train a baseline model"},
		"due_date": {"2026-12-01"},
	})
	if createRR.Code != http.StatusSeeOther {
		t.Fatalf("mentor create task: want 303, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	// Pull the task ID from the mentee's task list (each card links to
	// /rooms/{id}/tasks/{taskID}).
	roomBody := func(cookies []*http.Cookie) string {
		req := httptest.NewRequest(http.MethodGet, "/rooms/"+h.roomID, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.handler.ServeHTTP(rr, req)
		return rr.Body.String()
	}
	body := roomBody(h.menteeCookies)
	prefix := "/rooms/" + h.roomID + "/tasks/"
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatal("could not find task detail link in mentee room body")
	}
	tail := body[start+len(prefix):]
	end := strings.IndexAny(tail, `"/`)
	if end < 0 {
		t.Fatal("malformed task detail link")
	}
	taskID := tail[:end]

	// Mentor cannot submit (wrong role).
	mentorSubmit := h.post("/rooms/"+h.roomID+"/tasks/"+taskID+"/submit", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"link_url": {"https://example.com/work"},
	})
	if mentorSubmit.Code != http.StatusForbidden {
		t.Fatalf("mentor submitting: want 403, got %d", mentorSubmit.Code)
	}

	// Mentee submits with a bad URL → 400.
	badURL := h.post("/rooms/"+h.roomID+"/tasks/"+taskID+"/submit", h.menteeCookies, url.Values{
		"csrf":     {h.menteeCSRF},
		"link_url": {"javascript:alert(1)"},
	})
	if badURL.Code != http.StatusBadRequest {
		t.Fatalf("mentee submit bad URL: want 400, got %d", badURL.Code)
	}

	// Mentee submits properly.
	goodSubmit := h.post("/rooms/"+h.roomID+"/tasks/"+taskID+"/submit", h.menteeCookies, url.Values{
		"csrf":     {h.menteeCSRF},
		"link_url": {"https://docs.google.com/work"},
		"note":     {"first pass"},
	})
	if goodSubmit.Code != http.StatusSeeOther {
		t.Fatalf("mentee submit: want 303, got %d body=%s", goodSubmit.Code, goodSubmit.Body.String())
	}

	// Find submission ID from mentor's room view (review queue).
	mBody := roomBody(h.mentorCookies)
	subPrefix := "/rooms/" + h.roomID + "/submissions/"
	subStart := strings.Index(mBody, subPrefix)
	if subStart < 0 {
		t.Fatal("could not find submission review form in mentor room body")
	}
	subTail := mBody[subStart+len(subPrefix):]
	subEnd := strings.Index(subTail, "/review")
	if subEnd < 0 {
		t.Fatal("malformed submission review form action")
	}
	submissionID := subTail[:subEnd]

	// Mentee cannot review.
	menteeReview := h.post("/rooms/"+h.roomID+"/submissions/"+submissionID+"/review", h.menteeCookies, url.Values{
		"csrf":     {h.menteeCSRF},
		"status":   {"reviewed"},
		"feedback": {"good"},
		"score":    {"85"},
	})
	if menteeReview.Code != http.StatusForbidden {
		t.Fatalf("mentee review: want 403, got %d", menteeReview.Code)
	}

	// Mentor with bad status → 400.
	badStatus := h.post("/rooms/"+h.roomID+"/submissions/"+submissionID+"/review", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"status":   {"approved"},
		"feedback": {"x"},
	})
	if badStatus.Code != http.StatusBadRequest {
		t.Fatalf("mentor review bad status: want 400, got %d", badStatus.Code)
	}

	// Mentor with revise status but empty feedback → 400.
	noFeedback := h.post("/rooms/"+h.roomID+"/submissions/"+submissionID+"/review", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"status":   {"revise"},
		"feedback": {""},
	})
	if noFeedback.Code != http.StatusBadRequest {
		t.Fatalf("mentor revise empty feedback: want 400, got %d", noFeedback.Code)
	}

	// Mentor reviews successfully (classroom: 0-100 score).
	reviewOK := h.post("/rooms/"+h.roomID+"/submissions/"+submissionID+"/review", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"status":   {"reviewed"},
		"feedback": {"clean"},
		"score":    {"90"},
	})
	if reviewOK.Code != http.StatusSeeOther {
		t.Fatalf("mentor review ok: want 303, got %d body=%s", reviewOK.Code, reviewOK.Body.String())
	}

	// Re-review must 404 — guarded by reviewed_at = ''.
	reviewAgain := h.post("/rooms/"+h.roomID+"/submissions/"+submissionID+"/review", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"status":   {"reviewed"},
		"feedback": {"changing mind"},
		"score":    {"95"},
	})
	if reviewAgain.Code != http.StatusNotFound {
		t.Fatalf("mentor re-review: want 404, got %d", reviewAgain.Code)
	}
}

// TestClassroomTaskCreateRequiresDetailAndDeadline — in classroom mode,
// instructions (detail) and deadline are gradebook-material defaults, so
// the unified /tasks endpoint must reject creates that omit either.
// Mentorship mode keeps both optional.
func TestClassroomTaskCreateRequiresDetailAndDeadline(t *testing.T) {
	h := newClassroomHarness(t)
	missingDetail := h.post("/rooms/"+h.roomID+"/tasks", h.mentorCookies, url.Values{
		"csrf":     {h.mentorCSRF},
		"title":    {"x"},
		"due_date": {"2026-12-01"},
	})
	if missingDetail.Code != http.StatusBadRequest {
		t.Fatalf("classroom create without detail: want 400, got %d", missingDetail.Code)
	}
	missingDue := h.post("/rooms/"+h.roomID+"/tasks", h.mentorCookies, url.Values{
		"csrf":   {h.mentorCSRF},
		"title":  {"x"},
		"detail": {"y"},
	})
	if missingDue.Code != http.StatusBadRequest {
		t.Fatalf("classroom create without due_date: want 400, got %d", missingDue.Code)
	}
}

func TestInvalidRoomModeRejected(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()
	post := func(path string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	get := func(path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	rr := post("/setup", nil, url.Values{
		"name": {"Mentor"}, "email": {"m@e.com"}, "password": {"verysecurepass123"},
	})
	cookies := rr.Result().Cookies()
	homeBody := get("/", cookies).Body.String()
	csrfStart := strings.Index(homeBody, `name="csrf" value="`)
	if csrfStart < 0 {
		t.Fatal("no csrf")
	}
	csrfTail := homeBody[csrfStart+len(`name="csrf" value="`):]
	csrf := csrfTail[:strings.Index(csrfTail, `"`)]
	bad := post("/rooms", cookies, url.Values{"csrf": {csrf}, "name": {"X"}, "mode": {"calssroom"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("typo mode: want 400, got %d", bad.Code)
	}
}

func TestAuthenticatedPostRequiresCSRF(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	form := url.Values{}
	form.Set("name", "Mentor")
	form.Set("email", "mentor@example.com")
	form.Set("password", "verysecurepass123")
	setupReq := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	setupReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setupRR := httptest.NewRecorder()
	handler.ServeHTTP(setupRR, setupReq)
	if setupRR.Code != http.StatusSeeOther {
		t.Fatalf("setup status = %d", setupRR.Code)
	}
	cookies := setupRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("setup did not issue session cookie")
	}

	roomForm := url.Values{}
	roomForm.Set("name", "Backend")
	roomReq := httptest.NewRequest(http.MethodPost, "/rooms", strings.NewReader(roomForm.Encode()))
	roomReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		roomReq.AddCookie(c)
	}
	roomRR := httptest.NewRecorder()
	handler.ServeHTTP(roomRR, roomReq)
	if roomRR.Code != http.StatusForbidden {
		t.Fatalf("expected missing CSRF to be forbidden, got %d", roomRR.Code)
	}
}

// TestProfileFlow exercises the /profile self-service endpoints: viewing
// the page, updating profile fields, hitting the email-taken error path,
// and changing the password (which revokes other sessions).
func TestProfileFlow(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	post := func(path string, cookies []*http.Cookie, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}
	get := func(path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	setupRR := post("/setup", nil, url.Values{
		"name": {"Mentor"}, "email": {"mentor@example.com"}, "password": {"verysecurepass123"},
	})
	if setupRR.Code != http.StatusSeeOther {
		t.Fatalf("setup status = %d", setupRR.Code)
	}
	cookies := setupRR.Result().Cookies()
	profileBody := get("/profile", cookies).Body.String()
	if !strings.Contains(profileBody, "mentor@example.com") {
		t.Fatal("profile page missing user email")
	}
	csrfStart := strings.Index(profileBody, `name="csrf" value="`)
	if csrfStart < 0 {
		t.Fatal("no csrf on profile")
	}
	csrfTail := profileBody[csrfStart+len(`name="csrf" value="`):]
	csrf := csrfTail[:strings.Index(csrfTail, `"`)]

	// Rename + change language.
	upd := post("/profile", cookies, url.Values{
		"csrf": {csrf}, "name": {"Renamed"}, "email": {"renamed@example.com"},
		"language": {"id"}, "engagement_notif": {"on"},
	})
	if upd.Code != http.StatusSeeOther {
		t.Fatalf("profile update status = %d body=%s", upd.Code, upd.Body.String())
	}
	after := get("/profile", cookies).Body.String()
	if !strings.Contains(after, "renamed@example.com") {
		t.Fatal("profile update did not persist email")
	}

	// Wrong current password is rejected with a redirect carrying ?err=current_password.
	bad := post("/profile/password", cookies, url.Values{
		"csrf": {csrf}, "current_password": {"wrong"}, "new_password": {"newverysecurepass"},
	})
	if bad.Code != http.StatusSeeOther || !strings.Contains(bad.Header().Get("Location"), "current_password") {
		t.Fatalf("wrong-password redirect: code=%d loc=%q", bad.Code, bad.Header().Get("Location"))
	}

	// Correct change works.
	ok := post("/profile/password", cookies, url.Values{
		"csrf": {csrf}, "current_password": {"verysecurepass123"}, "new_password": {"newverysecurepass456"},
	})
	if ok.Code != http.StatusSeeOther || !strings.Contains(ok.Header().Get("Location"), "saved=password") {
		t.Fatalf("password change: code=%d loc=%q", ok.Code, ok.Header().Get("Location"))
	}
}

// TestReportEditDelete walks a mentee writing a report, editing it, then
// the mentor deleting it via the standard CSRF-protected POST.
func TestReportEditDelete(t *testing.T) {
	h := newClassroomHarness(t) // reuse harness — mentee + mentor already set up
	// Make a mentorship room because reports apply to that mode.
	// Reuse handler/cookies from the harness; create a new room.
	postRoom := h.post("/rooms", h.mentorCookies, url.Values{
		"csrf": {h.mentorCSRF}, "name": {"MR"}, "mode": {"mentorship"},
	})
	if postRoom.Code != http.StatusSeeOther {
		t.Fatalf("create mentorship room: %d", postRoom.Code)
	}
	mrID := strings.TrimPrefix(postRoom.Result().Header.Get("Location"), "/rooms/")
	// Re-invite same mentee into the new room.
	inv := h.post("/rooms/"+mrID+"/invites", h.mentorCookies, url.Values{
		"csrf": {h.mentorCSRF}, "role": {"mentee"},
	})
	if inv.Code != http.StatusOK {
		t.Fatalf("invite: %d", inv.Code)
	}
	body := inv.Body.String()
	// invite_created renders the full join URL first. Pull the code
	// out of the URL's ?code= query parameter.
	codeStart := strings.Index(body, "?code=") + len("?code=")
	codeEnd := strings.Index(body[codeStart:], "</code>")
	code := body[codeStart : codeStart+codeEnd]
	// Mentee accepts via /join with a new account.
	joinRR := h.post("/join", nil, url.Values{
		"code": {code}, "name": {"M2"}, "email": {"m2@example.com"}, "password": {"verysecurepass123"},
	})
	if joinRR.Code != http.StatusSeeOther {
		t.Fatalf("join: %d", joinRR.Code)
	}
	mc := joinRR.Result().Cookies()
	// Pull a CSRF for the new mentee.
	getReq := httptest.NewRequest(http.MethodGet, "/rooms/"+mrID, nil)
	for _, c := range mc {
		getReq.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.handler.ServeHTTP(rr, getReq)
	roomBody := rr.Body.String()
	csrfStart := strings.Index(roomBody, `name="csrf" value="`)
	if csrfStart < 0 {
		t.Fatal("no csrf in new mentee room view")
	}
	mcsrf := roomBody[csrfStart+len(`name="csrf" value="`) : csrfStart+len(`name="csrf" value="`)+strings.Index(roomBody[csrfStart+len(`name="csrf" value="`):], `"`)]
	// Mentee posts a report.
	rep := h.post("/rooms/"+mrID+"/reports", mc, url.Values{
		"csrf": {mcsrf}, "learned": {"L"}, "practiced": {"P"}, "next_plan": {"N"},
	})
	if rep.Code != http.StatusSeeOther {
		t.Fatalf("create report: %d body=%s", rep.Code, rep.Body.String())
	}
	// Pull the report ID from the mentee's room view.
	getReq2 := httptest.NewRequest(http.MethodGet, "/rooms/"+mrID, nil)
	for _, c := range mc {
		getReq2.AddCookie(c)
	}
	rr2 := httptest.NewRecorder()
	h.handler.ServeHTTP(rr2, getReq2)
	body2 := rr2.Body.String()
	prefix := "/rooms/" + mrID + "/reports/"
	idx := strings.Index(body2, prefix)
	if idx < 0 {
		t.Fatal("no report card in mentee room view")
	}
	tail := body2[idx+len(prefix):]
	rid := tail[:strings.IndexAny(tail, "/\"")]
	// Edit it as the author.
	ed := h.post("/rooms/"+mrID+"/reports/"+rid+"/edit", mc, url.Values{
		"csrf": {mcsrf}, "learned": {"Updated"}, "practiced": {"P"}, "next_plan": {"N"},
	})
	if ed.Code != http.StatusSeeOther {
		t.Fatalf("edit report: %d body=%s", ed.Code, ed.Body.String())
	}
	// Mentor can delete it.
	del := h.post("/rooms/"+mrID+"/reports/"+rid+"/delete", h.mentorCookies, url.Values{
		"csrf": {h.mentorCSRF},
	})
	if del.Code != http.StatusSeeOther {
		t.Fatalf("delete report: %d body=%s", del.Code, del.Body.String())
	}
	// After delete, the report is gone from the mentor's view.
	getReq3 := httptest.NewRequest(http.MethodGet, "/rooms/"+mrID, nil)
	for _, c := range h.mentorCookies {
		getReq3.AddCookie(c)
	}
	rr3 := httptest.NewRecorder()
	h.handler.ServeHTTP(rr3, getReq3)
	if strings.Contains(rr3.Body.String(), prefix+rid) {
		t.Fatal("deleted report still appears on room page")
	}
}

// TestSignedInAcceptInvite covers the auto-join flow for an existing
// account. The mentee already has a session from `newClassroomHarness`;
// they click an invite to a brand-new room → the GET /join?code=…
// renders the confirmation page (not the registration form), and the
// POST /join/accept attaches them to the room without re-registering.
func TestSignedInAcceptInvite(t *testing.T) {
	h := newClassroomHarness(t)
	// Mentor stands up a second room.
	postRoom := h.post("/rooms", h.mentorCookies, url.Values{
		"csrf": {h.mentorCSRF}, "name": {"R2"}, "mode": {"mentorship"},
	})
	if postRoom.Code != http.StatusSeeOther {
		t.Fatalf("create second room: %d", postRoom.Code)
	}
	r2 := strings.TrimPrefix(postRoom.Result().Header.Get("Location"), "/rooms/")
	// Mentor creates an invite for that second room.
	inv := h.post("/rooms/"+r2+"/invites", h.mentorCookies, url.Values{
		"csrf": {h.mentorCSRF}, "role": {"mentee"},
	})
	if inv.Code != http.StatusOK {
		t.Fatalf("create invite: %d", inv.Code)
	}
	body := inv.Body.String()
	codeStart := strings.Index(body, "?code=") + len("?code=")
	codeEnd := strings.Index(body[codeStart:], "</code>")
	code := body[codeStart : codeStart+codeEnd]

	// GET /join?code=… as the signed-in mentee should render the
	// confirmation page, not auto-join.
	getReq := httptest.NewRequest(http.MethodGet, "/join?code="+code, nil)
	for _, c := range h.menteeCookies {
		getReq.AddCookie(c)
	}
	previewRR := httptest.NewRecorder()
	h.handler.ServeHTTP(previewRR, getReq)
	if previewRR.Code != http.StatusOK {
		t.Fatalf("preview render: %d", previewRR.Code)
	}
	if !strings.Contains(previewRR.Body.String(), "/join/accept") {
		t.Fatal("confirmation page should expose the /join/accept POST target")
	}
	// Membership should NOT exist yet — GET is read-only.
	pre := httptest.NewRequest(http.MethodGet, "/rooms/"+r2, nil)
	for _, c := range h.menteeCookies {
		pre.AddCookie(c)
	}
	preRR := httptest.NewRecorder()
	h.handler.ServeHTTP(preRR, pre)
	if preRR.Code != http.StatusNotFound {
		t.Fatalf("mentee should be 404 on the room before accepting; got %d", preRR.Code)
	}

	// POST /join/accept completes the join.
	accept := h.post("/join/accept", h.menteeCookies, url.Values{
		"csrf": {h.menteeCSRF}, "code": {code},
	})
	if accept.Code != http.StatusSeeOther {
		t.Fatalf("accept: %d body=%s", accept.Code, accept.Body.String())
	}
	if loc := accept.Header().Get("Location"); loc != "/rooms/"+r2 {
		t.Fatalf("accept redirect: want /rooms/%s, got %s", r2, loc)
	}
	// Membership now exists.
	post := httptest.NewRequest(http.MethodGet, "/rooms/"+r2, nil)
	for _, c := range h.menteeCookies {
		post.AddCookie(c)
	}
	postRR := httptest.NewRecorder()
	h.handler.ServeHTTP(postRR, post)
	if postRR.Code != http.StatusOK {
		t.Fatalf("mentee should be in the room after accepting; got %d", postRR.Code)
	}

	// Re-accepting the same code (already a member) should be
	// idempotent: redirect, no error.
	again := h.post("/join/accept", h.menteeCookies, url.Values{
		"csrf": {h.menteeCSRF}, "code": {code},
	})
	if again.Code != http.StatusSeeOther {
		t.Fatalf("re-accept idempotency broke: %d body=%s", again.Code, again.Body.String())
	}
}
