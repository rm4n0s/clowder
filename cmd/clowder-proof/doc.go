//go:build clowder_browser

// Command clowder-proof is the proof tool: it starts a server on demo data, logs
// in with chromedp and saves a screenshot of a page. It is built only with the
// clowder_browser tag.
//
// Besides -page and -o it takes -invite (starts the server with an invitation
// for a new member and puts its token where -page says {invite}), -fill
// selector=value (repeatable: types into a field) and -click selector (clicks,
// then waits for the page to settle), so that a proof can show a page after a
// form was submitted. With -as, {room:name} in -page stands for the path of the
// room of that name (the whole path, /rooms/<id>), found in the sidebar of the
// user: -page {room:secret}.
package main
