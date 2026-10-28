//go:build clowder_browser

// Command clowder-proof is the proof tool: it starts a server on demo data, logs
// in with chromedp and saves a screenshot of a page. It is built only with the
// clowder_browser tag.
//
// Besides -page and -o it takes -invite (starts the server with an invitation
// for a new member and puts its token where -page says {invite}), -reset
// <user> (starts the server with a password reset link for that demo user and
// puts its token where -page says {reset}), -fill
// selector=value (repeatable: types into a field) and -click selector (repeatable: clicks in
// order, waiting for the page to settle after each), so that a proof can show a page after a
// form was submitted. With -as, {room:name} in -page stands for the path of the
// room of that name (the whole path, /rooms/<id>), found in the sidebar of the
// user: -page {room:secret}.
//
// With -api-login <client name> the -as user also logs in through the API
// (POST /api/v1/auth/login) as a client of that name before the page opens, and
// the session stays: the page of the sessions (/me/sessions) shows it as an App
// next to the browser's.
//
// With -ssh the server also runs its SSH server of the sign-in codes (SPEC
// §7.16) on a free port of 127.0.0.1, so that the Sign-in section of the
// settings page shows the ssh command.
//
// With -ssh-code <user> the tool turns the SSH code at login on for that demo
// user before the page opens (it starts the SSH server, adds a key through the
// API, fetches a code with it and turns the switch on). A proof of the second
// form of the login opens /login without -as and types the password with -fill
// and -click: -page /login -fill '#login-username=alice' -fill
// '#login-password=demo-password-1234' -click '#login-form button[type=submit]'.
//
// With -color-scheme light|dark the browsers report that prefers-color-scheme,
// so that the proof shows the palette of the system theme (a user's own theme
// setting still wins).
//
// With -calls the server also runs its media server of the calls (SPEC §7.18)
// on a free port of 127.0.0.1 and the browsers get a fake camera, microphone and
// screen (browsertest.FakeMedia), so that a proof can show a call: the -other
// user presses Video call with -other-click '[data-call-start="video"]' (and
// -other-hold keeps the call going), the first user answers with -click
// '[data-call-answer="video"]', and a -wait of a few seconds lets the media
// connect. -ring-timeout makes a call that nobody answers missed in seconds.
//
// With -render-icon <svg> the tool starts no server: it renders the SVG file to
// the PNG of -o, -icon-size pixels wide and high, with a transparent ground or
// the -icon-bg color and -icon-pad percent of space around the icon (the
// favicons and, from step 21.5, the icons of the installed app; see
// scripts/README.md).
//
// With -other a second user acts in a second browser while the first one
// watches. With -cluster the tool starts two servers on one new PostgreSQL
// database, one Redis and one data directory, as compose.cluster.yaml does
// (SPEC D41): the -other user acts on the first server and the screenshot is
// taken on the second, so it shows a change that came through Redis. With
// -calls as well, both servers run their media server, each on a port of its
// own, and a call that the -other user starts on the first has its home there
// (SPEC D65): the user of the screenshot answers on the second, whose page
// relays its signalling to the first through Redis, and the screenshot shows
// the call (-other-click '[data-call-start="video"]', -other-hold, -click
// '[data-call-answer="video"]' and a -wait of a few seconds). It needs
// a PostgreSQL server where the user may create databases and a Redis server,
// from -postgres-url and -redis-url or from CLOWDER_TEST_POSTGRES and
// CLOWDER_TEST_REDIS (scripts/test-docker.sh starts both), and prints the
// addresses of both servers.
package main
