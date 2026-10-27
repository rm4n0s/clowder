package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/auth"
	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
)

// testScale is a large demo of a few hundred posts, made in a second.
var testScale = demoScale{
	users: 30, rooms: 9, privateEvery: 3, posts: 400, commentShare: 0.30, tasks: 120,
	directChats: 2, groupChats: 1, chatMessages: 6,
}

// makeLargeForTest makes the large demo of the scale in a new directory with
// the fast hasher and returns the directory, its output and the opened store.
func makeLargeForTest(t *testing.T, sc demoScale) (string, string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	var stdout bytes.Buffer
	if err := fillDemo(t.Context(), dir, "", &sc, &stdout, io.Discard, auth.FastHasher()); err != nil {
		t.Fatalf("fillDemo: %+v", err)
	}
	_, st := openAccounts(t, dir)
	return dir, stdout.String(), st
}

func TestLargeDemoIsConsistent(t *testing.T) {
	t.Parallel()
	dir, out, st := makeLargeForTest(t, testScale)
	s, err := st.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats: %+v", err)
	}
	sc := testScale

	t.Run("the sizes of the scale", func(t *testing.T) {
		if s.Users != int64(sc.users) || s.Rooms != int64(sc.rooms) {
			t.Errorf("users %d, rooms %d; want %d, %d", s.Users, s.Rooms, sc.users, sc.rooms)
		}
		if s.Posts+s.Comments != int64(sc.posts) {
			t.Errorf("posts and comments = %d, want %d", s.Posts+s.Comments, sc.posts)
		}
		if share := float64(s.Comments) / float64(sc.posts); share < 0.2 || share > 0.4 {
			t.Errorf("%d of %d are comments (%.2f), want about 30%%", s.Comments, sc.posts, share)
		}
		if s.Tasks != int64(sc.tasks) {
			t.Errorf("tasks = %d, want %d", s.Tasks, sc.tasks)
		}
		if s.PrivateRooms == 0 || s.PrivateRooms == s.Rooms {
			t.Errorf("private rooms = %d of %d, want some", s.PrivateRooms, s.Rooms)
		}
		if s.Columns != 3*s.Rooms {
			t.Errorf("board columns = %d, want three per room (%d)", s.Columns, 3*s.Rooms)
		}
		if want := int64(sc.directChats + sc.groupChats + 2); s.Conversations != want {
			t.Errorf("conversations = %d, want %d (the small demo has 2)", s.Conversations, want)
		}
		if s.Tags == 0 || s.PostTags == 0 || s.Reactions == 0 || s.Mentions == 0 || s.Assignees == 0 {
			t.Errorf("a kind of row is missing: %+v", s)
		}
	})

	t.Run("the full-text index matches the posts", func(t *testing.T) {
		// The small demo deletes one comment that has replies; it stays a
		// placeholder and is not indexed.
		if want := s.Posts + s.Comments - 1; s.Indexed != want {
			t.Errorf("index rows = %d, want %d", s.Indexed, want)
		}
		if s.Unindexed != 0 || s.StrayIndexRows != 0 {
			t.Errorf("unindexed %d, stray %d", s.Unindexed, s.StrayIndexRows)
		}
	})

	t.Run("every rule of the services holds", func(t *testing.T) {
		if s.BadComments != 0 || s.NonMemberAuthors != 0 || s.BadColumns != 0 ||
			s.TasksInForeignColumn != 0 || s.BadTaskPositions != 0 {
			t.Errorf("broken rules: %+v", s)
		}
	})

	t.Run("every post and task has its audit event", func(t *testing.T) {
		if s.PostEvents != s.Posts+s.Comments {
			t.Errorf("post.created events = %d, want %d", s.PostEvents, s.Posts+s.Comments)
		}
		if s.TaskEvents != s.Tasks {
			t.Errorf("task.created events = %d, want %d", s.TaskEvents, s.Tasks)
		}
	})

	t.Run("the output has the counts", func(t *testing.T) {
		for _, want := range []string{
			"large demo users: 30", "large demo posts and comments: 400 (", "large demo tasks: 120 (",
			"large demo rooms: 9 (", "large demo conversations: 5 with", "search index rows: ", "large demo made in ",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		// The small demo's output is still there, first.
		if !strings.Contains(out, "demo messages: 6 in 2 conversations") {
			t.Errorf("the small demo's output is missing:\n%s", out)
		}
	})

	t.Run("the users can log in with the demo password", func(t *testing.T) {
		users, err := st.ListUsers(t.Context())
		if err != nil {
			t.Fatalf("ListUsers: %+v", err)
		}
		last := users[len(users)-1]
		if ok, err := auth.FastHasher().Verify(demoPassword, last.PasswordHash); err != nil || !ok {
			t.Errorf("%s: the password does not verify: %v, %v", last.Username, ok, err)
		}
		if last.Role != store.RoleMember || last.DisabledAt != nil {
			t.Errorf("%s = %+v", last.Username, last)
		}
	})

	t.Run("the attachments have their files", func(t *testing.T) {
		entries, err := os.ReadDir(filepath.Join(dir, filesDir))
		if err != nil {
			t.Fatal(err)
		}
		// The small demo has two attachments; tmp is a directory of its own.
		if s.Attachments > 2 && len(entries) < int(s.Attachments) {
			t.Errorf("%d attachments but %d entries in the files directory", s.Attachments, len(entries))
		}
	})
}

func TestLargeDemoIsTheSameEveryTime(t *testing.T) {
	t.Parallel()
	_, _, a := makeLargeForTest(t, testScale)
	_, _, b := makeLargeForTest(t, testScale)
	sa, errA := a.Stats(t.Context())
	sb, errB := b.Stats(t.Context())
	if errA != nil || errB != nil {
		t.Fatalf("Stats: %v, %v", errA, errB)
	}
	// Memberships depend on the random source only, not on the clock.
	if sa != sb {
		t.Errorf("two runs differ:\n%+v\n%+v", sa, sb)
	}
}

func TestDemoSizeFlag(t *testing.T) {
	t.Run("rejects an unknown size", func(t *testing.T) {
		for _, size := range []string{"huge", "", "Large", "100"} {
			dir := filepath.Join(t.TempDir(), "demo")
			err := run(t.Context(), []string{"demo", "-data", dir, "-size", size}, mapEnv(nil), io.Discard, io.Discard, auth.FastHasher())
			appErr, ok := errors.FromError(err)
			if !ok || !appErr.HasRoute("run->runDemo->demoScaleOf.DemoSizeInvalid") {
				t.Errorf("size %q: err = %v, want run->runDemo->demoScaleOf.DemoSizeInvalid", size, err)
			}
			if _, statErr := os.Stat(dir); statErr == nil {
				t.Errorf("size %q: the data directory was made", size)
			}
		}
	})

	t.Run("small is the default demo", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "demo")
		var stdout bytes.Buffer
		if err := run(t.Context(), []string{"demo", "-data", dir, "-size", "small"}, mapEnv(nil), &stdout, io.Discard, auth.FastHasher()); err != nil {
			t.Fatalf("demo: %+v", err)
		}
		if strings.Contains(stdout.String(), "large demo") {
			t.Errorf("the small demo printed large demo lines:\n%s", stdout.String())
		}
		_, st := openAccounts(t, dir)
		s, err := st.Stats(t.Context())
		if err != nil || s.Users != 4 || s.Rooms != 3 || s.Posts != 8 || s.Comments != 9 {
			t.Errorf("Stats = %+v, %v; want the 4 users, 3 rooms, 8 posts and 9 comments of the small demo", s, err)
		}
	})

	t.Run("large is the scale of SPEC §10", func(t *testing.T) {
		sc, err := demoScaleOf("large")
		if err != nil || sc == nil {
			t.Fatalf("demoScaleOf(large) = %v, %v", sc, err)
		}
		if sc.users != 200 || sc.rooms != 100 || sc.posts != 100_000 || sc.tasks != 20_000 || sc.commentShare != 0.30 {
			t.Errorf("large scale = %+v", *sc)
		}
		if small, err := demoScaleOf("small"); err != nil || small != nil {
			t.Errorf("demoScaleOf(small) = %v, %v; want nil", small, err)
		}
	})
}

func TestLargeDemoFailures(t *testing.T) {
	t.Parallel()
	_, _, st := makeLargeForTest(t, testScale)
	t.Run("a scale smaller than the small demo", func(t *testing.T) {
		t.Parallel()
		sc := testScale
		sc.posts = 5
		err := fillDemo(t.Context(), t.TempDir(), "", &sc, io.Discard, io.Discard, auth.FastHasher())
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute("fillDemo->makeLargeDemo->largeGen.makeContent.DemoScaleInvalid") {
			t.Errorf("err = %v, want ...makeContent.DemoScaleInvalid", err)
		}
	})

	t.Run("a database that breaks a rule", func(t *testing.T) {
		rooms, err := st.ListRooms(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// Taking a room's posts out of the index leaves them unindexed.
		if err := st.UnindexRoom(t.Context(), rooms[0].ID); err != nil {
			t.Fatal(err)
		}
		g := &largeGen{ctx: t.Context(), d: largeDeps{st: st, clk: clock.Real()}, stdout: io.Discard}
		err = g.report(0)
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute("largeGen.report.DemoInconsistent") {
			t.Fatalf("err = %v, want largeGen.report.DemoInconsistent", err)
		}
		if problems, _ := appErr.Metadata["problems"].([]string); len(problems) == 0 || !strings.HasPrefix(problems[0], "unindexed posts ") {
			t.Errorf("problems = %v", appErr.Metadata["problems"])
		}
	})

	t.Run("a store that fails", func(t *testing.T) {
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		g := &largeGen{ctx: context.Background(), d: largeDeps{st: st, clk: clock.Real()}, stdout: io.Discard}
		err := g.report(0)
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute("largeGen.report->Store.Stats.QueryFailed") {
			t.Errorf("err = %v, want a Stats.QueryFailed", err)
		}
	})

	t.Run("details that cannot be made into JSON", func(t *testing.T) {
		_, err := marshalDetails(map[string]any{"bad": make(chan int)})
		appErr, ok := errors.FromError(err)
		if !ok || !appErr.HasRoute("marshalDetails.DemoDetailsFailed") {
			t.Errorf("err = %v, want marshalDetails.DemoDetailsFailed", err)
		}
	})
}
