//go:build clowder_browser

package web_test

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// TestAliceWritesAPostWithTasks writes a post in "general" with two task rows
// added with the Add task button (one assigned to bob, with a due date), sees
// the tasks under the post with "To do" badges, edits the post, removes one
// task and renames the other, and sees the post update.
func TestAliceWritesAPostWithTasks(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	openGeneral(t, ctx)

	// The editor starts with no rows; Add task asks the server for each, with
	// the next index.
	if err := waitFor(ctx, `document.querySelectorAll('#post-task-rows-new .task-row').length === 0`); err != nil {
		t.Fatalf("the new editor has rows: %v", err)
	}
	add := `#post-editor-new .tasks-field > button`
	if err := chromedp.Do(ctx, chromedp.Click(add)); err != nil {
		t.Fatalf("add the first row: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#post-task-rows-new .task-row').length === 1`); err != nil {
		t.Fatalf("the first row did not appear: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Click(add)); err != nil {
		t.Fatalf("add the second row: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#post-task-rows-new .task-row').length === 2`); err != nil {
		t.Fatalf("the second row did not appear: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('[name="tasks[0][title]"]') !== null && document.querySelector('[name="tasks[1][title]"]') !== null`); err != nil {
		t.Fatalf("the rows are not numbered 0 and 1: %v", err)
	}
	// The picker offers the members of general, bob among them.
	if err := waitFor(ctx, `[...document.querySelectorAll('select[name="tasks[0][assignees]"] option')].some(o => o.textContent.includes('@bob'))`); err != nil {
		t.Fatalf("the picker does not offer bob: %v", err)
	}

	due := time.Now().UTC().AddDate(0, 0, 7).Format("2006-01-02")
	err := chromedp.Do(ctx,
		browsertest.Fill("#post-body-new", "Planning the offsite"),
		browsertest.Fill(`[name="tasks[0][title]"]`, "Book the room"),
		browsertest.Fill(`[name="tasks[1][title]"]`, "Send the agenda"),
	)
	if err != nil {
		t.Fatalf("fill the editor: %v", err)
	}
	runJS(t, ctx, `(() => {
		const sel = document.querySelector('select[name="tasks[0][assignees]"]');
		[...sel.options].forEach(o => { o.selected = o.textContent.includes('@bob'); });
		const d = document.querySelector('[name="tasks[0][due]"]');
		d.value = '`+due+`';
		return d.value === '`+due+`';
	})()`)
	if err := chromedp.Do(ctx, chromedp.Click(`#post-editor-new button[type="submit"]`)); err != nil {
		t.Fatalf("submit the post: %v", err)
	}

	// The post is at the top of the feed with its two tasks and their badges.
	first := `#feed-posts article.post:first-child`
	if err := waitFor(ctx, `document.querySelectorAll('`+first+` .post-task').length === 2`); err != nil {
		t.Fatalf("the new post does not show two tasks: %v", err)
	}
	titles := `[...document.querySelectorAll('` + first + ` .task-title')].map(e => e.textContent).join('|')`
	if got, _ := chromedp.Run(ctx, chromedp.Evaluate[string](titles)); got != "Book the room|Send the agenda" {
		t.Errorf("task titles = %q", got)
	}
	badges := `[...document.querySelectorAll('` + first + ` .task-column')].map(e => e.textContent).join('|')`
	if got, _ := chromedp.Run(ctx, chromedp.Evaluate[string](badges)); got != "To do|To do" {
		t.Errorf("column badges = %q, want To do|To do", got)
	}
	if got := textOf(t, ctx, first+" .post-task:first-child .task-assignees"); got != "Bob" {
		t.Errorf("assignees of the first task = %q, want Bob", got)
	}
	if got := textOf(t, ctx, first+" .post-task:first-child .task-due"); got != due {
		t.Errorf("due date of the first task = %q, want %q", got, due)
	}
	if got, _ := chromedp.Run(ctx, chromedp.Evaluate[bool](`document.querySelector('`+first+` .post-task:last-child .task-due') === null`)); !got {
		t.Error("the second task shows a due date")
	}
	if got, _ := chromedp.Run(ctx, chromedp.Evaluate[string](`document.querySelector('`+first+` .post-task a.task-link').getAttribute('href')`)); got == "" {
		t.Error("the task has no link to the board")
	}
	// The editor is fresh again: no rows.
	if err := waitFor(ctx, `document.querySelectorAll('#post-task-rows-new .task-row').length === 0`); err != nil {
		t.Errorf("the editor kept the rows of the post: %v", err)
	}

	// Edit: the rows come filled; remove the second, rename the first.
	err = chromedp.Do(ctx, chromedp.Click(first+" .post-edit"), chromedp.WaitVisible(first+" textarea"))
	if err != nil {
		t.Fatalf("open the editor of the post: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('`+first+` .task-row').length === 2`); err != nil {
		t.Fatalf("the editor does not hold the two tasks: %v", err)
	}
	if got := valueOf(t, ctx, first+` [name="tasks[0][title]"]`); got != "Book the room" {
		t.Errorf("first title in the editor = %q", got)
	}
	if got := valueOf(t, ctx, first+` [name="tasks[0][due]"]`); got != due {
		t.Errorf("first due date in the editor = %q, want %q", got, due)
	}
	if got, _ := chromedp.Run(ctx, chromedp.Evaluate[bool](`[...document.querySelectorAll('`+first+` select[name="tasks[0][assignees]"] option')].some(o => o.selected && o.textContent.includes('@bob'))`)); !got {
		t.Error("bob is not selected on the first task in the editor")
	}
	err = chromedp.Do(ctx,
		chromedp.Click(first+" .task-row:nth-child(2) .task-remove"),
		browsertest.Fill(first+` [name="tasks[0][title]"]`, "Book the big room"),
	)
	if err != nil {
		t.Fatalf("change the tasks: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('`+first+` .task-row').length === 1`); err != nil {
		t.Fatalf("the second row did not leave the editor: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Click(first+` button[type="submit"]`)); err != nil {
		t.Fatalf("save the post: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+first+` .post-edited') !== null && document.querySelectorAll('`+first+` .post-task').length === 1`); err != nil {
		t.Fatalf("the post did not update to one task: %v", err)
	}
	if got := textOf(t, ctx, first+" .post-task .task-title"); got != "Book the big room" {
		t.Errorf("task title after the edit = %q", got)
	}
	if got := textOf(t, ctx, first+" .post-task .task-assignees"); got != "Bob" {
		t.Errorf("assignees after the edit = %q, want Bob (kept)", got)
	}
	if got := textOf(t, ctx, first+" .post-task .task-column"); got != "To do" {
		t.Errorf("badge after the edit = %q", got)
	}
}
