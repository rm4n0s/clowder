// Package callstest is the client of the tests of the calls: a pion peer that
// takes part in a call through the signalling socket of the server, sends
// synthetic Opus and VP8 packets and counts what it receives. It takes
// the small interface TB, which a *testing.T satisfies, so that the tests and
// clowder-perf -calls use it. It lives under internal/calls
// because pion is imported nowhere else (SPEC D63).
package callstest
