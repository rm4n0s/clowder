//go:build clowder_browser

// Command clowder-proof is the proof tool: it starts a server on demo data, logs
// in with chromedp and saves a screenshot of a page. It is built only with the
// clowder_browser tag.
//
// Besides -page and -o it takes -invite (starts the server with an invitation
// for a new member and puts its token where -page says {invite}), -fill
// selector=value (repeatable: types into a field) and -click selector (repeatable: clicks in
// order, waiting for the page to settle after each), so that a proof can show a page after a
// form was submitted. With -as, {room:name} in -page stands for the path of the
// room of that name (the whole path, /rooms/<id>), found in the sidebar of the
// user: -page {room:secret}.
//
// With -other a second user acts in a second browser while the first one
// watches. With -cluster the tool starts two servers on one new PostgreSQL
// database, one Redis and one data directory, as compose.cluster.yaml does
// (SPEC D41): the -other user acts on the first server and the screenshot is
// taken on the second, so it shows a change that came through Redis. It needs
// a PostgreSQL server where the user may create databases and a Redis server,
// from -postgres-url and -redis-url or from CLOWDER_TEST_POSTGRES and
// CLOWDER_TEST_REDIS (scripts/test-docker.sh starts both), and prints the
// addresses of both servers.
package main
