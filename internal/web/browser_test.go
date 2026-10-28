//go:build clowder_browser

package web_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/rm4n0s/clowder/internal/browsertest"
)

// demoPassword is the password of the demo users (clowder demo).
const demoPassword = "demo-password-1234"

// pageTimeout bounds one browser test.
const pageTimeout = 60 * time.Second

// pollTimeout bounds one wait for the page to change.
const pollTimeout = 10 * time.Second

// waitFor waits until the JavaScript expression is true in the page.
func waitFor(ctx context.Context, expr string) error {
	_, err := chromedp.Run(ctx, chromedp.Poll[chromedp.Void](expr, chromedp.WithPollingTimeout(pollTimeout)))
	return err
}

// textOf returns the text of the element.
func textOf(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Text(sel))
	if err != nil {
		t.Fatalf("read %s: %v", sel, err)
	}
	return got
}

// openUserMenu opens the menu of the user in the header (a details element
// with no script), where Admin, the theme and Log out are.
func openUserMenu() chromedp.Action[chromedp.Void] {
	return chromedp.Steps(chromedp.Click("#user-menu summary"), chromedp.WaitVisible("#user-menu[open] #logout-form button"))
}

// openPostMenu opens the "more" menu of the post or comment (a details element
// with no script) that the selector finds, where Follow, Edit and Delete are.
func openPostMenu(article string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(chromedp.Click(article+" .post-menu > summary"), chromedp.WaitVisible(article+" .post-menu[open] .post-menu-panel"))
}

// valueOf returns the value of the input.
func valueOf(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Value(sel))
	if err != nil {
		t.Fatalf("read the value of %s: %v", sel, err)
	}
	return got
}

// logIn types the credentials into the login form and submits it.
func logIn(t *testing.T, ctx context.Context, username, password string) {
	t.Helper()
	err := chromedp.Do(ctx,
		browsertest.Fill("#login-username", username),
		browsertest.Fill("#login-password", password),
		chromedp.Click(`#login-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("submit the login form: %v", err)
	}
}

// TestLoginDatastar logs in through the real login form: Datastar sends the
// form, shows a failure in the form, and follows the redirect after a success;
// then it logs out. The home page also proves that datastar.js runs: its
// data-text shows the value of the data-signals.
func TestLoginDatastar(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	// A page that needs a login sends the browser to the login page.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("open the home page: %v", err)
	}
	if got, err := chromedp.Run(ctx, chromedp.Location()); err != nil || got != srv.URL+"/login?next=%2F" {
		t.Fatalf("location = %q, %v; want the login page with next", got, err)
	}

	// A wrong password shows the problem in the form, with the username kept
	// and no password sent back, without leaving the page.
	logIn(t, ctx, "admin", "not the password")
	if err := waitFor(ctx, `document.querySelector('#login-form .form-error') !== null`); err != nil {
		t.Fatalf("the form problem did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#login-form .form-error"); got != "Wrong username or password." {
		t.Errorf("form problem = %q", got)
	}
	if got := valueOf(t, ctx, "#login-username"); got != "admin" {
		t.Errorf("username after the failure = %q, want admin", got)
	}
	// The server sent no password back (the field keeps what was typed in the
	// browser, as Datastar leaves a typed value alone).
	if err := waitFor(ctx, `document.getElementById('login-password').getAttribute('value') === null`); err != nil {
		t.Errorf("the patched form carries a password value: %v", err)
	}

	// The right one goes to the page that was asked for: the home page.
	logIn(t, ctx, "admin", demoPassword)
	if err := chromedp.Do(ctx, chromedp.WaitVisible("#datastar-check")); err != nil {
		t.Fatalf("the home page did not open: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('datastar-check').textContent === 'Datastar is running'`); err != nil {
		t.Errorf("datastar.js did not run on the home page: %v", err)
	}
	if got := textOf(t, ctx, "#signed-in-as"); got != "Signed in as Admin" {
		t.Errorf("greeting = %q, want %q", got, "Signed in as Admin")
	}

	// The login page sends a logged-in person home.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/login"), chromedp.WaitVisible("#signed-in-as")); err != nil {
		t.Fatalf("open the login page while logged in: %v", err)
	}

	// Logging out brings the login page back, and the home page needs a login again.
	if err := chromedp.Do(ctx, openUserMenu(), chromedp.Click("#logout-form button"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("log out: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/"), chromedp.WaitVisible("#login-form")); err != nil {
		t.Fatalf("open the home page after the logout: %v", err)
	}
}

// TestSignUpDatastar signs up through the real form of an invitation: the
// field errors appear next to the fields, the input is kept, and a valid form
// logs the new member in.
func TestSignUpDatastar(t *testing.T) {
	srv := browsertest.StartWith(t, browsertest.Options{InviteMember: true})
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	fill := browsertest.Fill
	err := chromedp.Do(ctx,
		chromedp.Navigate(srv.URL+"/invite/"+srv.InviteToken),
		chromedp.WaitVisible("#signup-form"),
		fill("#signup-username", "ab"),
		fill("#signup-display-name", "Dana"),
		fill("#signup-password", "short"),
		chromedp.Click(`#signup-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("submit the sign-up form: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('signup-password-error') !== null`); err != nil {
		t.Fatalf("the field errors did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#signup-username-error"); got != "Use 3 to 32 characters." {
		t.Errorf("username error = %q", got)
	}
	if got := textOf(t, ctx, "#signup-password-error"); got != "Use at least 12 characters." {
		t.Errorf("password error = %q", got)
	}
	if got := valueOf(t, ctx, "#signup-username"); got != "ab" {
		t.Errorf("username after the failure = %q, want ab", got)
	}
	if got := valueOf(t, ctx, "#signup-display-name"); got != "Dana" {
		t.Errorf("display name after the failure = %q, want Dana", got)
	}
	if err := waitFor(ctx, `document.getElementById('signup-password').getAttribute('value') === null`); err != nil {
		t.Errorf("the patched form carries a password value: %v", err)
	}

	err = chromedp.Do(ctx,
		fill("#signup-username", "dana"),
		fill("#signup-password", "a long enough password"),
		chromedp.Click(`#signup-form button[type="submit"]`),
		chromedp.WaitVisible("#signed-in-as"),
	)
	if err != nil {
		t.Fatalf("submit the corrected sign-up form: %v", err)
	}
	if got := textOf(t, ctx, "#signed-in-as"); got != "Signed in as Dana" {
		t.Errorf("greeting = %q, want %q", got, "Signed in as Dana")
	}

	// The invitation is used up.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/invite/"+srv.InviteToken), chromedp.WaitVisible("h1")); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, ctx, "h1"); got != "404" {
		t.Errorf("heading of the used invitation = %q, want 404", got)
	}
}

// TestLoggedInSidebar logs in as alice with browsertest.LogIn and sees her
// name in the sidebar, without the Admin link.
func TestLoggedInSidebar(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	if err := chromedp.Do(ctx, openUserMenu()); err != nil {
		t.Fatalf("open the user menu: %v", err)
	}
	if got := textOf(t, ctx, "#sidebar-user"); got != "Alice" {
		t.Errorf("user menu name = %q, want Alice", got)
	}
	if err := waitFor(ctx, `document.querySelector('#user-menu a[href="/admin/users"]') === null`); err != nil {
		t.Errorf("a member sees the Admin link: %v", err)
	}

	// The admin pages are invisible to a member.
	if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/admin/users"), chromedp.WaitVisible("h1")); err != nil {
		t.Fatalf("open the admin page: %v", err)
	}
	if got := textOf(t, ctx, "h1"); got != "404" {
		t.Errorf("heading of the admin page for a member = %q, want 404", got)
	}
}

// TestAdminDisablesAUser logs in as the admin, opens the users page from the
// sidebar, disables carol and sees her row say disabled; then enables her
// again and makes an invitation link.
func TestAdminDisablesAUser(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "admin"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	err := chromedp.Do(ctx, openUserMenu(), chromedp.Click(`#user-menu a[href="/admin/users"]`), chromedp.WaitVisible("#users-panel"))
	if err != nil {
		t.Fatalf("open the users page: %v", err)
	}
	carol := `tr[data-username="carol"]`
	if got := textOf(t, ctx, carol+" .state"); got != "Active" {
		t.Fatalf("state of carol = %q, want Active", got)
	}
	// The admin's own row has no disable button.
	if err := waitFor(ctx, `document.querySelector('tr[data-username="admin"] form[action$="/disabled"]') === null`); err != nil {
		t.Errorf("the admin's row has a disable button: %v", err)
	}

	if err := chromedp.Do(ctx, chromedp.Click(carol+` form[action$="/disabled"] button`)); err != nil {
		t.Fatalf("click Disable: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('tr[data-username="carol"] .state').textContent.trim() === 'Disabled'`); err != nil {
		t.Fatalf("carol's row did not say disabled: %v", err)
	}
	if got := textOf(t, ctx, carol+` form[action$="/disabled"] button`); got != "Enable" {
		t.Errorf("button of carol = %q, want Enable", got)
	}

	// Enable her again.
	if err := chromedp.Do(ctx, chromedp.Click(carol+` form[action$="/disabled"] button`)); err != nil {
		t.Fatalf("click Enable: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('tr[data-username="carol"] .state').textContent.trim() === 'Active'`); err != nil {
		t.Fatalf("carol's row did not say active: %v", err)
	}

	// The last admin cannot lose the role: the message shows above the table.
	if err := chromedp.Do(ctx, chromedp.Click(`tr[data-username="admin"] form[action$="/role"] button`)); err != nil {
		t.Fatalf("click Make member: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#users-panel .form-error') !== null`); err != nil {
		t.Fatalf("the last-admin message did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#users-panel .form-error"); !strings.Contains(got, "one active admin") {
		t.Errorf("message = %q", got)
	}

	// An invitation link for an admin appears with its expiry.
	err = chromedp.Do(ctx,
		chromedp.SetValue("#invite-role", "admin"),
		chromedp.Click(`#invite-form button[type="submit"]`),
		chromedp.WaitVisible("#invitation-link"),
	)
	if err != nil {
		t.Fatalf("make an invitation: %v", err)
	}
	link := valueOf(t, ctx, "#invitation-link")
	if !strings.HasPrefix(link, srv.URL+"/invite/") {
		t.Errorf("invitation link = %q, want it to start with %s/invite/", link, srv.URL)
	}
	if got := textOf(t, ctx, "#invitation-box .hint"); !strings.HasPrefix(got, "Expires ") {
		t.Errorf("expiry = %q", got)
	}
	// The copy button copies the link and says so.
	if err := chromedp.Do(ctx, chromedp.Click("#copy-invitation")); err != nil {
		t.Fatalf("click Copy link: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('copy-invitation').textContent === 'Copied'`); err != nil {
		t.Errorf("the copy button did not say Copied: %v", err)
	}
	// The link opens the sign-up page, in a browser that is not logged in.
	other, cancelOther, err := browsertest.NewBrowser(context.Background())
	if err != nil {
		t.Fatalf("second browser: %v", err)
	}
	defer cancelOther()
	otherCtx, cancelCtx := context.WithTimeout(other, pageTimeout)
	defer cancelCtx()
	if err := chromedp.Do(otherCtx, chromedp.Navigate(link), chromedp.WaitVisible("#signup-form")); err != nil {
		t.Fatalf("open the invitation link: %v", err)
	}
}

// TestAlicePlansARoom logs in as alice, makes the private room "plans" through
// the form, adds bob to it and sees him in the members panel; then bob logs in
// in a second browser and finds "plans" in his sidebar and can open it.
func TestAlicePlansARoom(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	// The sidebar lists her rooms, the private one with a lock, and the links.
	if err := waitFor(ctx, `document.querySelector('aside a[data-room="secret"] svg.lock') !== null`); err != nil {
		t.Fatalf("the private room has no lock in the sidebar: %v", err)
	}
	err := chromedp.Do(ctx, chromedp.Click(`aside a[href="/rooms/new"]`), chromedp.WaitVisible("#room-new-form"))
	if err != nil {
		t.Fatalf("open the new room form: %v", err)
	}
	// A member makes private rooms: there is no visibility choice.
	if err := waitFor(ctx, `document.querySelector('#room-new-form select') === null`); err != nil {
		t.Errorf("a member is offered the visibility: %v", err)
	}

	// An empty name shows the problem in the form, without leaving the page.
	err = chromedp.Do(ctx, browsertest.Fill("#room-name", " "), chromedp.Click(`#room-new-form button[type="submit"]`))
	if err != nil {
		t.Fatalf("submit the empty form: %v", err)
	}
	if err := waitFor(ctx, `document.getElementById('room-name-error') !== null`); err != nil {
		t.Fatalf("the field error did not appear: %v", err)
	}
	if got := textOf(t, ctx, "#room-name-error"); got != "Use 1 to 64 characters." {
		t.Errorf("name error = %q", got)
	}

	err = chromedp.Do(ctx,
		browsertest.Fill("#room-name", "plans"),
		browsertest.Fill("#room-description", "What we do next"),
		chromedp.Click(`#room-new-form button[type="submit"]`),
		chromedp.WaitVisible("#room-header"),
	)
	if err != nil {
		t.Fatalf("make the room: %v", err)
	}
	if got := textOf(t, ctx, "#room-header h1"); got != "plans" {
		t.Errorf("heading = %q, want plans", got)
	}
	if err := waitFor(ctx, `document.querySelector('aside a[data-room="plans"].current') !== null`); err != nil {
		t.Errorf("the new room is not the current one in the sidebar: %v", err)
	}
	if err := waitFor(ctx, `document.querySelectorAll('#members-panel li').length === 1`); err != nil {
		t.Fatalf("alice is not the only member: %v", err)
	}

	// Add bob with the user picker; the members panel is patched in place.
	bobID, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`[...document.querySelectorAll('#add-member-user option')].find(o => o.textContent.startsWith('Bob')).value`))
	if err != nil {
		t.Fatalf("find bob in the picker: %v", err)
	}
	err = chromedp.Do(ctx,
		chromedp.SetValue("#add-member-user", bobID),
		chromedp.Click(`#add-member-form button[type="submit"]`),
	)
	if err != nil {
		t.Fatalf("add bob: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('#members-panel li[data-username="bob"]') !== null`); err != nil {
		t.Fatalf("bob did not appear in the members panel: %v", err)
	}
	if got := textOf(t, ctx, `#members-panel li[data-username="bob"] .member-added`); got != "added by Alice" {
		t.Errorf("bob's line = %q, want added by Alice", got)
	}
	// Bob is no longer offered in the picker.
	if err := waitFor(ctx, `![...document.querySelectorAll('#add-member-user option')].some(o => o.textContent.startsWith('Bob'))`); err != nil {
		t.Errorf("bob is still in the picker: %v", err)
	}

	// Bob, in a second browser, has the room in his sidebar.
	other, cancelOther, err := browsertest.NewBrowser(context.Background())
	if err != nil {
		t.Fatalf("second browser: %v", err)
	}
	defer cancelOther()
	otherCtx, cancelCtx := context.WithTimeout(other, pageTimeout)
	defer cancelCtx()
	if err := browsertest.LogIn(otherCtx, srv, "bob"); err != nil {
		t.Fatalf("LogIn bob: %+v", err)
	}
	if err := waitFor(otherCtx, `document.querySelector('aside a[data-room="plans"] svg.lock') !== null`); err != nil {
		t.Fatalf("plans is not in bob's sidebar: %v", err)
	}
	err = chromedp.Do(otherCtx, chromedp.Click(`aside a[data-room="plans"]`), chromedp.WaitVisible("#room-header"))
	if err != nil {
		t.Fatalf("bob opens the room: %v", err)
	}
	if got := textOf(t, otherCtx, "#room-header h1"); got != "plans" {
		t.Errorf("bob sees the heading %q, want plans", got)
	}
	// Bob did not make the room: no rename or delete for him.
	if err := waitFor(otherCtx, `document.getElementById('delete-form') === null && document.getElementById('room-edit') === null`); err != nil {
		t.Errorf("bob is offered to manage the room: %v", err)
	}
}

// TestBrowseAndJoinARoom logs in as carol, who is in no room but general,
// joins "random" from the browse page and lands in the room.
func TestBrowseAndJoinARoom(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "carol"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	err := chromedp.Do(ctx, chromedp.Click(`aside a[href="/rooms"]`), chromedp.WaitVisible("#rooms-panel"))
	if err != nil {
		t.Fatalf("open the browse page: %v", err)
	}
	// She joined general in the demo; random offers a Join button; secret is not listed.
	if err := waitFor(ctx, `document.querySelector('.room-card[data-room="general"] .joined') !== null`); err != nil {
		t.Fatalf("general does not say Joined: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('.room-card[data-room="secret"]') === null`); err != nil {
		t.Errorf("a private room is listed: %v", err)
	}
	err = chromedp.Do(ctx, chromedp.Click(`.room-card[data-room="random"] form button`), chromedp.WaitVisible("#room-header"))
	if err != nil {
		t.Fatalf("join random: %v", err)
	}
	if got := textOf(t, ctx, "#room-header h1"); got != "random" {
		t.Errorf("heading = %q, want random", got)
	}
	if err := waitFor(ctx, `document.querySelector('aside a[data-room="random"].current') !== null`); err != nil {
		t.Errorf("random is not in the sidebar: %v", err)
	}
}

// TestAdminAuditAndRooms logs in as the admin, filters the audit log by alice
// (the table is patched in place and the address keeps the filter), opens the
// admin rooms page, removes bob from the private room "secret" through its
// members list, and finds "Admin removed Bob from #secret" in the audit log.
func TestAdminAuditAndRooms(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "admin"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+"/admin/audit"), chromedp.WaitVisible("#audit-panel table.audit"))
	if err != nil {
		t.Fatalf("open the audit log: %v", err)
	}
	// The demo's log has events of several people.
	const actors = `[...document.querySelectorAll('#audit-panel td.audit-actor')]`
	all, err := chromedp.Run(ctx, chromedp.Evaluate[int](actors+`.length`))
	if err != nil || all == 0 {
		t.Fatalf("events in the log = %d, %v; want some", all, err)
	}
	if err := waitFor(ctx, actors+`.some(td => !td.textContent.includes('@alice'))`); err != nil {
		t.Fatalf("the demo log has events of someone other than alice: %v", err)
	}

	// Filter by alice: Datastar patches the table, the address gets the filter.
	aliceID, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`[...document.querySelectorAll('#audit-actor option')].find(o => o.textContent.includes('@alice')).value`))
	if err != nil {
		t.Fatalf("find alice in the actor filter: %v", err)
	}
	err = chromedp.Do(ctx, chromedp.SetValue("#audit-actor", aliceID), chromedp.Click(`#audit-filter button[type="submit"]`))
	if err != nil {
		t.Fatalf("filter by alice: %v", err)
	}
	if err := waitFor(ctx, actors+`.length > 0 && `+actors+`.every(td => td.textContent.includes('@alice'))`); err != nil {
		t.Fatalf("the table does not show only alice: %v", err)
	}
	if err := waitFor(ctx, `location.search === '?actor=`+aliceID+`'`); err != nil {
		t.Errorf("the address does not keep the filter: %v", err)
	}
	// The form is not patched, so the choice stays.
	if got, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.getElementById('audit-actor').selectedOptions[0].textContent`)); err != nil || !strings.Contains(got, "@alice") {
		t.Errorf("the actor filter shows %q, %v after the patch", got, err)
	}

	// The rooms page: the tab bar leads there, and shows every room.
	err = chromedp.Do(ctx, chromedp.Click(`.admin-tabs a[href="/admin/rooms"]`), chromedp.WaitVisible("#admin-rooms"))
	if err != nil {
		t.Fatalf("open the admin rooms page: %v", err)
	}
	secret := `li.admin-room[data-room="secret"]`
	if err := waitFor(ctx, `document.querySelector('`+secret+` .badge-private') !== null`); err != nil {
		t.Fatalf("the private room secret is not listed as private: %v", err)
	}
	// The members list loads when the toggle is opened.
	err = chromedp.Do(ctx, chromedp.Click(secret+` details.admin-room-members summary`))
	if err != nil {
		t.Fatalf("open the members of secret: %v", err)
	}
	bob := secret + ` li[data-username="bob"]`
	if err := waitFor(ctx, `document.querySelector('`+bob+`') !== null`); err != nil {
		t.Fatalf("bob did not appear in the members of secret: %v", err)
	}
	if got := textOf(t, ctx, secret+" .room-members-count"); got != "2 members" {
		t.Errorf("member count = %q, want 2 members", got)
	}
	// Remove bob; the dialog that asks is answered yes.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(window.confirm = () => true, true)`)); err != nil {
		t.Fatalf("answer the dialog: %v", err)
	}
	if err := chromedp.Do(ctx, chromedp.Click(bob+` button`)); err != nil {
		t.Fatalf("click Remove: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+bob+`') === null && document.querySelector('`+secret+` li[data-username="alice"]') !== null`); err != nil {
		t.Fatalf("bob is still in the list, or alice is gone: %v", err)
	}
	if err := waitFor(ctx, `document.querySelector('`+secret+` .room-members-count').textContent === '1 member'`); err != nil {
		t.Errorf("the member count did not drop: %v", err)
	}

	// The audit log tells who did it.
	err = chromedp.Do(ctx, chromedp.Click(`.admin-tabs a[href="/admin/audit"]`), chromedp.WaitVisible("#audit-panel table.audit"))
	if err != nil {
		t.Fatalf("open the audit log again: %v", err)
	}
	if err := waitFor(ctx, `[...document.querySelectorAll('#audit-panel td.audit-summary > span')].some(s => s.textContent === 'Admin removed Bob from #secret')`); err != nil {
		t.Fatalf("the audit log does not say that the admin removed bob from #secret: %v", err)
	}
	// Its room links to the row on the rooms page.
	if err := waitFor(ctx, `document.querySelector('#audit-panel tr[data-action="member.removed"] td.audit-room a[href^="/admin/rooms#room-"]') !== null`); err != nil {
		t.Errorf("the room of the event is not a link to the rooms page: %v", err)
	}
}

// TestAdminPagesForAMemberInTheBrowser logs in as alice: the admin pages are
// the 404 page.
func TestAdminPagesForAMemberInTheBrowser(t *testing.T) {
	srv := browsertest.Start(t)
	browser := browsertest.Browser(t, srv)
	ctx, cancel := context.WithTimeout(browser, pageTimeout)
	defer cancel()

	if err := browsertest.LogIn(ctx, srv, "alice"); err != nil {
		t.Fatalf("LogIn: %+v", err)
	}
	for _, path := range []string{"/admin/rooms", "/admin/audit"} {
		if err := chromedp.Do(ctx, chromedp.Navigate(srv.URL+path), chromedp.WaitVisible("h1")); err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		if got := textOf(t, ctx, "h1"); got != "404" {
			t.Errorf("heading of %s for a member = %q, want 404", path, got)
		}
	}
}
