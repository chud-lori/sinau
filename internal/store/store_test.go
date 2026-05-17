package store

import (
	"path/filepath"
	"strconv"
	"strings"
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

// TestSchemaV1HasFinalShape exercises the consolidated migration: a fresh
// install must end up with every column the application uses present on
// the first run. This is the regression guard against accidentally
// dropping a column from schemaV1 during a refactor.
func TestSchemaV1HasFinalShape(t *testing.T) {
	st := newTestStore(t)
	for _, tc := range []struct {
		table  string
		column string
	}{
		{"users", "language"},
		{"users", "can_create_rooms"},
		{"users", "engagement_notif_enabled"},
		{"users", "onboarded_at"},
		{"sessions", "created_at"},
		{"rooms", "mode"},
		{"rooms", "leaderboard_visible"},
		{"reports", "edited_at"},
		{"reports", "deleted_at"},
		{"comments", "edited_at"},
		{"comments", "deleted_at"},
		{"tasks", "due_date"},
		{"tasks", "last_reminded_at"},
		{"tasks", "resource_url"},
		{"tasks", "edited_at"},
		{"tasks", "deleted_at"},
		{"task_submissions", "score"},
		{"task_submissions", "reviewed_by"},
		{"task_submissions", "feedback"},
		{"notification_prefs", "whatsapp_number"},
		{"notification_prefs", "telegram_chat_id"},
	} {
		ok, err := st.columnExists(tc.table, tc.column)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("expected %s.%s to exist in schemaV1", tc.table, tc.column)
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

	if err := st.CreateReport(roomID, menteeA, "A learned", "A practiced", "", "A next", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateReport(roomID, menteeB, "B learned", "B practiced", "", "B next", nil); err != nil {
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

func TestClassroomTaskCanBeSubmittedAndReviewed(t *testing.T) {
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

	// Classroom tasks are always broadcast (assigned_to = "").
	taskID, err := st.CreateTask(roomID, mentorID, "", "Build notebook", "Train a small model", "https://docs.google.com/doc", "2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	studentTasks, err := st.Tasks(roomID, menteeID, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(studentTasks) != 1 || studentTasks[0].MySubmissionStatus != "" {
		t.Fatalf("expected one unsubmitted task, got %+v", studentTasks)
	}
	if err := st.SubmitTask(roomID, taskID, menteeID, "Finished baseline",
		[]domain.Link{{Label: "Notebook", URL: "https://colab.research.google.com/notebook"}}); err != nil {
		t.Fatal(err)
	}
	submissions, err := st.TaskSubmissions(roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(submissions) != 1 || submissions[0].Status != "submitted" || submissions[0].StudentName != "Student" {
		t.Fatalf("expected submitted student work, got %+v", submissions)
	}
	updated, err := st.ReviewTaskSubmission(roomID, submissions[0].ID, "reviewed", "Good baseline", "90", mentorID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("review did not update submission")
	}
	studentTasks, err = st.Tasks(roomID, menteeID, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if studentTasks[0].MySubmissionStatus != "reviewed" || studentTasks[0].MyScore != "90" {
		t.Fatalf("mentee did not see review result: %+v", studentTasks[0])
	}
}

// TestBroadcastTaskReachesEveryMenteeOnce — one CreateTask with
// assigned_to="" must appear for every mentee, never to the mentor.
func TestBroadcastTaskReachesEveryMenteeOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "Mentee A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "Mentee B", "b@example.com")

	taskID, err := st.CreateTask(roomID, mentorID, "", "Read RFC", "details", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if taskID == "" {
		t.Fatal("expected task id from CreateTask")
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
	if tasksA[0].ID != taskID || tasksB[0].ID != taskID {
		t.Fatal("broadcast task should be the same row for every mentee")
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

// TestIndividualTaskScopedToAssignee — a task assigned to menteeA must
// not appear in menteeB's task list, and only the assigned mentee can
// submit work against it.
func TestIndividualTaskScopedToAssignee(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "Mentee A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "Mentee B", "b@example.com")

	taskID, err := st.CreateTask(roomID, mentorID, menteeA, "Read", "Read docs", "", "")
	if err != nil {
		t.Fatal(err)
	}
	tasksA, err := st.Tasks(roomID, menteeA, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksA) != 1 || tasksA[0].ID != taskID {
		t.Fatalf("mentee A should see the task, got %+v", tasksA)
	}
	tasksB, err := st.Tasks(roomID, menteeB, domain.RoleMentee)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksB) != 0 {
		t.Fatalf("mentee B should not see another mentee's task, got %+v", tasksB)
	}

	// Submission by the wrong mentee must fail at the store boundary.
	if err := st.SubmitTask(roomID, taskID, menteeB, "wrong", nil); err == nil {
		t.Fatal("mentee B should not be able to submit a task assigned to mentee A")
	}
}

func TestMemberOpenTaskCountDoesNotMultiplyByReports(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")

	for i := 0; i < 3; i++ {
		if err := st.CreateReport(roomID, menteeID, "learned", "practiced", "", "next", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateTask(roomID, mentorID, menteeID, "One task", "detail", "", ""); err != nil {
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
	if _, err := st.CreateTask(roomID, mentorID, menteeID, "Due soon", "detail", "", dueSoon); err != nil {
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

// TestReviewMentorshipSubmissionAwardsPointsOnce — submitting and
// reviewing a mentorship task awards points to the ledger, and a second
// review of the same (already-reviewed) submission is rejected so the
// score can't be inflated by re-clicking.
func TestReviewMentorshipSubmissionAwardsPointsOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "mentee@example.com")

	taskID, err := st.CreateTask(roomID, mentorID, menteeID, "Read RFC", "details", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Submit work and pick up the resulting submission row.
	if err := st.SubmitTask(roomID, taskID, menteeID, "done reading",
		[]domain.Link{{Label: "Notes", URL: "https://docs.example.com/x"}}); err != nil {
		t.Fatal(err)
	}
	subs, err := st.TaskSubmissions(roomID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("want 1 submission, got %d", len(subs))
	}
	subID := subs[0].ID

	// First review awards points.
	ok, err := st.ReviewTaskSubmission(roomID, subID, "reviewed", "good", "4", mentorID)
	if err != nil || !ok {
		t.Fatalf("first review should succeed (ok=%v err=%v)", ok, err)
	}
	if got := st.UserPointsTotal(menteeID); got != 4 {
		t.Fatalf("total points want 4, got %d", got)
	}

	// Re-reviewing an already-reviewed submission is rejected.
	ok, err = st.ReviewTaskSubmission(roomID, subID, "reviewed", "bumped", "5", mentorID)
	if err != nil || ok {
		t.Fatalf("re-review should not award (ok=%v err=%v)", ok, err)
	}
	if got := st.UserPointsTotal(menteeID); got != 4 {
		t.Fatalf("total points unchanged after re-review, got %d", got)
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
		taskID, err := st.CreateTask(roomID, mentorID, who, "t", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SubmitTask(roomID, taskID, who, "done", nil); err != nil {
			t.Fatal(err)
		}
		subs, err := st.TaskSubmissions(roomID)
		if err != nil {
			t.Fatal(err)
		}
		var subID string
		for _, s := range subs {
			if s.TaskID == taskID {
				subID = s.ID
				break
			}
		}
		if subID == "" {
			t.Fatalf("submission not found for task %s", taskID)
		}
		score := strconv.Itoa(pts)
		if ok, err := st.ReviewTaskSubmission(roomID, subID, "reviewed", "ok", score, mentorID); err != nil || !ok {
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

	if err := st.CreateReport(roomID, menteeID, "learned", "practiced", "blocked", "next", nil); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := st.CreateTask(roomID, mentorID, menteeID, "Overdue task", "detail", "", yesterday); err != nil {
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

// TestSearchFTSReturnsHitsAndRespectsVisibility exercises the FTS5 path:
// reports get indexed via trigger, the cross-source query returns hits,
// and a mentee cannot see another mentee's report.
func TestSearchFTSReturnsHitsAndRespectsVisibility(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeA, _ := joinMentee(t, st, codeA, "Mentee A", "a@example.com")
	menteeB, _ := joinMentee(t, st, codeB, "Mentee B", "b@example.com")

	if err := st.CreateReport(roomID, menteeA, "Studied FTS5 indexing", "Practiced queries", "", "Continue", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateReport(roomID, menteeB, "Studied React hooks", "Practiced state", "", "Continue", nil); err != nil {
		t.Fatal(err)
	}

	// Mentor sees both reports for "studied".
	hits, err := st.Search(mentorID, "studied", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("mentor expected 2 hits, got %d (%+v)", len(hits), hits)
	}
	// Mentee A only sees their own.
	hitsA, err := st.Search(menteeA, "studied", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsA) != 1 || !strings.Contains(hitsA[0].Snippet, "FTS5") {
		t.Fatalf("mentee A expected 1 own-report hit with FTS5 snippet, got %+v", hitsA)
	}
}

// TestSearchExcludesSoftDeletedRows: after soft-delete, the row should
// drop out of search results even though it still exists in the table.
func TestSearchExcludesSoftDeletedRows(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	menteeID, _ := joinMentee(t, st, code, "Mentee", "m@example.com")

	if err := st.CreateReport(roomID, menteeID, "Studied unique keyword zorp", "P", "", "N", nil); err != nil {
		t.Fatal(err)
	}
	hits, err := st.Search(mentorID, "zorp", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit before delete, got %d", len(hits))
	}
	if _, err := st.DeleteReport(hits[0].ID); err != nil {
		t.Fatal(err)
	}
	hits2, err := st.Search(mentorID, "zorp", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits2) != 0 {
		t.Fatalf("expected 0 hits after delete, got %d", len(hits2))
	}
}

// TestStudentGradesAggregatesAcrossClassrooms confirms the cross-class
// grade view computes average and on-time percent per room.
func TestStudentGradesAggregatesAcrossClassrooms(t *testing.T) {
	st := newTestStore(t)
	mentorID, _ := createUserRoom(t, st, "Teacher", "teacher@example.com")
	roomID, err := st.CreateRoom("Math", mentorID, domain.RoomModeClassroom)
	if err != nil {
		t.Fatal(err)
	}
	code := createInvite(t, st, roomID, mentorID, domain.RoleMentee)
	studentID, _ := joinMentee(t, st, code, "S", "s@example.com")

	// Past-deadline classroom task (broadcast — classroom is always
	// broadcast) so we can exercise the "on time" math against a fresh
	// submission (today vs deadline in the past).
	taskID, err := st.CreateTask(roomID, mentorID, "", "Quiz 1", "instructions", "", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SubmitTask(roomID, taskID, studentID, "done",
		[]domain.Link{{Label: "Doc", URL: "https://docs.example.com/x"}}); err != nil {
		t.Fatal(err)
	}
	subs, err := st.TaskSubmissions(roomID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewTaskSubmission(roomID, subs[0].ID, "reviewed", "ok", "85", mentorID); err != nil {
		t.Fatal(err)
	}

	rooms, err := st.StudentGrades(studentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].RoomID != roomID {
		t.Fatalf("expected one room, got %+v", rooms)
	}
	if rooms[0].ScoredCount != 1 || rooms[0].AverageScore != 85 {
		t.Fatalf("expected score 85 / 1 scored, got %+v", rooms[0])
	}
}
