// Command clowder-apicheck is a small client of the REST API of Clowder
// (SPEC §7.12). It logs in to a running server, runs a list of named
// scenarios against it and prints each request and answer, so that it shows a
// writer of a desktop client what a session with the API looks like. It is
// also the proof of the API steps (docs/IMPLEMENTATION.md, M16).
//
//	clowder-apicheck -base-url http://localhost:8080 -username alice -password …
//
// The password may also be given in the environment variable
// CLOWDER_APICHECK_PASSWORD. Every request is printed as "> METHOD path" and
// every answer as "< status" followed by the JSON, indented. Tokens are printed
// as <token> and passwords as <password>, wherever they appear. The exit status
// is 0 when every step got the status it expects, 1 when one did not (the run
// stops there), and 2 for wrong flags.
//
// The board scenario also needs a second user, who is assigned a task and
// reads the notifications: -other-username (bob) and -other-password (the
// environment variable CLOWDER_APICHECK_OTHER_PASSWORD, or the password of
// the demo users).
//
// The messages scenario writes to the other user, and puts the other user and
// -third-username (carol) in a group. The admin scenario logs in as
// -admin-username (admin) with -admin-password (the environment variable
// CLOWDER_APICHECK_ADMIN_PASSWORD, or the password of the demo users); an
// empty -admin-username leaves the scenario out, for a server whose admin is
// not known.
//
// The watch scenario (also -watch, which runs only it) opens the WebSocket
// (SPEC §7.13), subscribes to a new room, its board and the conversation with
// the other user, and prints each message that comes while the other user
// writes a post with a task, a comment, a reaction, moves the card and sends a
// message through REST. Each write must be followed by its events within
// -event-timeout (5 seconds), or the run fails. -other-base-url sends the
// other user's writes to another process of the same installation (SPEC
// §8.1), so that the events cross from one process to the other.
//
// -ssh-key <path> (an unencrypted OpenSSH private key of the user) and
// -ssh-addr <host:port> (by default the host of -base-url and port 2222) are
// for a user who turned on the SSH code at login (SPEC §7.16): every login of
// the user first sends the password alone, expects 401 with the code
// ssh_code_required, fetches a code over SSH with the key (the command json;
// the fingerprint of the host key is printed, not checked), and logs in again
// with ssh_code. The code is printed as <code>. A user without the code on
// logs in at once.
//
// Each scenario is a named function of scenarios.go; -only runs just the named
// ones, and -list prints their names. A later step adds its scenarios to the
// list.
package main
