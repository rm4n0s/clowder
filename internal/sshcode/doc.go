// Package sshcode is the SSH server of the sign-in codes (SPEC §7.16, D52). A
// user who registered a public key runs ssh and the server answers with a
// one-time code, then closes the connection. It offers nothing else: no
// password, no shell, no forwarding.
//
// The server talks to the rest of Clowder through two small interfaces,
// SignIn and Limiter, which internal/app satisfies. It is the only package
// that runs an SSH server, and it never imports internal/store.
package sshcode
