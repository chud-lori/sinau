package web

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
	"sinau/internal/store"
)

type Server struct {
	store        *store.Store
	tpl          *template.Template
	secureCookie bool
	staticDir    string
}

type Config struct {
	Store        *store.Store
	Templates    string
	StaticDir    string
	SecureCookie bool
}

type ctxKey string

const userKey ctxKey = "user"

type PageData struct {
	Title       string
	User        *domain.User
	CSRF        string
	Error       string
	Notice      string
	SetupNeeded bool
	Rooms       []domain.Room
	Room        domain.Room
	Members     []domain.Member
	Reports     []domain.Report
	Report      domain.Report
	Comments    []domain.Comment
	Tasks       []domain.Task
	Invites     []domain.Invite
	InviteCode  string
	JoinCode    string
	Stats       domain.Stats
}

func New(cfg Config) (*Server, error) {
	tpl, err := template.ParseGlob(cfg.Templates + "/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{
		store:        cfg.Store,
		tpl:          tpl,
		secureCookie: cfg.SecureCookie,
		staticDir:    cfg.StaticDir,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.staticDir))))
	mux.HandleFunc("GET /", s.withUser(s.home))
	mux.HandleFunc("GET /setup", s.withUser(s.setupForm))
	mux.HandleFunc("POST /setup", s.setup)
	mux.HandleFunc("GET /login", s.withUser(s.loginForm))
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.auth(s.logout))
	mux.HandleFunc("GET /join", s.withUser(s.joinForm))
	mux.HandleFunc("POST /join", s.join)
	mux.HandleFunc("GET /rooms/{roomID}", s.auth(s.roomPage))
	mux.HandleFunc("POST /rooms/{roomID}/invites", s.auth(s.createInvite))
	mux.HandleFunc("POST /rooms/{roomID}/reports", s.auth(s.createReport))
	mux.HandleFunc("GET /rooms/{roomID}/reports/{reportID}", s.auth(s.reportPage))
	mux.HandleFunc("POST /rooms/{roomID}/reports/{reportID}/comments", s.auth(s.createComment))
	mux.HandleFunc("POST /rooms/{roomID}/tasks", s.auth(s.createTask))
	mux.HandleFunc("POST /rooms/{roomID}/tasks/{taskID}/status", s.auth(s.updateTask))
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
	return r.FormValue("csrf") != "" && r.FormValue("csrf") == s.csrfFor(r)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	if u == nil {
		if s.store.UserCount() == 0 {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	rooms, err := s.store.RoomsFor(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "home", PageData{Title: "Rooms", User: u, CSRF: s.csrfFor(r), Rooms: rooms})
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
	roomName := auth.Clean(r.FormValue("room_name"), 100)
	if name == "" || !auth.ValidEmail(email) || len(password) < 12 || roomName == "" {
		s.render(w, "setup", PageData{Title: "Setup", Error: "Use a name, valid email, room name, and password with at least 12 characters."})
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	uid, err := s.store.CreateInitialRoom(name, email, hash, roomName)
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
	uid, hash, err := s.store.UserPasswordByEmail(email)
	if err != nil || !auth.VerifyPassword(password, hash) {
		time.Sleep(300 * time.Millisecond)
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
	s.render(w, "join", PageData{Title: "Join", JoinCode: code})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	code := auth.Clean(r.FormValue("code"), 120)
	name := auth.Clean(r.FormValue("name"), 80)
	email := strings.ToLower(auth.Clean(r.FormValue("email"), 160))
	password := r.FormValue("password")
	if code == "" || name == "" || !auth.ValidEmail(email) || len(password) < 12 {
		s.render(w, "join", PageData{Title: "Join", JoinCode: code, Error: "Use invite code, name, valid email, and password with at least 12 characters."})
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	uid, roomID, err := s.store.JoinWithInvite(code, name, email, hash)
	if err != nil {
		s.render(w, "join", PageData{Title: "Join", JoinCode: code, Error: "Invite is invalid, used, expired, or the email is already registered."})
		return
	}
	if err := s.issueSession(w, uid); err != nil {
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
	s.render(w, "room", PageData{Title: rm.Name, User: u, CSRF: s.csrfFor(r), Room: rm, Members: data.Members, Reports: data.Reports, Tasks: data.Tasks, Invites: data.Invites, Stats: data.Stats})
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
	s.render(w, "report", PageData{Title: "Report", User: u, CSRF: s.csrfFor(r), Room: rm, Report: rep, Comments: comments})
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

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	u := current(r)
	roomID := r.PathValue("roomID")
	_, role, ok := s.store.RoomAccess(roomID, u.ID)
	if !ok || role != domain.RoleMentor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	assignedTo := r.FormValue("assigned_to")
	if !s.store.IsMember(roomID, assignedTo) {
		http.Error(w, "unknown assignee", http.StatusBadRequest)
		return
	}
	title := auth.Clean(r.FormValue("title"), 180)
	detail := auth.Clean(r.FormValue("detail"), 1200)
	if title == "" {
		http.Error(w, "task title required", http.StatusBadRequest)
		return
	}
	if err := s.store.CreateTask(roomID, assignedTo, u.ID, title, detail); err != nil {
		s.serverError(w, err)
		return
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
