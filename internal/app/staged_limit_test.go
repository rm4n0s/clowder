package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/markdown"
	"github.com/rm4n0s/clowder/internal/metrics"
)

// stagedTry makes the user upload size bytes and returns what UploadStaged
// answers.
func (e *postsEnv) stagedTry(user string, size int) (app.StagedUpload, error) {
	e.t.Helper()
	return e.posts.UploadStaged(e.t.Context(), e.as(user), "f.bin", strings.NewReader(strings.Repeat("x", size)))
}

// TestPostsStagedLimit walks the limit on what a person has staged (SPEC D62:
// ten times the attachment limit) at its boundary, and shows what frees the
// bytes: a post that takes the files, the removal of a file, and the sweep.
func TestPostsStagedLimit(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	limit := int64(app.StagedLimitFactor * testMaxUpload)

	set, err := metrics.NewSet(metrics.SetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := newPostsEnv(t)
	e.posts = app.NewPosts(e.st, e.clk, markdown.New(), e.files, app.PostsConfig{
		MaxAttachmentSize: testMaxUpload, MaxAttachments: testMaxAttachments,
		StagedRefused: set.LimitsRefused.With(metrics.LimitStaged),
	}, nil)
	if got := e.posts.StagedLimit(); got != limit {
		t.Fatalf("StagedLimit = %d, want %d", got, limit)
	}

	// Nine files of the attachment limit and one a byte short: 19,999 bytes.
	var first app.StagedUpload
	for i := range 9 {
		s, err := e.stagedTry("bob", testMaxUpload)
		if err != nil {
			t.Fatalf("file %d refused: %+v", i+1, err)
		}
		if i == 0 {
			first = s
		}
	}
	if _, err := e.stagedTry("bob", testMaxUpload-1); err != nil {
		t.Fatalf("the tenth file refused: %+v", err)
	}
	if got, _, err := app.StagedBytes(e.posts, t.Context(), e.users["bob"].ID); err != nil || got != limit-1 {
		t.Fatalf("staged bytes = %d, %v, want %d", got, err, limit-1)
	}

	// One more byte reaches the limit exactly, and is accepted.
	last, err := e.stagedTry("bob", 1)
	if err != nil {
		t.Fatalf("an upload that reaches the limit exactly was refused: %+v", err)
	}
	onDisk := len(e.onDisk())

	// Past it: 413, and nothing stays on the disk.
	_, err = e.stagedTry("bob", 1)
	appErr := requireTag(t, err, app.TagStagedFull, "Posts.UploadStaged.StagedUploadsFull")
	if appErr.Metadata["limit"] != limit || appErr.Metadata["staged"] != limit {
		t.Errorf("metadata = %v, want limit and staged %d", appErr.Metadata, limit)
	}
	if got := len(e.onDisk()); got != onDisk {
		t.Errorf("files on disk = %d, want %d: a refused upload stays", got, onDisk)
	}
	if got, _, _ := app.StagedBytes(e.posts, t.Context(), e.users["bob"].ID); got != limit {
		t.Errorf("staged bytes = %d after a refusal, want %d", got, limit)
	}
	if got := set.LimitsRefused.With(metrics.LimitStaged).Value(); got != 1 {
		t.Errorf("refusals counted = %d, want 1", got)
	}
	// A file of the attachment limit is refused too.
	if _, err := e.stagedTry("bob", testMaxUpload); err == nil {
		t.Error("a file was staged past the limit")
	}

	// Another person is not affected, and bob's files are not theirs.
	if _, err := e.stagedTry("alice", testMaxUpload); err != nil {
		t.Errorf("another user refused: %+v", err)
	}

	// Posting frees the bytes of the files the post took.
	uploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{first.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.writeWith("bob", "lobby", "with a file", uploads...)
	e.posts.ForgetStaged(t.Context(), e.as("bob"), uploads)
	if _, err := e.stagedTry("bob", testMaxUpload); err != nil {
		t.Errorf("refused after a post took %d bytes: %+v", testMaxUpload, err)
	}
	if _, err := e.stagedTry("bob", 1); err == nil {
		t.Error("the limit moved after a post")
	}

	// Removing a staged file (what a refused post does) frees its bytes.
	lastUploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{last.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.posts.DiscardUploads(t.Context(), e.as("bob"), lastUploads)
	e.posts.ForgetStaged(t.Context(), e.as("bob"), lastUploads)
	if _, err := e.stagedTry("bob", 1); err != nil {
		t.Errorf("refused after a staged byte was removed: %+v", err)
	}
	if _, err := e.stagedTry("bob", 1); err == nil {
		t.Error("the limit moved after a removal")
	}

	// The sweep frees what expired.
	e.clk.Advance(day + time.Minute)
	if n, err := e.posts.SweepStaged(t.Context(), day); err != nil || n == 0 {
		t.Fatalf("SweepStaged = %d, %v", n, err)
	}
	if got, _, err := app.StagedBytes(e.posts, t.Context(), e.users["bob"].ID); err != nil || got != 0 {
		t.Errorf("staged bytes after the sweep = %d, %v, want 0", got, err)
	}
	if _, err := e.stagedTry("bob", testMaxUpload); err != nil {
		t.Errorf("refused after the sweep: %+v", err)
	}
}

// TestPostsStagedBytesCounting shows what the count reads from the notes: the
// notes of one person, of every scope, and none that is damaged; and the
// route of the error of a context that ended.
func TestPostsStagedBytesCounting(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	bob := e.users["bob"].ID
	e.stage("bob", "a.txt", "12345")
	e.stage("alice", "b.txt", "123")
	damaged := e.stage("bob", "c.txt", "123456789")
	if err := e.files.PutNote(damaged.ID, []byte("not a note")); err != nil {
		t.Fatal(err)
	}
	other := e.stage("bob", "d.txt", "1234567")
	if err := e.files.PutNote(other.ID, []byte(strings.Join([]string{"clowder-upload-1", "messages", "1", "1700000000", "7", "0", "0", "text/plain", "d.txt"}, "\n"))); err != nil {
		t.Fatal(err)
	}
	// The note of the messages names user 1, not bob.
	got, _, err := app.StagedBytes(e.posts, t.Context(), bob)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(5)
	if bob == 1 {
		want += 7
	}
	if got != want {
		t.Errorf("staged bytes of bob = %d, want %d", got, want)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = app.StagedBytes(e.posts, ctx, bob)
	_ = requireTag(t, err, app.TagStagedCountCanceled, "Posts.stagedBytes.StagedCountCanceled")
}

// TestPostsStagedLimitOfParallelUploads has several uploads of one person run
// at once and shows that together they never pass the limit.
func TestPostsStagedLimitOfParallelUploads(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	limit := int64(app.StagedLimitFactor * testMaxUpload)
	type result struct{ err error }
	results := make(chan result)
	const uploads = 16
	for range uploads {
		go func() {
			_, err := e.stagedTry("bob", testMaxUpload)
			results <- result{err}
		}()
	}
	accepted := 0
	for range uploads {
		if r := <-results; r.err == nil {
			accepted++
		}
	}
	if accepted > app.StagedLimitFactor {
		t.Errorf("%d uploads of %d bytes were accepted, the limit is %d bytes", accepted, testMaxUpload, limit)
	}
	if got, _, err := app.StagedBytes(e.posts, t.Context(), e.users["bob"].ID); err != nil || got > limit {
		t.Errorf("staged bytes = %d, %v, over the limit %d", got, err, limit)
	}
}

// TestPostsStagedFileLimit is the cap on the number of staged files (step
// 24.1): a user who stages many tiny files, each far under the byte limit,
// stops at StagedFilesFactor times the attachments of a post; the refusal
// leaves nothing on the disk, another user is not affected, and posting frees
// places.
func TestPostsStagedFileLimit(t *testing.T) {
	t.Parallel()
	e := newPostsEnv(t)
	limit := e.posts.StagedFileLimit()
	if limit != app.StagedFilesFactor*testMaxAttachments {
		t.Fatalf("StagedFileLimit = %d, want %d", limit, app.StagedFilesFactor*testMaxAttachments)
	}
	var first app.StagedUpload
	for i := range limit {
		s, err := e.stagedTry("bob", 1)
		if err != nil {
			t.Fatalf("file %d of %d refused: %+v", i+1, limit, err)
		}
		if i == 0 {
			first = s
		}
	}
	onDisk := len(e.onDisk())
	_, err := e.stagedTry("bob", 1)
	appErr := requireTag(t, err, app.TagStagedFull, "Posts.UploadStaged.StagedUploadsFull")
	if appErr.Metadata["fileLimit"] != limit || appErr.Metadata["files"] != limit {
		t.Errorf("metadata = %v, want fileLimit and files %d", appErr.Metadata, limit)
	}
	if got := len(e.onDisk()); got != onDisk {
		t.Errorf("files on disk = %d, want %d: a refused upload stays", got, onDisk)
	}
	if _, count, _ := app.StagedBytes(e.posts, t.Context(), e.users["bob"].ID); count != limit {
		t.Errorf("staged files = %d after a refusal, want %d", count, limit)
	}
	if _, err := e.stagedTry("alice", 1); err != nil {
		t.Errorf("another user refused: %+v", err)
	}
	// Posting one frees one place, and only one.
	uploads, err := e.posts.StagedUploads(t.Context(), e.as("bob"), []string{first.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.writeWith("bob", "lobby", "with a file", uploads...)
	e.posts.ForgetStaged(t.Context(), e.as("bob"), uploads)
	if _, err := e.stagedTry("bob", 1); err != nil {
		t.Errorf("refused after a post took a file: %+v", err)
	}
	if _, err := e.stagedTry("bob", 1); err == nil {
		t.Error("the limit moved after a post")
	}
}
