package app

import "github.com/rm4n0s/clowder/internal/store"

// Actor is the user on whose behalf a service method runs. The web layer
// builds it from the session; the method trusts its Role and checks the
// permission itself. Services take it by value, as the second parameter after
// ctx. Methods for people who are not logged in take no Actor.
type Actor struct {
	// UserID is the ID of the user.
	UserID uint64
	// Username is the username of the user.
	Username string
	// Role is the role of the user.
	Role store.Role
}

// IsAdmin reports whether the actor has the admin role.
func (a Actor) IsAdmin() bool {
	return a.Role == store.RoleAdmin
}

// The tags of the errors that the services return and the handlers turn into
// answers (SPEC §9.1). TagNotFound and TagConflict are the store's own tags,
// the very same strings: an error of the store travels up through a service
// untouched and still matches them. Any other error is a 500.
const (
	// TagNotFound is the tag of something that does not exist, or that the
	// actor may not know exists (a private room, an invitation that is used,
	// expired or unknown). The handlers answer 404. The metadata "op" or
	// "reason" says why, for the log; it is never shown.
	TagNotFound = store.TagNotFound
	// TagConflict is the tag of a change that clashes with the current state
	// (the last admin cannot lose the role, a unique value is taken). The
	// handlers answer 409.
	TagConflict = store.TagConflict
	// TagForbidden is the tag of an action that the actor's role does not
	// allow. The handlers answer 403.
	TagForbidden = "Forbidden"
	// TagInvalidInput is the tag of input that breaks a rule. The metadata
	// "fields" is a map[string]string from the name of each wrong field to a
	// short message for people. The handlers answer 422 and show the messages
	// next to the fields.
	TagInvalidInput = "InvalidInput"
	// TagUnauthenticated is the tag of a request that needs a login and has
	// none. The handlers redirect to the login page.
	TagUnauthenticated = "Unauthenticated"
	// TagRateLimited is the tag of a login attempt refused because the
	// username or the client address failed too many times lately (SPEC
	// §7.1). The handlers answer 429.
	TagRateLimited = "RateLimited"
)
