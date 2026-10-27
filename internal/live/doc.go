// Package live is the event hub: services publish events and the server-sent
// event streams subscribe to them (SPEC D18, §8).
//
// An Event only says that something changed, on which topic and about which
// room, post or user. It never carries content: a consumer re-reads what it
// needs from the services, which check the permissions again. The package
// imports nothing of Clowder, so every layer may use it.
package live
