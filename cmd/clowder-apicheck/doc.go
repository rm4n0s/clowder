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
// Each scenario is a named function of scenarios.go; -only runs just the named
// ones, and -list prints their names. A later step adds its scenarios to the
// list.
package main
