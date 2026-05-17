package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sinau.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createUserRoom(t *testing.T, st *Store, name, email string) (string, string) {
	t.Helper()
	hash, err := auth.HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateInitialRoomCreator(name, email, hash)
	if err != nil {
		t.Fatal(err)
	}
	roomID, err := st.CreateRoom("Backend", uid, domain.RoomModeMentorship)
	if err != nil {
		t.Fatal(err)
	}
	rooms, err := st.RoomsFor(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 {
		t.Fatalf("expected one room, got %d", len(rooms))
	}
	if rooms[0].ID != roomID {
		t.Fatalf("expected created room %s, got %s", roomID, rooms[0].ID)
	}
	return uid, roomID
}

func TestMigrateHandlesPreRebaseClassroomMigration3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sinau.db")
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1, '2026-01-01T00:00:00Z'), (2, '2026-01-01T00:00:00Z'), (3, '2026-01-01T00:00:00Z')`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE memberships (room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, role TEXT NOT NULL CHECK(role IN ('mentor','mentee')), created_at TEXT NOT NULL, PRIMARY KEY(room_id, user_id))`,
		`CREATE TABLE rooms (id TEXT PRIMARY KEY, name TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'mentorship', created_by TEXT NOT NULL REFERENCES users(id), created_at TEXT NOT NULL)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, assigned_to TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, assigned_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, title TEXT NOT NULL, detail TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('todo','doing','done')), created_at TEXT NOT NULL, updated_at TEXT NOT NULL, due_date TEXT NOT NULL DEFAULT '', last_reminded_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE assignments (id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, created_by TEXT NOT NULL REFERENCES users(id), title TEXT NOT NULL, instructions TEXT NOT NULL, resource_url TEXT NOT NULL, due_date TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE submissions (id TEXT PRIMARY KEY, assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE, student_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, link_url TEXT NOT NULL, note TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('submitted','reviewed','revise')), feedback TEXT NOT NULL, score TEXT NOT NULL, submitted_at TEXT NOT NULL, reviewed_at TEXT NOT NULL, UNIQUE(assignment_id, student_id))`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, tc := range []struct {
		table  string
		column string
	}{
		{"rooms", "mode"},
		{"rooms", "leaderboard_visible"},
		{"users", "can_create_rooms"},
		{"tasks", "points_awarded"},
		{"tasks", "reviewed_at"},
		{"notification_prefs", "whatsapp_number"},
		{"notification_prefs", "telegram_chat_id"},
	} {
		ok, err := st.columnExists(tc.table, tc.column)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("expected %s.%s to exist after compatibility migration", tc.table, tc.column)
		}
	}
}

func createInvite(t *testing.T, st *Store, roomID, mentorID, role string) string {
	t.Helper()
	code, err := auth.RandomToken(24)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(roomID, role, mentorID, code, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return code
}

func joinMentee(t *testing.T, st *Store, code, name, email string) (string, string) {
	t.Helper()
	hash, err := auth.HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	uid, roomID, err := st.JoinWithInvite(code, name, email, hash)
	if err != nil {
		t.Fatal(err)
	}
	return uid, roomID
}

func TestInitialSetupOnlyRunsOnce(t *testing.T) {
	st := newTestStore(t)
	hash, err := auth.HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateInitialRoomCreator("Mentor", "mentor@example.com", hash); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateInitialRoomCreator("Other", "other@example.com", hash)
	if err != ErrSetupComplete {
		t.Fatalf("expected ErrSetupComplete, got %v", err)
	}
	if got := st.UserCount(); got != 1 {
		t.Fatalf("expected single user after blocked second setup, got %d", got)
	}
}

func TestInitialRoomCreatorCanCreateFirstRoomAfterSetup(t *testing.T) {
	st := newTestStore(t)
	hash, err := auth.HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	mentorID, err := st.CreateInitialRoomCreator("Mentor", "mentor@example.com", hash)
	if err != nil {
		t.Fatal(err)
	}
	if !st.CanCreateRooms(mentorID) {
		t.Fatal("initial account should be able to create rooms")
	}
	if st.IsMentor(mentorID) {
		t.Fatal("initial account should not be a room mentor before joining or creating a room")
	}
	rooms, err := st.RoomsFor(mentorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 0 {
		t.Fatalf("setup should not create a room, got %d", len(rooms))
	}
	roomID, err := st.CreateRoom("Class A", mentorID, domain.RoomModeClassroom)
	if err != nil {
		t.Fatal(err)
	}
	rm, role, ok := st.RoomAccess(roomID, mentorID)
	if !ok || role != domain.RoleMentor || rm.Mode != domain.RoomModeClassroom {
		t.Fatalf("created room access broken: room=%+v role=%q ok=%v", rm, role, ok)
	}
	if !st.IsMentor(mentorID) {
		t.Fatal("room creator should become mentor in the created room")
	}
}

func TestRoomMentorInviteDoesNotGrantCreateRoomsCapability(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentor)
	coMentorID, _ := joinMentee(t, st, code, "Co Mentor", "co@example.com")

	if !st.IsMentor(coMentorID) {
		t.Fatal("mentor invite should grant mentor role inside the room")
	}
	if st.CanCreateRooms(coMentorID) {
		t.Fatal("room mentor invite should not grant global room creation capability")
	}
	if _, err := st.CreateRoom("Other", coMentorID, domain.RoomModeMentorship); err == nil {
		t.Fatal("room mentor without capability created a new room")
	}
}

func TestInviteCanOnlyBeUsedOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)

	menteeID, joinedRoomID := joinMentee(t, st, code, "Mentee", "mentee@example.com")
	if menteeID == "" || joinedRoomID != roomID {
		t.Fatal("mentee did not join expected room")
	}

	hash, err := auth.HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.JoinWithInvite(code, "Other", "other@example.com", hash); err == nil {
		t.Fatal("used invite accepted twice")
	}
}

func TestMenteeOnlySeesOwnReports(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "Mentee A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "Mentee B", "b@example.com")

	if err := st.CreateReport(roomID, menteeA, "A learned", "A practiced", "", "A next", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateReport(roomID, menteeB, "B learned", "B practiced", "", "B next", ""); err != nil {
		t.Fatal(err)
	}

	mentorReports, err := st.Reports(roomID, mentorID, domain.RoleMentor)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentorReports) != 2 {
		t.Fatalf("mentor expected 2 reports, got %d", len(mentorReports))
	}
	menteeReports, err := st.Reports(roomID, menteeA, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(menteeReports) != 1 || menteeReports[0].UserID != menteeA {
		t.Fatalf("mentee report visibility broken: %+v", menteeReports)
	}
}

func TestMentorCanCreateMultipleRooms(t *testing.T) {
	st := newTestStore(t)
	mentorID, firstRoomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	secondRoomID, err := st.CreateRoom("Frontend", mentorID, domain.RoomModeMentorship)
	if err != nil {
		t.Fatal(err)
	}
	if secondRoomID == "" || secondRoomID == firstRoomID {
		t.Fatal("second room id was not unique")
	}
	rooms, err := st.RoomsFor(mentorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 {
		t.Fatalf("expected two rooms, got %d", len(rooms))
	}
	if !st.CanCreateRooms(mentorID) {
		t.Fatal("mentor should be able to create rooms")
	}
}

func TestClassroomAssignmentsCanBeSubmittedAndReviewed(t *testing.T) {
	st := newTestStore(t)
	mentorID, _ := createUserRoom(t, st, "Mentor", "mentor@example.com")
	roomID, err := st.CreateRoom("Data Science", mentorID, domain.RoomModeClassroom)
	if err != nil {
		t.Fatal(err)
	}
	rm, role, ok := st.RoomAccess(roomID, mentorID)
	if !ok || role != domain.RoleMentor || rm.Mode != domain.RoomModeClassroom {
		t.Fatalf("classroom room access broken: room=%+v role=%q ok=%v", rm, role, ok)
	}
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Student", "student@example.com")

	if err := st.CreateAssignment(roomID, mentorID, "Build notebook", "Train a small model", "https://docs.google.com/doc", "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	studentAssignments, err := st.Assignments(roomID, menteeID, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(studentAssignments) != 1 || studentAssignments[0].MySubmissionStatus != "" {
		t.Fatalf("expected one unsubmitted assignment, got %+v", studentAssignments)
	}
	if err := st.SubmitAssignment(roomID, studentAssignments[0].ID, menteeID, "https://colab.research.google.com/notebook", "Finished baseline"); err != nil {
		t.Fatal(err)
	}
	submissions, err := st.Submissions(roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(submissions) != 1 || submissions[0].Status != "submitted" || submissions[0].StudentName != "Student" {
		t.Fatalf("expected submitted student work, got %+v", submissions)
	}
	updated, err := st.ReviewSubmission(roomID, submissions[0].ID, "reviewed", "Good baseline", "90")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("review did not update submission")
	}
	studentAssignments, err = st.Assignments(roomID, menteeID, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if studentAssignments[0].MySubmissionStatus != "reviewed" || studentAssignments[0].MyScore != "90" {
		t.Fatalf("mentee did not see review result: %+v", studentAssignments[0])
	}
}

func TestCreateTaskForMenteesAssignsEveryMenteeOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "Mentee A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "Mentee B", "b@example.com")

	count, err := st.CreateTaskForMentees(roomID, mentorID, "Read RFC", "details", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 tasks created, got %d", count)
	}

	tasksA, err := st.Tasks(roomID, menteeA, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	tasksB, err := st.Tasks(roomID, menteeB, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksA) != 1 || len(tasksB) != 1 {
		t.Fatalf("expected exactly one task per mentee, got A=%d B=%d", len(tasksA), len(tasksB))
	}
	if tasksA[0].ID == tasksB[0].ID {
		t.Fatal("bulk assign produced shared task ID across mentees")
	}

	// Mentor is not a mentee: bulk should never assign to mentor.
	mentorTasks, err := st.Tasks(roomID, mentorID, domain.RoleMentor)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range mentorTasks {
		if tk.Assignee == "Mentor" {
			t.Fatalf("mentor received a task from bulk assign: %+v", tk)
		}
	}
}

func TestCreateTaskForMenteesIsZeroWhenNoMentees(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	count, err := st.CreateTaskForMentees(roomID, mentorID, "Read RFC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 tasks in mentor-only room, got %d", count)
	}
}

func TestIsMenteeRejectsMentorsAndOutsiders(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	if st.IsMentee(roomID, mentorID) {
		t.Fatal("mentor incorrectly classified as mentee")
	}
	if st.IsMentee(roomID, "non-existent-user") {
		t.Fatal("non-member classified as mentee")
	}
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")
	if !st.IsMentee(roomID, menteeID) {
		t.Fatal("mentee not recognised by IsMentee")
	}
}

func TestTaskUpdateAuthorization(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "Mentee A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "Mentee B", "b@example.com")

	if err := st.CreateTask(roomID, menteeA, mentorID, "Read", "Read docs", ""); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.Tasks(roomID, menteeA, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}

	updated, err := st.UpdateTaskStatus(roomID, tasks[0].ID, menteeB, domain.RoleMentee, "done")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("mentee updated another mentee's task")
	}
	updated, err = st.UpdateTaskStatus(roomID, tasks[0].ID, mentorID, domain.RoleMentor, "done")
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("mentor could not update task")
	}
}

func TestMemberOpenTaskCountDoesNotMultiplyByReports(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")

	for i := 0; i < 3; i++ {
		if err := st.CreateReport(roomID, menteeID, "learned", "practiced", "", "next", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateTask(roomID, menteeID, mentorID, "One task", "detail", ""); err != nil {
		t.Fatal(err)
	}
	members, err := st.Members(roomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.UserID == menteeID && m.OpenTasks != 1 {
			t.Fatalf("expected one open task, got %d", m.OpenTasks)
		}
	}
}

func TestTaskDueDateAndReminders(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")

	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	dueSoon := now.Add(24 * time.Hour).Format("2006-01-02")
	if err := st.CreateTask(roomID, menteeID, mentorID, "Due soon", "detail", dueSoon); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.Tasks(roomID, menteeID, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].DueDate != dueSoon {
		t.Fatalf("due date not stored: %+v", tasks)
	}

	rems, err := st.DueTaskReminders(now, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(rems) != 1 || rems[0].Title != "Due soon" {
		t.Fatalf("expected one reminder, got %+v", rems)
	}
	if err := st.MarkTaskReminded(rems[0].TaskID, now); err != nil {
		t.Fatal(err)
	}
	rems, err = st.DueTaskReminders(now, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(rems) != 0 {
		t.Fatalf("reminder repeated same day: %+v", rems)
	}
}

func TestReviewTaskAwardsPointsOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")

	if err := st.CreateTask(roomID, menteeID, mentorID, "Read RFC", "details", ""); err != nil {
		t.Fatal(err)
	}
	tasks, _ := st.Tasks(roomID, menteeID, domain.RoleMentee)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	taskID := tasks[0].ID

	// Cannot review a task that is not yet done.
	if ok, err := st.ReviewTask(roomID, taskID, mentorID, 4); err != nil || ok {
		t.Fatalf("review pre-done should not award (ok=%v err=%v)", ok, err)
	}

	// Mark done, then review.
	if _, err := st.UpdateTaskStatus(roomID, taskID, menteeID, domain.RoleMentee, "done"); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ReviewTask(roomID, taskID, mentorID, 4)
	if err != nil || !ok {
		t.Fatalf("first review should succeed (ok=%v err=%v)", ok, err)
	}
	if got := st.UserPointsTotal(menteeID); got != 4 {
		t.Fatalf("total points want 4, got %d", got)
	}

	// Second review must no-op (idempotent).
	ok, err = st.ReviewTask(roomID, taskID, mentorID, 5)
	if err != nil || ok {
		t.Fatalf("re-review should not award (ok=%v err=%v)", ok, err)
	}
	if got := st.UserPointsTotal(menteeID); got != 4 {
		t.Fatalf("total points unchanged after re-review, got %d", got)
	}

	// Reviewed tasks lock status.
	updated, err := st.UpdateTaskStatus(roomID, taskID, mentorID, domain.RoleMentor, "todo")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("status change should be locked once reviewed")
	}
}

func TestRoomLeaderboardOrderAndRank(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeC := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "B", "b@example.com")
	menteeC, _ := joinMentee(t, st, codeC, "C", "c@example.com")

	for who, pts := range map[string]int{menteeA: 5, menteeB: 3, menteeC: 5} {
		if err := st.CreateTask(roomID, who, mentorID, "t", "", ""); err != nil {
			t.Fatal(err)
		}
		tasks, _ := st.Tasks(roomID, who, domain.RoleMentee)
		if _, err := st.UpdateTaskStatus(roomID, tasks[0].ID, who, domain.RoleMentee, "done"); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.ReviewTask(roomID, tasks[0].ID, mentorID, pts); err != nil || !ok {
			t.Fatalf("review failed user=%s err=%v ok=%v", who, err, ok)
		}
	}
	board, err := st.RoomLeaderboard(roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 3 {
		t.Fatalf("want 3 entries, got %d", len(board))
	}
	if board[0].Rank != 1 || board[1].Rank != 1 || board[2].Rank != 2 {
		t.Fatalf("dense ranking wrong: %+v", board)
	}
	rank, err := st.UserRankInRoom(menteeB, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if rank.Position != 2 || rank.Total != 3 {
		t.Fatalf("mentee B rank want 2/3, got %+v", rank)
	}
}

func TestNotificationPrefsDefaultOff(t *testing.T) {
	st := newTestStore(t)
	mentorID, _ := createUserRoom(t, st, "Mentor", "mentor@example.com")
	prefs := st.NotificationPrefsFor(mentorID)
	if prefs.Enabled || prefs.Channel != domain.NotifChannelOff {
		t.Fatalf("default prefs not off: %+v", prefs)
	}
	if err := st.SetNotificationPrefs(domain.NotificationPrefs{
		UserID: mentorID, Enabled: true, Channel: domain.NotifChannelEmail,
	}); err != nil {
		t.Fatal(err)
	}
	prefs = st.NotificationPrefsFor(mentorID)
	if !prefs.Enabled || prefs.Channel != domain.NotifChannelEmail {
		t.Fatalf("prefs not persisted: %+v", prefs)
	}
}

func TestNotificationPrefsPersistContactFields(t *testing.T) {
	st := newTestStore(t)
	mentorID, _ := createUserRoom(t, st, "Mentor", "mentor@example.com")
	in := domain.NotificationPrefs{
		UserID:         mentorID,
		Enabled:        true,
		Channel:        domain.NotifChannelWhatsApp,
		WhatsAppNumber: "+6281234567890",
		TelegramChatID: "12345",
	}
	if err := st.SetNotificationPrefs(in); err != nil {
		t.Fatal(err)
	}
	got := st.NotificationPrefsFor(mentorID)
	if got.Channel != domain.NotifChannelWhatsApp || got.WhatsAppNumber != in.WhatsAppNumber || got.TelegramChatID != in.TelegramChatID {
		t.Fatalf("contact fields not roundtripped: %+v", got)
	}
}

func TestValidNotifChannel(t *testing.T) {
	for _, c := range []string{
		domain.NotifChannelOff, domain.NotifChannelEmail, domain.NotifChannelLog,
		domain.NotifChannelWhatsApp, domain.NotifChannelTelegram,
	} {
		if !domain.ValidNotifChannel(c) {
			t.Fatalf("channel %q should be valid", c)
		}
	}
	if domain.ValidNotifChannel("sms") {
		t.Fatal("sms unexpectedly valid")
	}
}

func TestRoleDashboards(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")

	if err := st.CreateReport(roomID, menteeID, "learned", "practiced", "blocked", "next", ""); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if err := st.CreateTask(roomID, menteeID, mentorID, "Overdue task", "detail", yesterday); err != nil {
		t.Fatal(err)
	}

	mentorDash, err := st.MentorDashboard(mentorID)
	if err != nil {
		t.Fatal(err)
	}
	if mentorDash.Summary.ActiveMentees != 1 || mentorDash.Summary.Blockers != 1 || mentorDash.Summary.OverdueTasks != 1 {
		t.Fatalf("bad mentor summary: %+v", mentorDash.Summary)
	}
	if len(mentorDash.AttentionItems) == 0 {
		t.Fatal("expected attention items")
	}
	if len(mentorDash.Mentees) != 1 || mentorDash.Mentees[0].Status != "overdue" {
		t.Fatalf("bad mentee progress: %+v", mentorDash.Mentees)
	}

	menteeDash, err := st.MenteeDashboard(menteeID)
	if err != nil {
		t.Fatal(err)
	}
	if menteeDash.Summary.OpenTasks != 1 || menteeDash.Summary.OverdueTasks != 1 || menteeDash.Summary.Blockers != 1 {
		t.Fatalf("bad mentee summary: %+v", menteeDash.Summary)
	}
}
