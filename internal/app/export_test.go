package app

// Len gives the tests the number of keys the limiter holds in memory.
func (l *LoginLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.byUser) + len(l.byIP)
}
