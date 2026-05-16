package web

import (
	"context"
	"crypto/subtle"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
	"sinau/internal/store"
)

type Server struct {
	store                *store.Store
	tpl                  *template.Template
	secureCookie         bool
	staticDir            string
	dummyHash            string
	authLimiter          *limiter
	notificationsEnabled bool
}

type Config struct {
	Store                *store.Store
	Templates            string
	StaticDir            string
	SecureCookie         bool
	NotificationsEnabled bool
}

type ctxKey string

const userKey ctxKey = "user"

type PageData struct {
	Title                string
	User                 *domain.User
	CSRF                 string
	Error                string
	Notice               string
	SetupNeeded          bool
	NotificationsEnabled bool
	UserPoints           int
	Rooms                []domain.Room
	Room                 domain.Room
	InvitePreview        domain.InvitePreview
	Members              []domain.Member
	Reports              []domain.Report
	Report               domain.Report
	Comments             []domain.Comment
	Tasks                []domain.Task
	Assignments          []domain.Assignment
	Submissions          []domain.Submission
	PendingReviews       int
	Invites              []domain.Invite
	InviteCode           string
	JoinCode             string
	Stats                domain.Stats
	RoomLearners         []domain.Member
	Leaderboard          []domain.LeaderboardEntry
	MyPoints             int
	MyRank               domain.Rank
	MentorDash           domain.MentorDashboard
	LearnerDash          domain.LearnerDashboard
	Prefs                domain.NotificationPrefs
}

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
		dummyHash:            dummy,
		authLimiter:          newLimiter(0.2, 8),
		notificationsEnabled: cfg.NotificationsEnabled,
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
	mux.HandleFunc("POST /rooms", s.auth(s.createRoom))
	mux.HandleFunc("GET /rooms/{roomID}", s.auth(s.roomPage))
	mux.HandleFunc("POST /rooms/{roomID}/invites", s.auth(s.createInvite))
	mux.HandleFunc("POST /rooms/{roomID}/reports", s.auth(s.createReport))
	mux.HandleFunc("GET /rooms/{roomID}/reports/{reportID}", s.auth(s.reportPage))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/comments", s.auth(s.createComment))
	mux.HandleFunc("POST /rooms/{roomID}/tasks", s.auth(s.createTask))
	mux.HandleFunc("POST /rooms/{roomID}/tasks/{taskID}/status", s.auth(s.updateTask))
	mux.HandleFunc("POST /rooms/{roomID}/tasks/{taskID}/review", s.auth(s.reviewTask))
	mux.HandleFunc("POST /rooms/{roomID}/assignments", s.auth(s.createAssignment))
	mux.HandleFunc("POST /rooms/{roomID}/assignments/{assignmentID}/submissions", s.auth(s.submitAssignment))
	mux.HandleFunc("POST /rooms/{roomID}/submissions/{submissionID}/review", s.auth(s.reviewSubmission))
	mux.HandleFunc("POST /rooms/{roomID}/settings", s.auth(s.updateRoomSettings))
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
	return securityHeaders(mux)
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
		if u != nil {
			r = r.WithContext(context.WithValue(r.Context(), userKey, u))
		}
		next(w, r)
	}
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

// pageData seeds a PageData with the bits every authenticated page needs:
// the current user, a session-scoped CSRF token, and their lifetime points
// total (used by the topbar chip). Handlers extend it with page-specific
// fields.
func (s *Server) pageData(r *http.Request, title string) PageData {
	u := current(r)
	pd := PageData{Title: title, NotificationsEnabled: s.notificationsEnabled}
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
		s.render(w, "landing", PageData{Title: "Mentor-led learning rooms", SetupNeeded: s.store.UserCount() == 0})
		return
	}
	if s.store.CanCreateRooms(u.ID) || s.store.IsMentor(u.ID) {
		dash, err := s.store.MentorDashboard(u.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		pd := s.pageData(r, "Mentor Dashboard")
		pd.Rooms = dash.Rooms
		pd.MentorDash = dash
		s.render(w, "mentor_home", pd)
		return
	}
	dash, err := s.store.LearnerDashboard(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pd := s.pageData(r, "My Progress")
	pd.Rooms = dash.Rooms
	pd.LearnerDash = dash
	s.render(w, "learner_home", pd)
}

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	if s.store.UserCount() > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, "setup", PageData{Title: "Setup"})
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
		s.render(w, "setup", PageData{Title: "Setup", Error: "Use a name, valid email, and password with at least 12 characters."})
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
	if err := s.issueSession(w, uid); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if current(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login", PageData{Title: "Login", SetupNeeded: s.store.UserCount() == 0})
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
		s.render(w, "login", PageData{Title: "Login", Error: "Invalid email or password."})
		return
	}
	if err := s.issueSession(w, uid); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
	pd := PageData{Title: "Join", JoinCode: code}
	if code != "" {
		pd.InvitePreview = s.store.InvitePreview(code)
	}
	s.render(w, "join", pd)
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	code := auth.Clean(r.FormValue("code"), 120)
	name := auth.Clean(r.FormValue("name"), 80)
	email := strings.ToLower(auth.Clean(r.FormValue("email"), 160))
	password := r.FormValue("password")
	preview := s.store.InvitePreview(code)
	if code == "" || name == "" || !auth.ValidEmail(email) || len(password) < 12 {
		s.render(w, "join", PageData{Title: "Join", JoinCode: code, InvitePreview: preview, Error: "Use invite code, name, valid email, and password with at least 12 characters."})
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	uid, roomID, err := s.store.JoinWithInvite(code, name, email, hash)
	if err != nil {
		s.render(w, "join", PageData{Title: "Join", JoinCode: code, InvitePreview: preview, Error: "Invite is invalid, used, expired, or the email is already registered."})
		return
	}
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
	learners := make([]domain.Member, 0, len(data.Members))
	for _, m := range data.Members {
		if m.Role == domain.RoleLearner {
			learners = append(learners, m)
		}
	}
	pd := s.pageData(r, rm.Name)
	pd.Room = rm
	pd.Members = data.Members
	pd.RoomLearners = learners
	pd.Reports = data.Reports
	pd.Tasks = data.Tasks
	pd.Assignments = data.Classroom.Assignments
	pd.Submissions = data.Classroom.Submissions
	pd.PendingReviews = data.Classroom.PendingReviews
	pd.Invites = data.Invites
	pd.Stats = data.Stats
	pd.MyPoints = data.MyPoints
	pd.MyRank = data.MyRank
	// data.Leaderboard is already empty for learners in rooms with the
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
	if inviteRole != domain.RoleMentor && inviteRole != domain.RoleLearner {
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
	s.render(w, "invite_created", PageData{InviteCode: code})
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	if _, _, ok := s.store.RoomAccess(roomID, u.ID); !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	link := auth.Clean(r.FormValue("link_url"), 400)
	if link != "" && !auth.ValidExternalURL(link) {
		http.Error(w, "link must be an http or https URL", http.StatusBadRequest)
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
	if err := s.store.CreateReport(roomID, u.ID, learned, practiced, blocker, nextPlan, link); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
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
	pd := s.pageData(r, "Report")
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
	if err := s.store.CreateComment(reportID, u.ID, body); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID+"/reports/"+reportID, http.StatusSeeOther)
}

// assignAllSentinel is the form value used to mean "create one task per
// learner in this room". It is intentionally not a valid 32-char hex user
// ID so it can never collide with a real assignee.
const assignAllSentinel = "all"

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	assignedTo := r.FormValue("assigned_to")
	title := auth.Clean(r.FormValue("title"), 180)
	detail := auth.Clean(r.FormValue("detail"), 1200)
	dueDate := auth.Clean(r.FormValue("due_date"), 10)
	if title == "" {
		http.Error(w, "task title required", http.StatusBadRequest)
		return
	}
	if dueDate != "" {
		if _, err := time.Parse("2006-01-02", dueDate); err != nil {
			http.Error(w, "due date must use YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}
	if assignedTo == assignAllSentinel {
		count, err := s.store.CreateTaskForLearners(roomID, u.ID, title, detail, dueDate)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if count == 0 {
			http.Error(w, "no learners in this room to assign", http.StatusBadRequest)
			return
		}
	} else {
		if !s.store.IsLearner(roomID, assignedTo) {
			http.Error(w, "assignee must be a learner in this room", http.StatusBadRequest)
			return
		}
		if err := s.store.CreateTask(roomID, assignedTo, u.ID, title, detail, dueDate); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	status := r.FormValue("status")
	if status != "todo" && status != "doing" && status != "done" {
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	updated, err := s.store.UpdateTaskStatus(roomID, taskID, u.ID, role, status)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !updated {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) reviewTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, taskID := r.PathValue("roomID"), r.PathValue("taskID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	points, err := strconv.Atoi(r.FormValue("points"))
	if err != nil || points < 1 || points > 5 {
		http.Error(w, "score must be an integer 1-5", http.StatusBadRequest)
		return
	}
	awarded, err := s.store.ReviewTask(roomID, taskID, u.ID, points)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !awarded {
		http.Error(w, "task is not awaiting review", http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) createAssignment(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor || rm.Mode != domain.RoomModeClassroom {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	title := auth.Clean(r.FormValue("title"), 180)
	instructions := auth.Clean(r.FormValue("instructions"), 2000)
	resourceURL := auth.Clean(r.FormValue("resource_url"), 400)
	dueDate := auth.Clean(r.FormValue("due_date"), 10)
	if title == "" || instructions == "" || dueDate == "" {
		http.Error(w, "title, instructions, and deadline are required", http.StatusBadRequest)
		return
	}
	if _, err := time.Parse("2006-01-02", dueDate); err != nil {
		http.Error(w, "deadline must use YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	if resourceURL != "" && !auth.ValidExternalURL(resourceURL) {
		http.Error(w, "resource link must be an http or https URL", http.StatusBadRequest)
		return
	}
	if err := s.store.CreateAssignment(roomID, u.ID, title, instructions, resourceURL, dueDate); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) submitAssignment(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, assignmentID := r.PathValue("roomID"), r.PathValue("assignmentID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleLearner || rm.Mode != domain.RoomModeClassroom {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	linkURL := auth.Clean(r.FormValue("link_url"), 400)
	note := auth.Clean(r.FormValue("note"), 1600)
	if linkURL == "" || !auth.ValidExternalURL(linkURL) {
		http.Error(w, "submission link must be an http or https URL", http.StatusBadRequest)
		return
	}
	if err := s.store.SubmitAssignment(roomID, assignmentID, u.ID, linkURL, note); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/rooms/"+roomID, http.StatusSeeOther)
}

func (s *Server) reviewSubmission(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID, submissionID := r.PathValue("roomID"), r.PathValue("submissionID")
	rm, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor || rm.Mode != domain.RoomModeClassroom {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	status := r.FormValue("status")
	if status != "reviewed" && status != "revise" {
		http.Error(w, "bad status", http.StatusBadRequest)
		return
	}
	feedback := auth.Clean(r.FormValue("feedback"), 2000)
	score := auth.Clean(r.FormValue("score"), 80)
	if status == "revise" && feedback == "" {
		http.Error(w, "revision feedback is required", http.StatusBadRequest)
		return
	}
	updated, err := s.store.ReviewSubmission(roomID, submissionID, status, feedback, score)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !updated {
		http.NotFound(w, r)
		return
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
	pd := s.pageData(r, "Settings")
	pd.Prefs = s.store.NotificationPrefsFor(u.ID)
	if notice := r.URL.Query().Get("saved"); notice == "1" {
		pd.Notice = "Notification settings saved."
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
	pd := s.pageData(r, "How Sinau Works")
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
