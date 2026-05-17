package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
	"sinau/internal/i18n"
	"sinau/internal/reminder"
	"sinau/internal/store"
)

type Server struct {
	store                *store.Store
	tpl                  *template.Template
	secureCookie         bool
	staticDir            string
	publicURL            string
	dummyHash            string
	authLimiter          *limiter
	notificationsEnabled bool
	engagement           *reminder.Engagement
}

type Config struct {
	Store                *store.Store
	Templates            string
	StaticDir            string
	SecureCookie         bool
	// PublicURL is the canonical scheme+host the app is served from
	// (e.g. https://sinau.example.com). Used to render absolute join
	// links the mentor can paste into a chat. Empty falls back to the
	// request's own Host header + scheme, which is fine in dev and most
	// straight-through prod setups.
	PublicURL            string
	NotificationsEnabled bool
	Engagement           *reminder.Engagement
}

type ctxKey string

const (
	userKey ctxKey = "user"
	langKey ctxKey = "lang"

	langCookie = "sinau_lang"
)

type PageData struct {
	Title                string
	TitleKey             string
	Lang                 i18n.Lang
	SupportedLangs       []i18n.Lang
	RequestPath          string
	User                 *domain.User
	CSRF                 string
	Error                string
	Notice               string
	SetupNeeded          bool
	NotificationsEnabled bool
	UserPoints           int
	Room                 domain.Room
	InvitePreview        domain.InvitePreview
	Members              []domain.Member
	Reports              []domain.Report
	Report               domain.Report
	Comments             []domain.Comment
	Tasks                []domain.Task
	Submissions          []domain.Submission
	PendingReviews       int
	Invites              []domain.Invite
	InviteCode           string
	JoinCode             string
	Stats                domain.Stats
	RoomMentees          []domain.Member
	Leaderboard          []domain.LeaderboardEntry
	MyPoints             int
	MyRank               domain.Rank
	MentorDash           domain.MentorDashboard
	MenteeDash           domain.MenteeDashboard
	Prefs                domain.NotificationPrefs
	Task                 domain.Task
	Profile              *domain.User
	SessionCount         int
	SearchQuery          string
	SearchHits           []domain.SearchHit
	CoachMetrics         domain.CoachMetrics
	Growth               domain.GrowthMetrics
	GradeRooms           []domain.GradeRoom
	WindowDays           int
	BaseURL              string // canonical scheme+host for absolute links (join URL etc.)
	ReturnTo             string // safe redirect path round-tripped through forms (login → join)
}

// T returns the localised string for key in the page's active language.
// Templates call this as {{.T "key"}}, or {{$.T "key"}} inside ranges.
func (p PageData) T(key string) string { return i18n.T(p.Lang, key) }

// Tf is T with fmt.Sprintf-style substitution. Used for strings with
// placeholders like "%d pts" or "Due %s".
func (p PageData) Tf(key string, args ...any) string { return i18n.Tf(p.Lang, key, args...) }

// RoleLabel translates a (mode, role) pair. Centralised here so templates
// never branch on raw "mentor"/"mentee" strings to pick a localised label.
func (p PageData) RoleLabel(mode, role string) string {
	return i18n.RoleLabel(p.Lang, mode, role)
}

// ModeLabel translates the room mode for the page's language.
func (p PageData) ModeLabel(mode string) string { return i18n.ModeLabel(p.Lang, mode) }

// LangLabel returns the native-language display name for a language code,
// used by the picker so each option shows in its own language.
func (p PageData) LangLabel(l i18n.Lang) string { return i18n.Label(l) }

// Initials renders the 1-2 letter monogram for the avatar chip.
// Templates call it as {{$.Initials .Name}} (or {{.Initials .User.Name}}
// at the top level). Centralised here so every list site reaches for
// the same helper instead of inlining {{printf "%.1s" .Name}}.
func (p PageData) Initials(name string) string { return domain.Initials(name) }

// AvatarBucket maps a user ID to a stable 0..N-1 color slot used by the
// `avatar-c{n}` CSS classes. Same ID → same chip colour across pages.
func (p PageData) AvatarBucket(id string) int { return domain.AvatarBucket(id) }

func New(cfg Config) (*Server, error) {
	tpl, err := template.ParseGlob(cfg.Templates + "/*.html")
	if err != nil {
		return nil, err
	}
	dummy, err := auth.HashPassword("sinau-login-timing-equalisation-only")
	if err != nil {
		return nil, err
	}
	return &Server{
		store:                cfg.Store,
		tpl:                  tpl,
		secureCookie:         cfg.SecureCookie,
		staticDir:            cfg.StaticDir,
		publicURL:            strings.TrimRight(cfg.PublicURL, "/"),
		dummyHash:            dummy,
		authLimiter:          newLimiter(0.2, 8),
		notificationsEnabled: cfg.NotificationsEnabled,
		engagement:           cfg.Engagement,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	mux.HandleFunc("GET /", s.withUser(s.home))
	mux.HandleFunc("GET /setup", s.withUser(s.setupForm))
	mux.HandleFunc("POST /setup", s.rateLimit(s.setup))
	mux.HandleFunc("GET /login", s.withUser(s.loginForm))
	mux.HandleFunc("POST /login", s.rateLimit(s.login))
	mux.HandleFunc("POST /logout", s.auth(s.logout))
	mux.HandleFunc("GET /join", s.withUser(s.joinForm))
	mux.HandleFunc("POST /join", s.rateLimit(s.join))
	mux.HandleFunc("POST /join/accept", s.auth(s.acceptInvite))
	mux.HandleFunc("POST /rooms", s.auth(s.createRoom))
	mux.HandleFunc("GET /rooms/{roomID}", s.auth(s.roomPage))
	mux.HandleFunc("POST /rooms/{roomID}/invites", s.auth(s.createInvite))
	mux.HandleFunc("POST /rooms/{roomID}/reports", s.auth(s.createReport))
	mux.HandleFunc("GET /rooms/{roomID}/reports/{reportID}", s.auth(s.reportPage))
	mux.HandleFunc("GET /rooms/{roomID}/reports/{reportID}/edit", s.auth(s.editReportForm))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/edit", s.auth(s.editReport))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/delete", s.auth(s.deleteReport))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/comments", s.auth(s.createComment))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/comments/{commentID}/edit", s.auth(s.editComment))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/comments/{commentID}/delete", s.auth(s.deleteComment))
	mux.HandleFunc("POST /rooms/{roomID}/tasks", s.auth(s.createTask))
	mux.HandleFunc("GET /rooms/{roomID}/tasks/{taskID}", s.auth(s.taskPage))
	mux.HandleFunc("POST /rooms/{roomID}/tasks/{taskID}/submit", s.auth(s.submitTask))
	mux.HandleFunc("GET /rooms/{roomID}/tasks/{taskID}/edit", s.auth(s.editTaskForm))
	mux.HandleFunc("POST /rooms/{roomID}/tasks/{taskID}/edit", s.auth(s.editTask))
	mux.HandleFunc("POST /rooms/{roomID}/tasks/{taskID}/delete", s.auth(s.deleteTask))
	mux.HandleFunc("POST /rooms/{roomID}/submissions/{submissionID}/review", s.auth(s.reviewSubmission))
	mux.HandleFunc("POST /rooms/{roomID}/settings", s.auth(s.updateRoomSettings))
	mux.HandleFunc("GET /profile", s.auth(s.profilePage))
	mux.HandleFunc("POST /profile", s.auth(s.updateProfile))
	mux.HandleFunc("POST /profile/password", s.auth(s.updatePassword))
	mux.HandleFunc("POST /profile/sessions/revoke-others", s.auth(s.revokeOtherSessions))
	mux.HandleFunc("GET /onboarding", s.auth(s.onboardingPage))
	mux.HandleFunc("POST /onboarding/skip", s.auth(s.skipOnboarding))
	mux.HandleFunc("GET /search", s.auth(s.searchPage))
	mux.HandleFunc("GET /search/results", s.auth(s.searchResults))
	mux.HandleFunc("GET /me/coaching", s.auth(s.coachingPage))
	mux.HandleFunc("GET /me/growth", s.auth(s.growthPage))
	mux.HandleFunc("GET /me/grades", s.auth(s.gradesPage))
	if s.notificationsEnabled {
		mux.HandleFunc("GET /settings", s.auth(s.settingsPage))
		mux.HandleFunc("POST /settings", s.auth(s.updateSettings))
	} else {
		// Explicit 404 so /settings doesn't fall through to the home
		// handler (Go's ServeMux treats "GET /" as a catch-all).
		mux.HandleFunc("GET /settings", http.NotFound)
		mux.HandleFunc("POST /settings", http.NotFound)
	}
	mux.HandleFunc("GET /help", s.withUser(s.helpPage))
	// /guide was a duplicate of /help in an earlier revision. Redirect any
	// bookmarks at it to the canonical URL.
	mux.HandleFunc("GET /guide", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/help", http.StatusMovedPermanently)
	})
	mux.HandleFunc("POST /lang", s.withUser(s.setLanguage))
	mux.HandleFunc("GET /partials/link-row", s.withUser(s.linkRowPartial))
	return securityHeaders(mux)
}

// linkRowPartial returns a single empty labelled-link form row. The
// multi-link form on reports and submissions calls this via htmx's
// hx-get to append another row when the user clicks "Add link". We
// render it through the same template engine as the rest of the app so
// localised placeholders stay in sync.
func (s *Server) linkRowPartial(w http.ResponseWriter, r *http.Request) {
	pd := s.pageData(r, "")
	s.render(w, "link_row", pd)
}

// setLanguage handles the topbar picker. Persists the choice to a cookie
// for anonymous visitors and, when the user is logged in, also to the
// users.language row so it follows them across browsers and gets used by
// notifications. CSRF is enforced only for logged-in users — anonymous
// visitors don't have a session-bound token yet.
func (s *Server) setLanguage(w http.ResponseWriter, r *http.Request) {
	lang := i18n.Lang(strings.ToLower(strings.TrimSpace(r.FormValue("lang"))))
	if !i18n.IsValid(lang) {
		http.Error(w, "unsupported language", http.StatusBadRequest)
		return
	}
	u := current(r)
	if u != nil && !s.validCSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     langCookie,
		Value:    string(lang),
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false, // readable by JS is fine — no secret content.
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	if u != nil {
		if err := s.store.SetUserLanguage(u.ID, string(lang)); err != nil {
			s.serverError(w, err)
			return
		}
	}
	to := r.FormValue("return_to")
	if to == "" || !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := s.currentUser(r)
		ctx := r.Context()
		if u != nil {
			ctx = context.WithValue(ctx, userKey, u)
		}
		ctx = context.WithValue(ctx, langKey, detectLang(r, u))
		next(w, r.WithContext(ctx))
	}
}

// detectLang resolves the request's UI language. Precedence:
//  1. Logged-in user's saved preference (users.language).
//  2. sinau_lang cookie (anonymous visitors and pre-login choice).
//  3. Accept-Language header.
//  4. Default (English).
func detectLang(r *http.Request, u *domain.User) i18n.Lang {
	userLang := ""
	if u != nil {
		userLang = u.Language
	}
	cookieLang := ""
	if c, err := r.Cookie(langCookie); err == nil {
		cookieLang = c.Value
	}
	return i18n.Detect(userLang, cookieLang, r.Header.Get("Accept-Language"))
}

func currentLang(r *http.Request) i18n.Lang {
	if v, ok := r.Context().Value(langKey).(i18n.Lang); ok {
		return v
	}
	return i18n.Default
}

// safeReturnPath sanitises a path for use as the language picker's
// return_to value. We only ever accept absolute single-slash paths to
// avoid open redirects.
func safeReturnPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	return p
}

// baseURL returns the canonical scheme://host prefix for absolute
// links (e.g. join URLs the mentor copies and pastes into a chat).
// SINAU_PUBLIC_URL wins when set so operators behind a reverse proxy
// can pin the host; otherwise we derive from the request, honouring
// X-Forwarded-Proto when present.
func (s *Server) baseURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		// Trust X-Forwarded-Proto only on the value side; the
		// caller upstream is supposed to strip incoming copies.
		// Accept "https" or "http"; ignore weirder values.
		if proto == "https" || proto == "http" {
			scheme = proto
		}
	}
	return scheme + "://" + r.Host
}

// normalizeScore validates and canonicalises a review score against the
// room's mode. Classroom uses 0–100 (gradebook); mentorship uses 1–5
// (leaderboard rubric). An empty value is allowed in either mode —
// the mentor may want to leave feedback without a grade.
func normalizeScore(raw, mode string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return "", false
	}
	if mode == domain.RoomModeMentorship {
		if n < 1 || n > 5 {
			return "", false
		}
	} else {
		if n < 0 || n > 100 {
			return "", false
		}
	}
	return strconv.Itoa(n), true
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return s.withUser(func(w http.ResponseWriter, r *http.Request) {
		if current(r) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !s.validCSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func (s *Server) currentUser(r *http.Request) (*domain.User, error) {
	c, err := r.Cookie("sinau_session")
	if err != nil || c.Value == "" {
		return nil, err
	}
	return s.store.CurrentUser(c.Value)
}

func current(r *http.Request) *domain.User {
	u, _ := r.Context().Value(userKey).(*domain.User)
	return u
}

func (s *Server) csrfFor(r *http.Request) string {
	c, err := r.Cookie("sinau_session")
	if err != nil {
		return ""
	}
	return s.store.CSRF(c.Value)
}

func (s *Server) validCSRF(r *http.Request) bool {
	got := r.FormValue("csrf")
	want := s.csrfFor(r)
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authLimiter.allow(clientIP(r)) {
			http.Error(w, "too many requests, slow down", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// pageData seeds a PageData with the bits every page needs: language,
// supported languages for the picker, notifications gating, and (for
// authenticated pages) the current user, CSRF token, and lifetime points.
// titleKey is an i18n key resolved on the page's active language.
func (s *Server) pageData(r *http.Request, titleKey string) PageData {
	u := current(r)
	lang := currentLang(r)
	pd := PageData{
		TitleKey:             titleKey,
		Title:                i18n.T(lang, titleKey),
		Lang:                 lang,
		SupportedLangs:       i18n.Supported,
		RequestPath:          safeReturnPath(r.URL.Path),
		NotificationsEnabled: s.notificationsEnabled,
		BaseURL:              s.baseURL(r),
	}
	if u != nil {
		pd.User = u
		pd.CSRF = s.csrfFor(r)
		pd.UserPoints = s.store.UserPointsTotal(u.ID)
	}
	return pd
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if u == nil {
		pd := s.pageData(r, "title.landing")
		pd.SetupNeeded = s.store.UserCount() == 0
		s.render(w, "landing", pd)
		return
	}
	// First-run onboarding: a newly registered user (no `users.onboarded_at`)
	// gets routed to the explainer page on their first visit to the
	// dashboard. The page itself is skippable so we never trap anyone
	// behind it.
	if !u.Onboarded {
		http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
		return
	}
	if s.store.CanCreateRooms(u.ID) || s.store.IsMentor(u.ID) {
		dash, err := s.store.MentorDashboard(u.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		pd := s.pageData(r, "title.dashboard.mentor")
		pd.MentorDash = dash
		s.render(w, "mentor_home", pd)
		return
	}
	dash, err := s.store.MenteeDashboard(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "title.dashboard.mentee")
	pd.MenteeDash = dash
	s.render(w, "mentee_home", pd)
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	if s.store.UserCount() > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup", s.pageData(r, "title.setup"))
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if s.store.UserCount() > 0 {
		http.Error(w, "setup already completed", http.StatusForbidden)
		return
	}
	name := auth.Clean(r.FormValue("name"), 80)
	email := strings.ToLower(auth.Clean(r.FormValue("email"), 160))
	password := r.FormValue("password")
	if name == "" || !auth.ValidEmail(email) || len(password) < 12 {
		pd := s.pageData(r, "title.setup")
		pd.Error = i18n.T(pd.Lang, "setup.error.invalid")
		s.render(w, "setup", pd)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	uid, err := s.store.CreateInitialRoomCreator(name, email, hash)
	if err == store.ErrSetupComplete {
		http.Error(w, "setup already completed", http.StatusForbidden)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	// The first user who runs /setup is by definition the operator —
	// they just typed a name/email/password into the bootstrap form, so
	// the explainer page would be condescending. Mark them onboarded
	// immediately. Onboarding stays on for everyone who arrives via /join.
	if err := s.store.MarkOnboarded(uid); err != nil {
		log.Printf("mark setup user onboarded uid=%s: %v", uid, err)
	}
	// Carry the pre-login language choice (if any) to the new user.
	s.persistLangChoice(r, uid)
	if err := s.issueSession(w, uid); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// persistLangChoice copies the visitor's sinau_lang cookie (set via the
// picker before they had an account) onto their newly-created user record,
// so the choice survives login and is used for notification content.
func (s *Server) persistLangChoice(r *http.Request, userID string) {
	c, err := r.Cookie(langCookie)
	if err != nil {
		return
	}
	lang := i18n.Lang(strings.ToLower(strings.TrimSpace(c.Value)))
	if !i18n.IsValid(lang) || lang == i18n.Default {
		return
	}
	if err := s.store.SetUserLanguage(userID, string(lang)); err != nil {
		log.Printf("persist lang choice user=%s: %v", userID, err)
	}
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	returnTo := safeReturnPath(r.URL.Query().Get("return_to"))
	if current(r) != nil {
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
		return
	}
	pd := s.pageData(r, "title.login")
	pd.SetupNeeded = s.store.UserCount() == 0
	pd.ReturnTo = returnTo
	s.render(w, "login", pd)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(auth.Clean(r.FormValue("email"), 160))
	password := r.FormValue("password")
	uid, hash, lookupErr := s.store.UserPasswordByEmail(email)
	// Always run argon2 verify against a real hash so timing does not
	// distinguish "no such user" from "wrong password".
	verifyAgainst := hash
	if lookupErr != nil {
		verifyAgainst = s.dummyHash
	}
	ok := auth.VerifyPassword(password, verifyAgainst)
	if lookupErr != nil || !ok {
		pd := s.pageData(r, "title.login")
		pd.Error = i18n.T(pd.Lang, "login.error")
		s.render(w, "login", pd)
		return
	}
	if err := s.issueSession(w, uid); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, safeReturnPath(r.FormValue("return_to")), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("sinau_session"); err == nil {
		_ = s.store.DeleteSession(c.Value)
	}
	s.clearSession(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) joinForm(w http.ResponseWriter, r *http.Request) {
	if s.store.UserCount() == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	code := auth.Clean(r.URL.Query().Get("code"), 120)
	preview := domain.InvitePreview{}
	if code != "" {
		preview = s.store.InvitePreview(code)
	}
	// Signed-in user with a valid invite → confirmation page.
	// Auto-join is intentionally NOT silent: a phishing link in HTML
	// email (e.g. <img src="…/join?code=…">) would otherwise enrol a
	// logged-in victim. POST + button click gates consent.
	if u := current(r); u != nil && preview.Valid {
		pd := s.pageData(r, "title.join")
		pd.JoinCode = code
		pd.InvitePreview = preview
		s.render(w, "join_confirm", pd)
		return
	}
	// Anonymous (or invalid/missing code) → render the existing
	// register-with-invite form. When code is valid but the visitor is
	// not signed in, the template also offers a "Log in instead" link
	// that round-trips through return_to back to this page.
	pd := s.pageData(r, "title.join")
	pd.JoinCode = code
	pd.InvitePreview = preview
	s.render(w, "join", pd)
}

// acceptInvite is the signed-in side of the join flow. The
// confirmation page (rendered by joinForm) posts here with a CSRF
// token; we attach the existing user to the room and redirect.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	code := auth.Clean(r.FormValue("code"), 120)
	if code == "" {
		http.Error(w, "invite code required", http.StatusBadRequest)
		return
	}
	roomID, err := s.store.AcceptInvite(code, u.ID)
	if err != nil {
		pd := s.pageData(r, "title.join")
		pd.JoinCode = code
		pd.InvitePreview = s.store.InvitePreview(code)
		pd.Error = i18n.T(pd.Lang, "join.error.failed")
		s.render(w, "join_confirm", pd)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	code := auth.Clean(r.FormValue("code"), 120)
	name := auth.Clean(r.FormValue("name"), 80)
	email := strings.ToLower(auth.Clean(r.FormValue("email"), 160))
	password := r.FormValue("password")
	preview := s.store.InvitePreview(code)
	if code == "" || name == "" || !auth.ValidEmail(email) || len(password) < 12 {
		pd := s.pageData(r, "title.join")
		pd.JoinCode = code
		pd.InvitePreview = preview
		pd.Error = i18n.T(pd.Lang, "join.error.invalid")
		s.render(w, "join", pd)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	uid, roomID, err := s.store.JoinWithInvite(code, name, email, hash)
	if err != nil {
		pd := s.pageData(r, "title.join")
		pd.JoinCode = code
		pd.InvitePreview = preview
		pd.Error = i18n.T(pd.Lang, "join.error.failed")
		s.render(w, "join", pd)
		return
	}
	s.persistLangChoice(r, uid)
	if err := s.issueSession(w, uid); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) createRoom(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if !s.store.CanCreateRooms(u.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := auth.Clean(r.FormValue("name"), 100)
	mode := auth.Clean(r.FormValue("mode"), 40)
	if name == "" {
		http.Error(w, "room name required", http.StatusBadRequest)
		return
	}
	if mode == "" {
		mode = domain.RoomModeMentorship
	}
	if !domain.ValidRoomMode(mode) {
		http.Error(w, "unknown room format", http.StatusBadRequest)
		return
	}
	roomID, err := s.store.CreateRoom(name, u.ID, mode)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) roomPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := s.store.RoomData(roomID, u.ID, role)
	if err != nil {
		s.serverError(w, err)
		return
	}
	rm.Role = role
	mentees := make([]domain.Member, 0, len(data.Members))
	for _, m := range data.Members {
		if m.Role == domain.RoleMentee {
			mentees = append(mentees, m)
		}
	}
	pd := s.pageData(r, "")
	pd.Title = rm.Name
	pd.TitleKey = ""
	pd.Room = rm
	pd.Members = data.Members
	pd.RoomMentees = mentees
	pd.Reports = data.Reports
	pd.Tasks = data.Tasks
	pd.Submissions = data.Submissions
	pd.PendingReviews = data.PendingReviews
	pd.Invites = data.Invites
	pd.Stats = data.Stats
	pd.MyPoints = data.MyPoints
	pd.MyRank = data.MyRank
	// data.Leaderboard is already empty for mentees in rooms with the
	// visibility toggle off (see RoomData), so a direct copy is safe.
	pd.Leaderboard = data.Leaderboard
	s.render(w, "room", pd)
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	inviteRole := r.FormValue("role")
	if inviteRole != domain.RoleMentor && inviteRole != domain.RoleMentee {
		http.Error(w, "bad invite role", http.StatusBadRequest)
		return
	}
	code, err := auth.RandomToken(24)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.CreateInvite(roomID, inviteRole, u.ID, code, time.Now().UTC().Add(7*24*time.Hour)); err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "")
	pd.InviteCode = code
	s.render(w, "invite_created", pd)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	if _, _, ok := s.store.RoomAccess(roomID, u.ID); !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	links, err := collectLinks(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	learned := auth.Clean(r.FormValue("learned"), 2000)
	practiced := auth.Clean(r.FormValue("practiced"), 2000)
	nextPlan := auth.Clean(r.FormValue("next_plan"), 2000)
	blocker := auth.Clean(r.FormValue("blocker"), 2000)
	if learned == "" || practiced == "" || nextPlan == "" {
		http.Error(w, "learned, practiced, and next plan are required", http.StatusBadRequest)
		return
	}
	if err := s.store.CreateReport(roomID, u.ID, learned, practiced, blocker, nextPlan, links); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

// collectLinks reads pair-wise `link_label[i]` / `link_url[i]` form
// fields off the request, drops empty rows, validates each URL, and
// returns the resulting slice in form-order. It's the shared parser
// used by both createReport and submitAssignment, since both flows
// expose the same multi-link UI.
//
// Validation rules:
//   - URLs must be http or https (auth.ValidExternalURL).
//   - A row with an empty URL is skipped entirely (the user removed it).
//   - An empty label gets a sensible default so something always renders.
//   - Caps each list at 8 entries to bound the form size.
func collectLinks(r *http.Request) ([]domain.Link, error) {
	const maxLinks = 8
	urls := r.Form["link_url"]
	labels := r.Form["link_label"]
	out := make([]domain.Link, 0, len(urls))
	for i, raw := range urls {
		url := auth.Clean(raw, 400)
		if url == "" {
			continue
		}
		if !auth.ValidExternalURL(url) {
			return nil, errors.New("link must be an http or https URL")
		}
		label := ""
		if i < len(labels) {
			label = auth.Clean(labels[i], 120)
		}
		if label == "" {
			label = "Link"
		}
		out = append(out, domain.Link{Label: label, URL: url})
		if len(out) >= maxLinks {
			break
		}
	}
	return out, nil
}

func (s *Server) reportPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID := r.PathValue("roomID"), r.PathValue("reportID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rep, err := s.store.ReportByID(roomID, reportID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if role != domain.RoleMentor && rep.UserID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	comments, err := s.store.Comments(reportID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	rm.Role = role
	pd := s.pageData(r, "title.report")
	pd.Room = rm
	pd.Report = rep
	pd.Comments = comments
	s.render(w, "report", pd)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID := r.PathValue("roomID"), r.PathValue("reportID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rep, err := s.store.ReportByID(roomID, reportID)
	if err != nil || (role != domain.RoleMentor && rep.UserID != u.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body := auth.Clean(r.FormValue("body"), 2000)
	if body == "" {
		http.Error(w, "comment required", http.StatusBadRequest)
		return
	}
	commentID, err := s.store.CreateComment(reportID, u.ID, body)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.dispatchReportComment(rep, u, body, commentID, roomID, reportID)
	http.Redirect(w, r, "/rooms/"+roomID+"/reports/"+reportID, http.StatusSeeOther)
}

// assignAllSentinel is the form value used in mentorship rooms to mean
// "broadcast to every mentee in this room". On the wire it maps to an
// empty `assigned_to` column. Classroom rooms always send this sentinel
// since assignments are always broadcast there.
const assignAllSentinel = "all"

// createTask is the unified create endpoint for both mentorship tasks
// and classroom assignments. The two flavours differ in defaults and
// in what gets validated:
//
//   - mentorship: assignedTo is a specific mentee, OR "all" for a
//     broadcast. detail is optional, resource_url is optional.
//   - classroom: assigned_to is always "all" (every student gets it);
//     detail (instructions) is required.
//
// The underlying row is the same.
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	assignedTo := r.FormValue("assigned_to")
	title := auth.Clean(r.FormValue("title"), 180)
	detail := auth.Clean(r.FormValue("detail"), 2000)
	resourceURL := auth.Clean(r.FormValue("resource_url"), 400)
	dueDate := auth.Clean(r.FormValue("due_date"), 10)
	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	if rm.Mode == domain.RoomModeClassroom {
		// Classroom mode treats every task as a broadcast and
		// requires instructions + a deadline — those are gradebook-
		// material defaults, not just convention.
		assignedTo = assignAllSentinel
		if detail == "" {
			http.Error(w, "instructions required", http.StatusBadRequest)
			return
		}
		if dueDate == "" {
			http.Error(w, "deadline required", http.StatusBadRequest)
			return
		}
	}
	if dueDate != "" {
		if _, err := time.Parse("2006-01-02", dueDate); err != nil {
			http.Error(w, "due date must use YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}
	if resourceURL != "" && !auth.ValidExternalURL(resourceURL) {
		http.Error(w, "resource link must be an http or https URL", http.StatusBadRequest)
		return
	}
	storedAssignee := ""
	if assignedTo != assignAllSentinel {
		if !s.store.IsMentee(roomID, assignedTo) {
			http.Error(w, "assignee must be a mentee in this room", http.StatusBadRequest)
			return
		}
		storedAssignee = assignedTo
	}
	if _, err := s.store.CreateTask(roomID, u.ID, storedAssignee, title, detail, resourceURL, dueDate); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

// taskPage renders the unified task detail view — the mentee/student
// submission form, or the mentor/teacher review queue summary. The
// page is the canonical place to attach work to a specific task.
func (s *Server) taskPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.TaskByID(roomID, taskID, u.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Mentees only see broadcast tasks or tasks individually assigned
	// to them.
	if role != domain.RoleMentor && t.AssigneeID != "" && t.AssigneeID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rm.Role = role
	pd := s.pageData(r, "title.task")
	pd.Room = rm
	pd.Task = t
	if role == domain.RoleMentor {
		subs, err := s.store.TaskSubmissions(roomID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		// Filter to just this task's submissions for the review view.
		filtered := subs[:0]
		for _, sub := range subs {
			if sub.TaskID == taskID {
				filtered = append(filtered, sub)
			}
		}
		pd.Submissions = filtered
	}
	s.render(w, "task_page", pd)
}

// submitTask is the unified student-submits-work endpoint, replacing
// the old per-mode submitAssignment / updateTaskStatus pair. It writes
// (or replaces) the student's task_submissions row plus its links and
// fires an engagement notification to the room's mentors.
func (s *Server) submitTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentee {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	links, err := collectLinks(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(links) == 0 {
		http.Error(w, "at least one submission link is required", http.StatusBadRequest)
		return
	}
	note := auth.Clean(r.FormValue("note"), 2000)
	if err := s.store.SubmitTask(roomID, taskID, u.ID, note, links); err != nil {
		s.serverError(w, err)
		return
	}
	// Engagement notification to mentors: read the task's title for
	// the email subject.
	if t, err := s.store.TaskByID(roomID, taskID, u.ID); err == nil {
		s.dispatchSubmissionMade(u, roomID, t.Title)
	}
	http.Redirect(w, r, "/rooms/"+roomID+"/tasks/"+taskID, http.StatusSeeOther)
}

// reviewSubmission is the unified mentor-reviews-work endpoint. The
// score is validated per-mode (1–5 in mentorship → leaderboard;
// 0–100 in classroom → gradebook only). The store handles the
// points_ledger insert atomically when applicable.
func (s *Server) reviewSubmission(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, submissionID := r.PathValue("roomID"), r.PathValue("submissionID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	status := r.FormValue("status")
	if status != "reviewed" && status != "revise" {
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	feedback := auth.Clean(r.FormValue("feedback"), 2000)
	score, valid := normalizeScore(r.FormValue("score"), rm.Mode)
	if !valid {
		if rm.Mode == domain.RoomModeMentorship {
			http.Error(w, "score must be empty or an integer 1-5", http.StatusBadRequest)
		} else {
			http.Error(w, "score must be empty or an integer 0-100", http.StatusBadRequest)
		}
		return
	}
	if status == "revise" && feedback == "" {
		http.Error(w, "revision feedback is required", http.StatusBadRequest)
		return
	}
	updated, err := s.store.ReviewTaskSubmission(roomID, submissionID, status, feedback, score, u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !updated {
		http.NotFound(w, r)
		return
	}
	if studentID, taskTitle, _, err := s.store.SubmissionContext(submissionID); err == nil {
		s.dispatchFeedbackPosted(studentID, roomID, taskTitle, feedback, score)
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) updateRoomSettings(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	visible := r.FormValue("leaderboard_visible") == "on" || r.FormValue("leaderboard_visible") == "1"
	if err := s.store.SetRoomLeaderboardVisible(roomID, visible); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

// settingsPage / updateSettings are only registered when
// notificationsEnabled is true (see Handler()). No second guard needed
// inside the handlers — the route layer owns the gate.
func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	pd := s.pageData(r, "title.settings")
	pd.Prefs = s.store.NotificationPrefsFor(u.ID)
	if notice := r.URL.Query().Get("saved"); notice == "1" {
		pd.Notice = i18n.T(pd.Lang, "settings.saved")
	}
	s.render(w, "settings", pd)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	enabled := r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1"
	channel := r.FormValue("channel")
	if !domain.ValidNotifChannel(channel) {
		http.Error(w, "invalid channel", http.StatusBadRequest)
		return
	}
	if !enabled {
		// Off-switch normalises channel for consistency.
		channel = domain.NotifChannelOff
	}
	prefs := domain.NotificationPrefs{
		UserID:         u.ID,
		Enabled:        enabled,
		Channel:        channel,
		WhatsAppNumber: auth.Clean(r.FormValue("whatsapp_number"), 32),
		TelegramChatID: auth.Clean(r.FormValue("telegram_chat_id"), 32),
	}
	if err := s.store.SetNotificationPrefs(prefs); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) helpPage(w http.ResponseWriter, r *http.Request) {
	pd := s.pageData(r, "title.help")
	pd.SetupNeeded = s.store.UserCount() == 0
	s.render(w, "help", pd)
}

func (s *Server) issueSession(w http.ResponseWriter, userID string) error {
	token, err := auth.RandomToken(32)
	if err != nil {
		return err
	}
	csrf, err := auth.RandomToken(32)
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(14 * 24 * time.Hour)
	if err := s.store.CreateSession(userID, token, csrf, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "sinau_session",
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "sinau_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteLaxMode})
}

func (s *Server) render(w http.ResponseWriter, name string, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render: %v", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// profilePage renders the /profile self-service view: name/email/language/
// engagement notif toggle, change-password form, and a Sessions section
// with the count of active sessions plus a "Sign out other devices"
// button. Each form posts to a separate endpoint so the success/error
// state for one (e.g. password) does not clobber the others.
func (s *Server) profilePage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	profile, err := s.store.UserByID(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	count, err := s.store.UserSessionCount(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "title.profile")
	pd.Profile = profile
	pd.SessionCount = count
	switch r.URL.Query().Get("saved") {
	case "profile":
		pd.Notice = i18n.T(pd.Lang, "profile.saved")
	case "password":
		pd.Notice = i18n.T(pd.Lang, "profile.password.saved")
	case "sessions":
		pd.Notice = i18n.T(pd.Lang, "profile.sessions.revoked")
	}
	switch r.URL.Query().Get("err") {
	case "email_taken":
		pd.Error = i18n.T(pd.Lang, "profile.error.email_taken")
	case "invalid":
		pd.Error = i18n.T(pd.Lang, "profile.error.invalid")
	case "current_password":
		pd.Error = i18n.T(pd.Lang, "profile.password.error.current")
	case "new_password":
		pd.Error = i18n.T(pd.Lang, "profile.password.error.new")
	}
	s.render(w, "profile", pd)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	name := auth.Clean(r.FormValue("name"), 80)
	email := strings.ToLower(auth.Clean(r.FormValue("email"), 160))
	lang := i18n.Lang(strings.ToLower(strings.TrimSpace(r.FormValue("language"))))
	engagement := r.FormValue("engagement_notif") == "on" || r.FormValue("engagement_notif") == "1"
	if name == "" || !auth.ValidEmail(email) || !i18n.IsValid(lang) {
		http.Redirect(w, r, "/profile?err=invalid", http.StatusSeeOther)
		return
	}
	err := s.store.UpdateUserProfile(u.ID, name, email, string(lang), engagement)
	if errors.Is(err, store.ErrEmailTaken) {
		http.Redirect(w, r, "/profile?err=email_taken", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/profile?saved=profile", http.StatusSeeOther)
}

func (s *Server) updatePassword(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")
	if len(next) < 12 {
		http.Redirect(w, r, "/profile?err=new_password", http.StatusSeeOther)
		return
	}
	hash, err := s.store.UserPasswordHash(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !auth.VerifyPassword(current, hash) {
		http.Redirect(w, r, "/profile?err=current_password", http.StatusSeeOther)
		return
	}
	newHash, err := auth.HashPassword(next)
	if err != nil {
		s.serverError(w, err)
		return
	}
	c, err := r.Cookie("sinau_session")
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.UpdateUserPassword(u.ID, newHash, c.Value); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/profile?saved=password", http.StatusSeeOther)
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	c, err := r.Cookie("sinau_session")
	if err != nil {
		s.serverError(w, err)
		return
	}
	if _, err := s.store.RevokeOtherSessions(u.ID, c.Value); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/profile?saved=sessions", http.StatusSeeOther)
}

// dispatchReportComment fires an engagement notification to the report
// author when someone else posts a comment on it. Self-comments are
// dropped — no point notifying yourself.
func (s *Server) dispatchReportComment(rep domain.Report, commenter *domain.User, body, commentID, roomID, reportID string) {
	if s.engagement == nil || rep.UserID == commenter.ID {
		return
	}
	room, _, ok := s.store.RoomAccess(roomID, rep.UserID)
	if !ok {
		return
	}
	go s.engagement.Dispatch(context.Background(), reminder.EngagementEvent{
		Kind:         reminder.EngagementReportComment,
		RecipientID:  rep.UserID,
		ActorName:    commenter.Name,
		RoomID:       roomID,
		RoomName:     room.Name,
		RoomMode:     room.Mode,
		Snippet:      reminder.Snippet(body, 240),
		DeepLinkPath: "/rooms/" + roomID + "/reports/" + reportID,
	})
}

// dispatchSubmissionMade notifies every mentor in the room that a
// student has submitted (or resubmitted) an assignment.
func (s *Server) dispatchSubmissionMade(student *domain.User, roomID, assignmentTitle string) {
	if s.engagement == nil {
		return
	}
	room, _, ok := s.store.RoomAccess(roomID, student.ID)
	if !ok {
		return
	}
	mentorIDs, err := s.store.MentorIDs(roomID)
	if err != nil {
		log.Printf("dispatchSubmissionMade mentorIDs room=%s: %v", roomID, err)
		return
	}
	for _, mid := range mentorIDs {
		mid := mid
		go s.engagement.Dispatch(context.Background(), reminder.EngagementEvent{
			Kind:         reminder.EngagementSubmissionMade,
			RecipientID:  mid,
			ActorName:    student.Name,
			RoomID:       roomID,
			RoomName:     room.Name,
			RoomMode:     room.Mode,
			Title:        assignmentTitle,
			DeepLinkPath: "/rooms/" + roomID,
		})
	}
}

// dispatchFeedbackPosted notifies the student that the teacher has
// reviewed (or asked them to revise) their submission. Score is
// included on "reviewed" outcomes when provided.
func (s *Server) dispatchFeedbackPosted(studentID, roomID, assignmentTitle, feedback, score string) {
	if s.engagement == nil || studentID == "" {
		return
	}
	room, _, ok := s.store.RoomAccess(roomID, studentID)
	if !ok {
		return
	}
	go s.engagement.Dispatch(context.Background(), reminder.EngagementEvent{
		Kind:         reminder.EngagementFeedbackPosted,
		RecipientID:  studentID,
		RoomID:       roomID,
		RoomName:     room.Name,
		RoomMode:     room.Mode,
		Title:        assignmentTitle,
		Score:        score,
		Snippet:      reminder.Snippet(feedback, 240),
		DeepLinkPath: "/rooms/" + roomID,
	})
}

// --- Edit/delete handlers -------------------------------------------------
//
// These all share the same pattern: load the resource, verify the
// viewer is the author OR a mentor in the room, mutate the row, then
// redirect back. Reads of the underlying resources already filter
// deleted_at, so a deleted row is effectively a 404 on subsequent
// page loads.

func (s *Server) editReportForm(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID := r.PathValue("roomID"), r.PathValue("reportID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	rep, err := s.store.ReportByID(roomID, reportID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if rep.UserID != u.ID && role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rm.Role = role
	pd := s.pageData(r, "title.report.edit")
	pd.Room = rm
	pd.Report = rep
	s.render(w, "report_edit", pd)
}

func (s *Server) editReport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID := r.PathValue("roomID"), r.PathValue("reportID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rep, err := s.store.ReportByID(roomID, reportID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if rep.UserID != u.ID && role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	links, err := collectLinks(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	learned := auth.Clean(r.FormValue("learned"), 2000)
	practiced := auth.Clean(r.FormValue("practiced"), 2000)
	nextPlan := auth.Clean(r.FormValue("next_plan"), 2000)
	blocker := auth.Clean(r.FormValue("blocker"), 2000)
	if learned == "" || practiced == "" || nextPlan == "" {
		http.Error(w, "learned, practiced, and next plan are required", http.StatusBadRequest)
		return
	}
	updated, err := s.store.UpdateReport(reportID, learned, practiced, blocker, nextPlan, links)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !updated {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID+"/reports/"+reportID, http.StatusSeeOther)
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID := r.PathValue("roomID"), r.PathValue("reportID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rep, err := s.store.ReportByID(roomID, reportID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if rep.UserID != u.ID && role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.store.DeleteReport(reportID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID, commentID := r.PathValue("roomID"), r.PathValue("reportID"), r.PathValue("commentID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	authorID, parentReport, err := s.store.CommentAuthor(commentID)
	if err != nil || parentReport != reportID {
		http.NotFound(w, r)
		return
	}
	if authorID != u.ID && role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body := auth.Clean(r.FormValue("body"), 2000)
	if body == "" {
		http.Error(w, "comment required", http.StatusBadRequest)
		return
	}
	if _, err := s.store.EditComment(commentID, body); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID+"/reports/"+reportID, http.StatusSeeOther)
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, reportID, commentID := r.PathValue("roomID"), r.PathValue("reportID"), r.PathValue("commentID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	authorID, parentReport, err := s.store.CommentAuthor(commentID)
	if err != nil || parentReport != reportID {
		http.NotFound(w, r)
		return
	}
	if authorID != u.ID && role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.store.DeleteComment(commentID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID+"/reports/"+reportID, http.StatusSeeOther)
}

func (s *Server) editTaskForm(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	t, err := s.store.TaskByID(roomID, taskID, u.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rm.Role = role
	pd := s.pageData(r, "title.task.edit")
	pd.Room = rm
	pd.Task = t
	s.render(w, "task_edit", pd)
}

func (s *Server) editTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	title := auth.Clean(r.FormValue("title"), 180)
	detail := auth.Clean(r.FormValue("detail"), 2000)
	resourceURL := auth.Clean(r.FormValue("resource_url"), 400)
	dueDate := auth.Clean(r.FormValue("due_date"), 10)
	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	if rm.Mode == domain.RoomModeClassroom && (detail == "" || dueDate == "") {
		http.Error(w, "instructions and deadline required for classroom assignments", http.StatusBadRequest)
		return
	}
	if dueDate != "" {
		if _, err := time.Parse("2006-01-02", dueDate); err != nil {
			http.Error(w, "due date must use YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}
	if resourceURL != "" && !auth.ValidExternalURL(resourceURL) {
		http.Error(w, "resource link must be an http or https URL", http.StatusBadRequest)
		return
	}
	updated, err := s.store.UpdateTask(taskID, title, detail, resourceURL, dueDate)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !updated {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID+"/tasks/"+taskID, http.StatusSeeOther)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.store.DeleteTask(taskID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

// onboardingPage renders the first-run explainer. It's intercepted as
// the home redirect target for users without `onboarded_at`, but
// remains viewable at any time afterwards (the help page links to it)
// so users can re-read the tour. Visiting it does NOT re-set the
// onboarded flag, and the "Skip" button on the page only stamps
// onboarded_at for fresh accounts.
func (s *Server) onboardingPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	pd := s.pageData(r, "title.onboarding")
	pd.SetupNeeded = s.store.CanCreateRooms(u.ID)
	s.render(w, "onboarding", pd)
}

func (s *Server) skipOnboarding(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if err := s.store.MarkOnboarded(u.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// searchPage renders the search shell + (if `q` is present) the result
// list inline. /search/results is the htmx target the topbar input
// posts to so typing doesn't reload the chrome.
func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	q := auth.Clean(r.URL.Query().Get("q"), 200)
	pd := s.pageData(r, "title.search")
	pd.SearchQuery = q
	if q != "" {
		hits, err := s.store.Search(u.ID, q, 40)
		if err != nil {
			s.serverError(w, err)
			return
		}
		pd.SearchHits = hits
	}
	s.render(w, "search", pd)
}

// searchResults is the htmx-target endpoint: same query as searchPage
// but renders only the result list partial so the topbar input can
// stream results without a full reload.
func (s *Server) searchResults(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	q := auth.Clean(r.URL.Query().Get("q"), 200)
	pd := s.pageData(r, "")
	pd.SearchQuery = q
	if q != "" {
		hits, err := s.store.Search(u.ID, q, 40)
		if err != nil {
			s.serverError(w, err)
			return
		}
		pd.SearchHits = hits
	}
	s.render(w, "search_results", pd)
}

func (s *Server) coachingPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if !s.store.IsMentor(u.ID) && !s.store.CanCreateRooms(u.ID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	window := parseWindowDays(r.URL.Query().Get("window"), 30)
	metrics, err := s.store.CoachMetrics(u.ID, window)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "title.coaching")
	pd.CoachMetrics = metrics
	pd.WindowDays = window
	s.render(w, "coaching", pd)
}

func (s *Server) growthPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	growth, err := s.store.GrowthMetrics(u.ID, 12)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "title.growth")
	pd.Growth = growth
	s.render(w, "growth", pd)
}

func (s *Server) gradesPage(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	rooms, err := s.store.StudentGrades(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "title.grades")
	pd.GradeRooms = rooms
	s.render(w, "grades", pd)
}

// parseWindowDays parses the `window` query string for self-metric
// pages; accepts 30 / 90 / 365 (1y) and falls back to def. The /me/*
// pages all use the same convention so the period switcher is shared
// across templates.
func parseWindowDays(raw string, def int) int {
	switch raw {
	case "30":
		return 30
	case "90":
		return 90
	case "365":
		return 365
	}
	return def
}
