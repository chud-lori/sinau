package domain

import "database/sql"

const (
	RoleMentor = "mentor"
	RoleMentee = "mentee"
)

const (
	RoomModeMentorship = "mentorship"
	RoomModeClassroom  = "classroom"
)

// ValidRoomMode reports whether s is a recognised room mode. Used at the
// handler boundary so a typo in the form value doesn't silently degrade
// into "mentorship".
func ValidRoomMode(s string) bool {
	switch s {
	case RoomModeMentorship, RoomModeClassroom:
		return true
	}
	return false
}

type User struct {
	ID       string
	Name     string
	Email    string
	Language string
}

type Room struct {
	ID                 string
	Name               string
	Mode               string
	CreatedAt          string
	Role               string
	LeaderboardVisible bool
}

// RoleLabel returns the user-facing label for a role within this room's
// mode. Mentorship rooms use "Mentor" / "Mentee"; classroom rooms use
// "Teacher" / "Student". Templates should never render raw role/mode
// strings — always go through this helper.
func (r Room) RoleLabel(role string) string {
	if r.Mode == RoomModeClassroom {
		switch role {
		case RoleMentor:
			return "Teacher"
		case RoleMentee:
			return "Student"
		}
	}
	switch role {
	case RoleMentor:
		return "Mentor"
	case RoleMentee:
		return "Mentee"
	}
	return role
}

// MyRoleLabel is RoleLabel applied to the current viewer's role.
func (r Room) MyRoleLabel() string { return r.RoleLabel(r.Role) }

// ModeLabel returns a human-readable label for the room's workflow.
func (r Room) ModeLabel() string {
	switch r.Mode {
	case RoomModeClassroom:
		return "Classroom"
	case RoomModeMentorship:
		return "Mentorship"
	}
	return r.Mode
}

const (
	NotifChannelOff      = "off"
	NotifChannelEmail    = "email"
	NotifChannelLog      = "log"
	NotifChannelWhatsApp = "whatsapp"
	NotifChannelTelegram = "telegram"
)

// ValidNotifChannel reports whether s is a recognised notification channel.
// Validation lives here (not in a DB CHECK) so adding a channel is a code-
// only change — no migration required.
func ValidNotifChannel(s string) bool {
	switch s {
	case NotifChannelOff, NotifChannelEmail, NotifChannelLog,
		NotifChannelWhatsApp, NotifChannelTelegram:
		return true
	}
	return false
}

type NotificationPrefs struct {
	UserID         string
	Enabled        bool
	Channel        string
	WhatsAppNumber string
	TelegramChatID string
	UpdatedAt      string
}

type LeaderboardEntry struct {
	UserID string
	Name   string
	Points int
	Rank   int
}

type Rank struct {
	Position int
	Total    int
}

type Member struct {
	UserID     string
	Name       string
	Email      string
	Role       string
	CreatedAt  string
	LastReport string
	OpenTasks  int
}

type Report struct {
	ID        string
	RoomID    string
	UserID    string
	Author    string
	Learned   string
	Practiced string
	Blocker   string
	NextPlan  string
	LinkURL   string
	CreatedAt string
	Comments  int
}

type Comment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt string
}

type Task struct {
	ID            string
	Title         string
	Detail        string
	Status        string
	Assignee      string
	AssigneeID    string
	DueDate       string
	DueState      string
	CreatedAt     string
	PointsAwarded int
	ReviewedAt    string
	ReviewedBy    string
}

type Invite struct {
	Code      string
	Role      string
	ExpiresAt string
	UsedAt    sql.NullString
}

type Assignment struct {
	ID                 string
	RoomID             string
	Title              string
	Instructions       string
	ResourceURL        string
	DueDate            string
	CreatedAt          string
	Submitted          int
	TotalMentees       int
	MySubmissionStatus string
	MySubmissionURL    string
	MyFeedback         string
	MyScore            string
}

type Submission struct {
	ID              string
	AssignmentID    string
	AssignmentTitle string
	StudentID       string
	StudentName     string
	StudentEmail    string
	LinkURL         string
	Note            string
	Status          string
	Feedback        string
	Score           string
	SubmittedAt     string
	ReviewedAt      string
}

type ClassroomData struct {
	Assignments    []Assignment
	Submissions    []Submission
	PendingReviews int
}

// InvitePreview is the public-safe view of an invite, used to show the
// joiner what they're about to join (room name, mode, role) before they
// submit name/email/password. Mode-aware via the room's mode.
type InvitePreview struct {
	RoomName string
	RoomMode string
	Role     string
	Valid    bool
}

// RoleLabel translates the invited role using the same convention as
// Room.RoleLabel.
func (p InvitePreview) RoleLabel() string {
	if p.RoomMode == RoomModeClassroom {
		switch p.Role {
		case RoleMentor:
			return "Teacher"
		case RoleMentee:
			return "Student"
		}
	}
	switch p.Role {
	case RoleMentor:
		return "Mentor"
	case RoleMentee:
		return "Mentee"
	}
	return p.Role
}

// ModeLabel mirrors Room.ModeLabel for the preview.
func (p InvitePreview) ModeLabel() string {
	switch p.RoomMode {
	case RoomModeClassroom:
		return "Classroom"
	case RoomModeMentorship:
		return "Mentorship"
	}
	return p.RoomMode
}

type Stats struct {
	BlockedReports  int
	WaitingReports  int
	InactiveMentees int
	OpenTasks       int
	DueSoonTasks    int
	OverdueTasks    int
}

type TaskReminder struct {
	TaskID           string
	Title            string
	Detail           string
	DueDate          string
	RoomID           string
	RoomName         string
	AssigneeID       string
	AssigneeName     string
	AssigneeEmail    string
	AssigneeLanguage string
}

// AssignmentReminder is a single (assignment, mentee) pair the worker
// should ping about an approaching classroom deadline. The store query
// fans an assignment out into one record per mentee who has not yet
// submitted, so the worker dispatches each record exactly like a
// TaskReminder.
type AssignmentReminder struct {
	AssignmentID    string
	Title           string
	Instructions    string
	DueDate         string
	RoomID          string
	RoomName        string
	MenteeID        string
	MenteeName      string
	MenteeEmail     string
	MenteeLanguage  string
}

type RoomData struct {
	Members     []Member
	Reports     []Report
	Tasks       []Task
	Invites     []Invite
	Classroom   ClassroomData
	Stats       Stats
	Leaderboard []LeaderboardEntry
	MyPoints    int
	MyRank      Rank
}

type MentorDashboard struct {
	Rooms          []Room
	Summary        DashboardSummary
	AttentionItems []AttentionItem
	Mentees        []MenteeProgress
}

type MenteeDashboard struct {
	Rooms         []Room
	Summary       DashboardSummary
	Tasks         []Task
	RecentReports []Report
}

type DashboardSummary struct {
	Rooms           int
	ActiveMentees   int
	WaitingFeedback int
	Blockers        int
	OpenTasks       int
	DueSoonTasks    int
	OverdueTasks    int
	InactiveMentees int
	ReportsThisWeek int
}

type AttentionItem struct {
	Kind      string
	RoomID    string
	RoomName  string
	UserID    string
	UserName  string
	Title     string
	Detail    string
	DueDate   string
	CreatedAt string
}

type MenteeProgress struct {
	UserID          string
	Name            string
	Email           string
	RoomID          string
	RoomName        string
	LastReport      string
	ReportsThisWeek int
	OpenTasks       int
	OverdueTasks    int
	Blockers        int
	Status          string
}
