package app

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
