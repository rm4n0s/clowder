// Package callstest is the client of the tests of the calls: a pion peer that
// takes part in a call through the signalling socket of the server, sends
// synthetic Opus and VP8 packets and counts what it receives. It imports
// "testing", so only test code imports it. It lives under internal/calls
// because pion is imported nowhere else (SPEC D63).
package callstest
