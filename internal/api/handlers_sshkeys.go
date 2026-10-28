package api

import (
	"net/http"

	"github.com/rm4n0s/clowder/internal/app"
)

// sshKeyJSON is an SSH key in the answers of the API: never the key itself.
type sshKeyJSON struct {
	ID          uint64  `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Fingerprint string  `json:"fingerprint"`
	CreatedAt   string  `json:"created_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

// sshServerJSON is what the API says of the SSH server (SPEC §7.16).
type sshServerJSON struct {
	Enabled         bool   `json:"enabled"`
	HostFingerprint string `json:"host_fingerprint"`
	Command         string `json:"command"`
}

// sshKeysResponse is the answer of GET /me/ssh-keys.
type sshKeysResponse struct {
	Keys   []sshKeyJSON  `json:"keys"`
	Server sshServerJSON `json:"server"`
}

// sshKeyRequest is the body of POST /me/ssh-keys.
type sshKeyRequest struct {
	Name            string `json:"name"`
	PublicKey       string `json:"public_key"`
	CurrentPassword string `json:"current_password"`
}

// sshKeyRemoveRequest is the body of DELETE /me/ssh-keys/{id}.
type sshKeyRemoveRequest struct {
	CurrentPassword string `json:"current_password"`
}

// sshKeyOf makes the JSON of a key.
func sshKeyOf(k app.SSHKey) sshKeyJSON {
	out := sshKeyJSON{ID: k.ID, Name: k.Name, Type: k.Type, Fingerprint: k.Fingerprint, CreatedAt: rfc3339(k.CreatedAt)}
	if k.LastUsedAt != nil {
		last := rfc3339(*k.LastUsedAt)
		out.LastUsedAt = &last
	}
	return out
}

// sshKeyRoutes are the routes of the SSH keys of the logged-in user (SPEC
// §7.16).
func (s *Server) sshKeyRoutes() []Route {
	return []Route{
		s.route(http.MethodGet, "/me/ssh-keys", false, s.listSSHKeys),
		s.route(http.MethodPost, "/me/ssh-keys", false, s.addSSHKey),
		s.route(http.MethodDelete, "/me/ssh-keys/{id}", false, s.removeSSHKey),
	}
}

// listSSHKeys answers GET /me/ssh-keys: the keys of the user, oldest first, and
// what is known of the SSH server (whether it runs, the fingerprint of its host
// key and the ssh command to run).
func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	list, err := s.svc.SSHKeys.List(r.Context(), c.actor)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	server := s.svc.SSHKeys.Server(c.actor.Username)
	out := sshKeysResponse{
		Keys:   make([]sshKeyJSON, len(list)),
		Server: sshServerJSON{Enabled: server.Enabled, HostFingerprint: server.HostFingerprint, Command: server.Command},
	}
	for i, k := range list {
		out.Keys[i] = sshKeyOf(k)
	}
	s.writeJSON(w, r, http.StatusOK, out)
}

// addSSHKey answers POST /me/ssh-keys: it registers the public key under the
// name after it checks the current password, and returns the key with 201.
func (s *Server) addSSHKey(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	var body sshKeyRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	key, err := s.svc.SSHKeys.Add(r.Context(), c.actor, body.Name, body.PublicKey, body.CurrentPassword, s.clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, sshKeyOf(key))
}

// removeSSHKey answers DELETE /me/ssh-keys/{id}: it removes the key after it
// checks the current password, which comes as a JSON body. A key that does not
// exist and a key of someone else are both 404.
func (s *Server) removeSSHKey(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var body sshKeyRemoveRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.svc.SSHKeys.Remove(r.Context(), c.actor, id, body.CurrentPassword, s.clientIP(r)); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeNoContent(w)
}
