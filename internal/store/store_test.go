package store

import (
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
	uid, err := st.CreateInitialRoom(name, email, hash, "Backend")
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
	return uid, rooms[0].ID
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

func joinLearner(t *testing.T, st *Store, code, name, email string) (string, string) {
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
	if _, err := st.CreateInitialRoom("Mentor", "mentor@example.com", hash, "Backend"); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateInitialRoom("Other", "other@example.com", hash, "Frontend")
	if err != ErrSetupComplete {
		t.Fatalf("expected ErrSetupComplete, got %v", err)
	}
	if got := st.UserCount(); got != 1 {
		t.Fatalf("expected single user after blocked second setup, got %d", got)
	}
}

func TestInviteCanOnlyBeUsedOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleLearner)

	learnerID, joinedRoomID := joinLearner(t, st, code, "Learner", "learner@example.com")
	if learnerID == "" || joinedRoomID != roomID {
		t.Fatal("learner did not join expected room")
	}

	hash, err := auth.HashPassword("verysecurepass123")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.JoinWithInvite(code, "Other", "other@example.com", hash); err == nil {
		t.Fatal("used invite accepted twice")
	}
}

func TestLearnerOnlySeesOwnReports(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerA, _ := joinLearner(t, st, codeA, "Learner A", "a@example.com")
	learnerB, _ := joinLearner(t, st, codeB, "Learner B", "b@example.com")

	if err := st.CreateReport(roomID, learnerA, "A learned", "A practiced", "", "A next", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateReport(roomID, learnerB, "B learned", "B practiced", "", "B next", ""); err != nil {
		t.Fatal(err)
	}

	mentorReports, err := st.Reports(roomID, mentorID, domain.RoleMentor)
	if err != nil {
		t.Fatal(err)
	}
	if len(mentorReports) != 2 {
		t.Fatalf("mentor expected 2 reports, got %d", len(mentorReports))
	}
	learnerReports, err := st.Reports(roomID, learnerA, domain.RoleLearner)
	if err != nil {
		t.Fatal(err)
	}
	if len(learnerReports) != 1 || learnerReports[0].UserID != learnerA {
		t.Fatalf("learner report visibility broken: %+v", learnerReports)
	}
}

func TestMentorCanCreateMultipleRooms(t *testing.T) {
	st := newTestStore(t)
	mentorID, firstRoomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	secondRoomID, err := st.CreateRoom("Frontend", mentorID)
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
	if !st.IsMentor(mentorID) {
		t.Fatal("mentor should be able to create rooms")
	}
}

func TestCreateTaskForLearnersAssignsEveryLearnerOnce(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerA, _ := joinLearner(t, st, codeA, "Learner A", "a@example.com")
	learnerB, _ := joinLearner(t, st, codeB, "Learner B", "b@example.com")

	count, err := st.CreateTaskForLearners(roomID, mentorID, "Read RFC", "details", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 tasks created, got %d", count)
	}

	tasksA, err := st.Tasks(roomID, learnerA, domain.RoleLearner)
	if err != nil {
		t.Fatal(err)
	}
	tasksB, err := st.Tasks(roomID, learnerB, domain.RoleLearner)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksA) != 1 || len(tasksB) != 1 {
		t.Fatalf("expected exactly one task per learner, got A=%d B=%d", len(tasksA), len(tasksB))
	}
	if tasksA[0].ID == tasksB[0].ID {
		t.Fatal("bulk assign produced shared task ID across learners")
	}

	// Mentor is not a learner: bulk should never assign to mentor.
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

func TestCreateTaskForLearnersIsZeroWhenNoLearners(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	count, err := st.CreateTaskForLearners(roomID, mentorID, "Read RFC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 tasks in mentor-only room, got %d", count)
	}
}

func TestIsLearnerRejectsMentorsAndOutsiders(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	if st.IsLearner(roomID, mentorID) {
		t.Fatal("mentor incorrectly classified as learner")
	}
	if st.IsLearner(roomID, "non-existent-user") {
		t.Fatal("non-member classified as learner")
	}
	code := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerID, _ := joinLearner(t, st, code, "Learner", "learner@example.com")
	if !st.IsLearner(roomID, learnerID) {
		t.Fatal("learner not recognised by IsLearner")
	}
}

func TestTaskUpdateAuthorization(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerA, _ := joinLearner(t, st, codeA, "Learner A", "a@example.com")
	learnerB, _ := joinLearner(t, st, codeB, "Learner B", "b@example.com")

	if err := st.CreateTask(roomID, learnerA, mentorID, "Read", "Read docs", ""); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.Tasks(roomID, learnerA, domain.RoleLearner)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}

	updated, err := st.UpdateTaskStatus(roomID, tasks[0].ID, learnerB, domain.RoleLearner, "done")
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("learner updated another learner's task")
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
	code := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerID, _ := joinLearner(t, st, code, "Learner", "learner@example.com")

	for i := 0; i < 3; i++ {
		if err := st.CreateReport(roomID, learnerID, "learned", "practiced", "", "next", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateTask(roomID, learnerID, mentorID, "One task", "detail", ""); err != nil {
		t.Fatal(err)
	}
	members, err := st.Members(roomID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.UserID == learnerID && m.OpenTasks != 1 {
			t.Fatalf("expected one open task, got %d", m.OpenTasks)
		}
	}
}

func TestTaskDueDateAndReminders(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	code := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerID, _ := joinLearner(t, st, code, "Learner", "learner@example.com")

	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	dueSoon := now.Add(24 * time.Hour).Format("2006-01-02")
	if err := st.CreateTask(roomID, learnerID, mentorID, "Due soon", "detail", dueSoon); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.Tasks(roomID, learnerID, domain.RoleLearner)
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
	code := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerID, _ := joinLearner(t, st, code, "Learner", "learner@example.com")

	if err := st.CreateTask(roomID, learnerID, mentorID, "Read RFC", "details", ""); err != nil {
		t.Fatal(err)
	}
	tasks, _ := st.Tasks(roomID, learnerID, domain.RoleLearner)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	taskID := tasks[0].ID

	// Cannot review a task that is not yet done.
	if ok, err := st.ReviewTask(roomID, taskID, mentorID, 4); err != nil || ok {
		t.Fatalf("review pre-done should not award (ok=%v err=%v)", ok, err)
	}

	// Mark done, then review.
	if _, err := st.UpdateTaskStatus(roomID, taskID, learnerID, domain.RoleLearner, "done"); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ReviewTask(roomID, taskID, mentorID, 4)
	if err != nil || !ok {
		t.Fatalf("first review should succeed (ok=%v err=%v)", ok, err)
	}
	if got := st.UserPointsTotal(learnerID); got != 4 {
		t.Fatalf("total points want 4, got %d", got)
	}

	// Second review must no-op (idempotent).
	ok, err = st.ReviewTask(roomID, taskID, mentorID, 5)
	if err != nil || ok {
		t.Fatalf("re-review should not award (ok=%v err=%v)", ok, err)
	}
	if got := st.UserPointsTotal(learnerID); got != 4 {
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
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	codeC := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerA, _ := joinLearner(t, st, codeA, "A", "a@example.com")
	learnerB, _ := joinLearner(t, st, codeB, "B", "b@example.com")
	learnerC, _ := joinLearner(t, st, codeC, "C", "c@example.com")

	for who, pts := range map[string]int{learnerA: 5, learnerB: 3, learnerC: 5} {
		if err := st.CreateTask(roomID, who, mentorID, "t", "", ""); err != nil {
			t.Fatal(err)
		}
		tasks, _ := st.Tasks(roomID, who, domain.RoleLearner)
		if _, err := st.UpdateTaskStatus(roomID, tasks[0].ID, who, domain.RoleLearner, "done"); err != nil {
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
	rank, err := st.UserRankInRoom(learnerB, roomID)
	if err != nil {
		t.Fatal(err)
	}
	if rank.Position != 2 || rank.Total != 3 {
		t.Fatalf("learner B rank want 2/3, got %+v", rank)
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
	code := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerID, _ := joinLearner(t, st, code, "Learner", "learner@example.com")

	if err := st.CreateReport(roomID, learnerID, "learned", "practiced", "blocked", "next", ""); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if err := st.CreateTask(roomID, learnerID, mentorID, "Overdue task", "detail", yesterday); err != nil {
		t.Fatal(err)
	}

	mentorDash, err := st.MentorDashboard(mentorID)
	if err != nil {
		t.Fatal(err)
	}
	if mentorDash.Summary.ActiveLearners != 1 || mentorDash.Summary.Blockers != 1 || mentorDash.Summary.OverdueTasks != 1 {
		t.Fatalf("bad mentor summary: %+v", mentorDash.Summary)
	}
	if len(mentorDash.AttentionItems) == 0 {
		t.Fatal("expected attention items")
	}
	if len(mentorDash.Learners) != 1 || mentorDash.Learners[0].Status != "overdue" {
		t.Fatalf("bad learner progress: %+v", mentorDash.Learners)
	}

	learnerDash, err := st.LearnerDashboard(learnerID)
	if err != nil {
		t.Fatal(err)
	}
	if learnerDash.Summary.OpenTasks != 1 || learnerDash.Summary.OverdueTasks != 1 || learnerDash.Summary.Blockers != 1 {
		t.Fatalf("bad learner summary: %+v", learnerDash.Summary)
	}
}
