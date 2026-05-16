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
