package domain

import (
	"database/sql"
	"html/template"
	"strings"
)

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
	ID                string
	Name              string
	Email             string
	Language          string
	EngagementEnabled bool
	Onboarded         bool
}

type Room struct {
	ID                 string
	Name               string
	Mode               string
	CreatedAt          string
	Role               string
	LeaderboardVisible bool
}

// RoomCard is the dashboard-list view of a room — the bare Room shape
// plus a few denormalised counters so the room card can show why it's
// worth opening (mentee count, pending review work, overdue tasks,
// last-activity timestamp). All counts are role-aware:
//
//   - For a mentor viewer: PendingReviews = submitted-but-not-reviewed
//     in the room; OverdueTasks = open tasks past deadline across all
//     mentees.
//   - For a mentee viewer: PendingReviews = their own awaiting-review
//     submissions; OverdueTasks = their own past-deadline tasks.
//
// Other dashboard reads (RoomData, MentorDashboard summary) still use
// the lean Room shape — these counters are only computed for the card
// row, not every room query.
type RoomCard struct {
	Room
	MenteeCount    int
	PendingReviews int
	OverdueTasks   int
	LastActivity   string // empty when the room has no reports or submissions yet
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

// WorkLabel returns the room-appropriate label for the unified "task"
// concept: "Task" in mentorship rooms, "Assignment" in classroom rooms.
// The underlying data is the same; the label is purely a UX
// affordance so each mode reads naturally to its audience.
func (r Room) WorkLabel() string {
	if r.Mode == RoomModeClassroom {
		return "Assignment"
	}
	return "Task"
}

// ScoreMax returns the highest valid score for the room's mode.
// Mentorship reviews use a 1–5 rubric (rolls into points_ledger);
// classroom reviews use 0–100 (gradebook). The web layer reuses this
// to validate teacher/mentor input and to render the score chip.
func (r Room) ScoreMax() int {
	if r.Mode == RoomModeClassroom {
		return 100
	}
	return 5
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
	Links     []Link
	CreatedAt string
	EditedAt  string
	Comments  int
}

// Link is a labelled URL the mentee attaches to a report or submission so
// the mentor sees what the work is at a glance (e.g. "Notebook" → Colab,
// "Writeup" → Google Doc). Sorted by Position in the parent's list.
type Link struct {
	ID       string
	Label    string
	URL      string
	Position int
}

type Comment struct {
	ID        string
	AuthorID  string
	Author    string
	Body      string
	CreatedAt string
	EditedAt  string
}

// Task is the unified work-item used by both mentorship and classroom
// rooms. AssigneeID = "" means the task is broadcast to every mentee in
// the room (classroom mode is always like this; mentorship "assign to
// all" lands here too). Status is derived per viewer from the matching
// task_submissions row — Task itself carries no lifecycle column.
//
// MyXxx fields are populated when Tasks() is called by a mentee/student:
// they describe the viewer's own submission so the dashboard can render
// status pills without a second query per row.
type Task struct {
	ID            string
	RoomID        string
	CreatedByID   string
	Title         string
	Detail        string
	ResourceURL   string
	Assignee      string // human-readable; "" when broadcast
	AssigneeID    string // "" when broadcast
	DueDate       string
	DueState      string
	CreatedAt     string
	EditedAt      string

	// Viewer-specific submission summary (mentee/student view). Empty
	// strings mean "no submission yet"; status == 'reviewed' means
	// the mentor/teacher has graded.
	MySubmissionID    string
	MySubmissionStatus string
	MySubmissionLinks  []Link
	MyFeedback         string
	MyScore            string

	// Mentor/teacher aggregate counts when the task is broadcast.
	Submitted    int
	TotalStudents int
}

type Invite struct {
	Code      string
	Role      string
	ExpiresAt string
	UsedAt    sql.NullString
}

// Submission is one student's response to a task. Mentorship rooms have
// a max of one submission per (task, mentee) pair; classroom rooms have
// the same — the UNIQUE constraint enforces it. Resubmission overwrites
// in place and clears the review state.
type Submission struct {
	ID          string
	TaskID      string
	TaskTitle   string
	StudentID   string
	StudentName string
	StudentEmail string
	Links       []Link
	Note        string
	Status      string
	Feedback    string
	Score       string
	ReviewedBy  string
	SubmittedAt string
	ReviewedAt  string
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

// TaskReminder is one (task, recipient) pair the worker should ping
// about an approaching deadline. Today this covers both mentorship
// (individually-assigned tasks) and classroom (broadcast tasks fanned
// out into one record per unsubmitted student); the worker dispatches
// each row through the same notifier path.
type TaskReminder struct {
	TaskID           string
	Title            string
	Detail           string
	DueDate          string
	RoomID           string
	RoomName         string
	RoomMode         string
	AssigneeID       string
	AssigneeName     string
	AssigneeEmail    string
	AssigneeLanguage string
}

type RoomData struct {
	Members        []Member
	Reports        []Report
	Tasks          []Task
	Invites        []Invite
	Submissions    []Submission // mentor/teacher view: queue of submissions to review
	PendingReviews int
	Stats          Stats
	Leaderboard    []LeaderboardEntry
	MyPoints       int
	MyRank         Rank
}

// CoachMetrics is the data behind /me/coaching. Pure read-model, computed
// from existing rows in a single query bundle. Active vs lapsed mentees
// uses a 14-day no-activity threshold.
type CoachMetrics struct {
	WindowDays               int
	CommentsLeft             int
	SubmissionsReviewed      int
	SubmissionsTotal         int
	AvgFirstCommentHours     float64
	FirstCommentCount        int
	ActiveMentees            int
	LapsedMentees            int
}

// GrowthMetrics is the data behind /me/growth. Weeks is the last 12
// ISO-week buckets of reports submitted (oldest first). Streak is the
// consecutive count of weeks with at least one report ending at the
// current ISO week. Topics is a small frequency table of distinct words
// from learned/blocker fields, top-10 only.
type GrowthMetrics struct {
	Weeks       []WeekCount
	Streak      int
	TaskRate    float64 // 0..1 — done / (done + open) in the window
	TaskDone    int
	TaskOpen    int
	Blockers    int
	Topics      []TopicCount
	ReportsAll  int
	WindowWeeks int
}

type WeekCount struct {
	Label string // "Apr 1" — Monday of the week
	Count int
}

// HeightBucket maps a raw count to one of 11 CSS classes (h-0..h-10) used
// by the sparkline. Inline style="height:..." would violate the strict
// CSP, so the template emits class names and the stylesheet owns the
// pixel values.
func (w WeekCount) HeightBucket() int {
	if w.Count >= 10 {
		return 10
	}
	return w.Count
}

// TaskRatePct returns the task-completion rate as a 0..100 percentage,
// suitable for direct interpolation into the localised "%.0f%%" string.
func (g GrowthMetrics) TaskRatePct() float64 { return g.TaskRate * 100 }

type TopicCount struct {
	Word  string
	Count int
}

// GradeRow is one assignment row in /me/grades, scoped to a single
// student. Status is "on-time" | "late" | "missing" | "revise" |
// "submitted" | "reviewed". Computed at query time from
// submitted_at vs due_date.
type GradeRow struct {
	AssignmentID    string
	AssignmentTitle string
	DueDate         string
	Status          string
	Score           string
	Feedback        string
	SubmittedAt     string
}

type GradeRoom struct {
	RoomID       string
	RoomName     string
	Rows         []GradeRow
	AverageScore float64
	ScoredCount  int
	OnTimePct    float64
	TotalCount   int
}

type MentorDashboard struct {
	Rooms          []RoomCard
	Summary        DashboardSummary
	AttentionItems []AttentionItem
	Mentees        []MenteeProgress
}

type MenteeDashboard struct {
	Rooms         []RoomCard
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

// SearchHit is one row in the /search results. Kind tells the renderer
// which DeepLink to build; Snippet is FTS5-generated HTML-safe text with
// <mark>...</mark> around the match terms. RoomMode is included so the
// label can be "Teacher" vs "Mentor" without an extra round-trip.
type SearchHit struct {
	Kind         string // "report" | "comment" | "task" | "submission"
	ID           string
	RoomID       string
	RoomName     string
	RoomMode     string
	Title        string // resource title (assignment title, task title, "report by X", etc.)
	Author       string
	Snippet      string // user content, with \x02 / \x03 wrapping match terms
	CreatedAt    string
	DeepLinkPath string
}

// SafeSnippet returns the search snippet with user content HTML-escaped
// and the FTS5 match markers (ASCII STX / ETX) swapped for <mark> tags.
// The roundabout marker choice means we never trust raw user text and
// never accidentally render their literal "<script>"; only the marker
// pair becomes HTML.
func (h SearchHit) SafeSnippet() template.HTML {
	escaped := template.HTMLEscapeString(h.Snippet)
	escaped = strings.ReplaceAll(escaped, "\x02", "<mark>")
	escaped = strings.ReplaceAll(escaped, "\x03", "</mark>")
	return template.HTML(escaped)
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
