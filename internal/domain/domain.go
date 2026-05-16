package domain

import "database/sql"

const (
	RoleMentor  = "mentor"
	RoleLearner = "learner"
)

type User struct {
	ID    string
	Name  string
	Email string
}

type Room struct {
	ID                 string
	Name               string
	CreatedAt          string
	Role               string
	LeaderboardVisible bool
}

const (
	NotifChannelOff   = "off"
	NotifChannelEmail = "email"
	NotifChannelLog   = "log"
)

type NotificationPrefs struct {
	UserID    string
	Enabled   bool
	Channel   string
	UpdatedAt string
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

type Stats struct {
	BlockedReports   int
	WaitingReports   int
	InactiveLearners int
	OpenTasks        int
	DueSoonTasks     int
	OverdueTasks     int
}

type TaskReminder struct {
	TaskID        string
	Title         string
	Detail        string
	DueDate       string
	RoomID        string
	RoomName      string
	AssigneeID    string
	AssigneeName  string
	AssigneeEmail string
}

type RoomData struct {
	Members     []Member
	Reports     []Report
	Tasks       []Task
	Invites     []Invite
	Stats       Stats
	Leaderboard []LeaderboardEntry
	MyPoints    int
	MyRank      Rank
}

type MentorDashboard struct {
	Rooms          []Room
	Summary        DashboardSummary
	AttentionItems []AttentionItem
	Learners       []LearnerProgress
}

type LearnerDashboard struct {
	Rooms         []Room
	Summary       DashboardSummary
	Tasks         []Task
	RecentReports []Report
}

type DashboardSummary struct {
	Rooms            int
	ActiveLearners   int
	WaitingFeedback  int
	Blockers         int
	OpenTasks        int
	DueSoonTasks     int
	OverdueTasks     int
	InactiveLearners int
	ReportsThisWeek  int
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

type LearnerProgress struct {
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
