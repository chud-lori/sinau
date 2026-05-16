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
	ID        string
	Name      string
	CreatedAt string
	Role      string
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
	ID        string
	Title     string
	Detail    string
	Status    string
	Assignee  string
	DueDate   string
	DueState  string
	CreatedAt string
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
	Members []Member
	Reports []Report
	Tasks   []Task
	Invites []Invite
	Stats   Stats
}
