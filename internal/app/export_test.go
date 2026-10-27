package app

import "context"

// Len gives the tests the number of keys the limiter holds in memory.
func (l *LoginLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.byUser) + len(l.byIP)
}

// AuditEntry gives the tests the unexported entry of recordAudit.
type AuditEntry = auditEntry

// RecordAudit gives the tests recordAudit. It is a variable, not a wrapper
// function, so that it adds no frame to the route of its errors.
var RecordAudit = recordAudit

// BodyChangeDetails gives the tests bodyChangeDetails.
var BodyChangeDetails = bodyChangeDetails

// Tags of the errors that only the tests match.
const (
	TagAuditDetailsFailed  = tagAuditDetailsFailed
	TagAuditDetailsInvalid = tagAuditDetailsInvalid
)

// CleanEmoji gives the tests cleanEmoji.
var CleanEmoji = cleanEmoji

// TagHashWaitCanceled is the tag of a request that ended while it waited to
// hash a password.
const TagHashWaitCanceled = tagHashWaitCanceled

// FillHashSlots takes every place of the gate of the password hashing, as
// requests that are hashing would, and returns the function that gives them
// back.
func (a *Accounts) FillHashSlots(ctx context.Context) func() {
	for range cap(a.hashes.slots) {
		if err := a.hashes.enter(ctx); err != nil {
			panic(err)
		}
	}
	return func() {
		for range cap(a.hashes.slots) {
			a.hashes.leave()
		}
	}
}
