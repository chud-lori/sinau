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

func TestTaskUpdateAuthorization(t *testing.T) {
	st := newTestStore(t)
	mentorID, roomID := createUserRoom(t, st, "Mentor", "mentor@example.com")
	codeA := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	codeB := createInvite(t, st, roomID, mentorID, domain.RoleLearner)
	learnerA, _ := joinLearner(t, st, codeA, "Learner A", "a@example.com")
	learnerB, _ := joinLearner(t, st, codeB, "Learner B", "b@example.com")

	if err := st.CreateTask(roomID, learnerA, mentorID, "Read", "Read docs"); err != nil {
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
	if err := st.CreateTask(roomID, learnerID, mentorID, "One task", "detail"); err != nil {
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
