// Package api is the REST API of Clowder for desktop clients (SPEC §7.12, D42
// to D45): JSON over HTTP under /api/v1/, authenticated by a bearer token.
//
// Like internal/web it calls only the services of internal/app and never
// imports the store, templ or internal/web. The server of internal/web mounts
// the routes of a Server (Routes) on its own mux, so that the API goes through
// the same middleware chain as the pages: security headers, the request log,
// the deadline of the body and cross-origin protection.
//
// A route is added in three places: its handler and its entry in routes (in
// the file of its group), its operation in openapi.json, and a test that sends
// a request through checkResponse. TestOpenAPIMatchesRoutes fails when the
// routes and the document differ.
package api
