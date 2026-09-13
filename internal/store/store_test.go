package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrickspencer/algo-lab-public/internal/catalog"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	st, err := s.Stats(ctx)
	if err != nil || st.Problems != 0 || !st.LastSync.IsZero() {
		t.Fatalf("empty stats: %+v, %v", st, err)
	}

	in := []catalog.Problem{
		{FrontendID: "2", Title: "Add Two Numbers", TitleSlug: "add-two-numbers", Difficulty: "Medium", AcRate: 49.3,
			TopicTags: []catalog.TopicTag{{Name: "Linked List"}, {Name: "Math"}}},
		{FrontendID: "1", Title: "Two Sum", TitleSlug: "two-sum", Difficulty: "Easy", AcRate: 58.1, PaidOnly: true, Status: "ac"},
	}
	if err := s.SaveProblems(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Problems(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].TitleSlug != "add-two-numbers" || out[1].PaidOnly != true || out[1].Status != "ac" {
		t.Fatalf("problems round trip: %+v", out)
	}
	if err := s.SetProblemStatus(ctx, "add-two-numbers", "notac"); err != nil {
		t.Fatal(err)
	}
	if out, _ = s.Problems(ctx); out[0].Status != "notac" {
		t.Fatalf("status update: %+v", out[0])
	}
	if len(out[0].TopicTags) != 2 || out[0].TopicTags[1].Name != "Math" {
		t.Fatalf("tags round trip: %+v", out[0].TopicTags)
	}

	// Saving again replaces rather than duplicates.
	if err := s.SaveProblems(ctx, in[:1]); err != nil {
		t.Fatal(err)
	}
	if out, _ = s.Problems(ctx); len(out) != 1 {
		t.Fatalf("expected 1 problem after re-save, got %d", len(out))
	}

	if _, _, ok, err := s.Detail(ctx, "two-sum"); ok || err != nil {
		t.Fatalf("unexpected cached detail: ok=%v err=%v", ok, err)
	}
	d := &catalog.Detail{TitleSlug: "two-sum", Title: "Two Sum", Content: "<p>hi</p>", Hints: []string{"h"}}
	if err := s.SaveDetail(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, at, ok, err := s.Detail(ctx, "two-sum")
	if err != nil || !ok || got.Content != "<p>hi</p>" || at.IsZero() {
		t.Fatalf("detail round trip: %+v ok=%v at=%v err=%v", got, ok, at, err)
	}

	st, _ = s.Stats(ctx)
	if st.Problems != 1 || st.Details != 1 || st.LastSync.IsZero() {
		t.Fatalf("stats: %+v", st)
	}
}

func TestOpenRejectsForeignSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE problems (slug TEXT PRIMARY KEY, id TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "not created by algo-lab") {
		t.Fatalf("expected foreign-schema error, got %v", err)
	}
}

func TestAttemptsAndDrafts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, ok, _ := s.Draft(ctx, 0, "two-sum", "python3"); ok {
		t.Fatal("unexpected draft")
	}
	if err := s.SaveDraft(ctx, 0, "two-sum", "python3", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveDraft(ctx, 0, "two-sum", "python3", "v2"); err != nil {
		t.Fatal(err)
	}
	if code, ok, _ := s.Draft(ctx, 0, "two-sum", "python3"); !ok || code != "v2" {
		t.Fatalf("draft = %q ok=%v", code, ok)
	}

	id1, err := s.SaveAttempt(ctx, 0, "two-sum", "python3", "a1")
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.SaveAttempt(ctx, 0, "two-sum", "golang", "a2")
	as, err := s.Attempts(ctx, 0, "two-sum")
	if err != nil || len(as) != 2 || as[0].ID != id2 || as[1].ID != id1 || as[0].CreatedAt.IsZero() {
		t.Fatalf("attempts: %+v err=%v", as, err)
	}
	counts, _ := s.AttemptCounts(ctx, 0)
	if counts["two-sum"] != 2 {
		t.Fatalf("counts: %v", counts)
	}
	if err := s.DeleteAttempt(ctx, 0, id1); err != nil {
		t.Fatal(err)
	}
	if as, _ = s.Attempts(ctx, 0, "two-sum"); len(as) != 1 || as[0].Code != "a2" {
		t.Fatalf("after delete: %+v", as)
	}

	if v, _ := s.Meta(ctx, "lang"); v != "" {
		t.Fatalf("meta = %q", v)
	}
	if err := s.SetMeta(ctx, "lang", "golang"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Meta(ctx, "lang"); v != "golang" {
		t.Fatalf("meta = %q", v)
	}
}

func TestHintsAndReviews(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if hs, _ := s.Hints(ctx, "two-sum"); len(hs) != 0 {
		t.Fatalf("unexpected hints: %v", hs)
	}
	if err := s.SaveHint(ctx, "two-sum", 1, "claude", "think about a map"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveHint(ctx, "two-sum", 1, "codex", "updated"); err != nil {
		t.Fatal(err)
	}
	hs, _ := s.Hints(ctx, "two-sum")
	if len(hs) != 1 || hs[1].Text != "updated" || hs[1].Provider != "codex" || hs[1].CreatedAt.IsZero() {
		t.Fatalf("hints: %+v", hs)
	}

	if _, ok, _ := s.LatestReview(ctx, "two-sum"); ok {
		t.Fatal("unexpected review")
	}
	if _, err := s.SaveReview(ctx, "two-sum", "python3", "code1", "claude", "{}"); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.SaveReview(ctx, "two-sum", "python3", "code2", "claude", "{\"summary\":\"ok\"}")
	r, ok, err := s.LatestReview(ctx, "two-sum")
	if err != nil || !ok || r.ID != id2 || r.Code != "code2" || r.CreatedAt.IsZero() {
		t.Fatalf("latest review: %+v ok=%v err=%v", r, ok, err)
	}
}

func TestScratches(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id1, err := s.SaveScratch(ctx, 0, 0, "python3", "print(1)")
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.SaveScratch(ctx, 0, 0, "golang", "package main")
	if _, err := s.SaveScratch(ctx, 0, id1, "python3", "print(2)"); err != nil {
		t.Fatal(err)
	}
	list, err := s.Scratches(ctx, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("scratches: %+v err=%v", list, err)
	}
	// id1 was updated last, so it sorts first.
	if list[0].ID != id1 || list[0].Code != "print(2)" || list[1].ID != id2 {
		t.Fatalf("order/content: %+v", list)
	}
	if _, err := s.SaveScratch(ctx, 0, 999, "x", "y"); err == nil {
		t.Fatal("expected error updating missing scratch")
	}
	if err := s.DeleteScratch(ctx, 0, id1); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Scratches(ctx, 0); len(list) != 1 || list[0].ID != id2 {
		t.Fatalf("after delete: %+v", list)
	}
}

func TestUsersAndScoping(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	owner, err := s.CreateUser(ctx, "patrick", "hash1", "tok-1")
	if err != nil || owner.ID != LocalUser {
		t.Fatalf("first user should be the local user: %+v %v", owner, err)
	}
	friend, err := s.CreateUser(ctx, "sam", "", "tok-2")
	if err != nil || friend.ID == LocalUser {
		t.Fatalf("second user: %+v %v", friend, err)
	}
	// Lookup is case-insensitive and returns the hash.
	u, hash, token, ok, _ := s.FindUser(ctx, "Patrick")
	if !ok || u.ID != owner.ID || hash != "hash1" || token != "tok-1" {
		t.Fatalf("find: %+v hash=%q token=%q ok=%v", u, hash, token, ok)
	}
	if _, _, _, ok, _ := s.FindUser(ctx, "nobody"); ok {
		t.Fatal("unknown user found")
	}
	if err := s.UpdateToken(ctx, owner.ID, "tok-1b"); err != nil {
		t.Fatal(err)
	}
	if _, _, token, _, _ := s.FindUser(ctx, "patrick"); token != "tok-1b" {
		t.Fatalf("token not updated: %q", token)
	}
	// SetPassword creates or updates.
	if _, err := s.SetPassword(ctx, "sam", "hash2"); err != nil {
		t.Fatal(err)
	}
	if _, hash, _, _, _ := s.FindUser(ctx, "sam"); hash != "hash2" {
		t.Fatalf("password not set: %q", hash)
	}
	newbie, err := s.SetPassword(ctx, "kim", "hash3")
	if err != nil || newbie.ID == 0 {
		t.Fatalf("SetPassword create: %+v %v", newbie, err)
	}

	// Data is scoped per user.
	_ = s.SaveDraft(ctx, owner.ID, "two-sum", "python3", "mine")
	_ = s.SaveDraft(ctx, friend.ID, "two-sum", "python3", "theirs")
	if code, _, _ := s.Draft(ctx, friend.ID, "two-sum", "python3"); code != "theirs" {
		t.Fatalf("friend draft = %q", code)
	}
	id, _ := s.SaveAttempt(ctx, friend.ID, "two-sum", "python3", "x")
	if as, _ := s.Attempts(ctx, owner.ID, "two-sum"); len(as) != 0 {
		t.Fatalf("owner sees friend's attempts: %+v", as)
	}
	if err := s.DeleteAttempt(ctx, owner.ID, id); err != nil {
		t.Fatal(err)
	}
	if as, _ := s.Attempts(ctx, friend.ID, "two-sum"); len(as) != 1 {
		t.Fatalf("owner deleted friend's attempt")
	}
	if users, _ := s.Users(ctx); len(users) != 3 || users[0].ID != LocalUser {
		t.Fatalf("users: %+v", users)
	}
}

func TestMigrateOldDrafts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before users existed.
	if _, err := db.Exec(`
		CREATE TABLE drafts (title_slug TEXT NOT NULL, lang TEXT NOT NULL, code TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (title_slug, lang));
		INSERT INTO drafts VALUES ('two-sum', 'python3', 'old code', '2026-01-01T00:00:00Z');
		CREATE TABLE attempts (id INTEGER PRIMARY KEY AUTOINCREMENT, title_slug TEXT NOT NULL, lang TEXT NOT NULL, code TEXT NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO attempts (title_slug, lang, code, created_at) VALUES ('two-sum', 'python3', 'a', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if code, ok, err := s.Draft(ctx, LocalUser, "two-sum", "python3"); !ok || code != "old code" || err != nil {
		t.Fatalf("migrated draft: %q ok=%v err=%v", code, ok, err)
	}
	if as, err := s.Attempts(ctx, LocalUser, "two-sum"); err != nil || len(as) != 1 {
		t.Fatalf("migrated attempts: %+v err=%v", as, err)
	}
}
