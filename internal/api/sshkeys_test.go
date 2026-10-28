package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/clowder/internal/store"
	"golang.org/x/crypto/ssh"
)

// keyLine makes the line of the .pub file of a new ed25519 key.
func keyLine(t testing.TB) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " me@laptop"
}

// rsaLine makes the line of the .pub file of a new RSA key of the size.
func rsaLine(t testing.TB, bits int) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	key, err := ssh.NewPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatalf("NewPublicKey: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// dsaLine is the line of an ssh-dss key, a type Clowder does not accept.
const dsaLine = "ssh-dss AAAAB3NzaC1kc3MAAACBALHTz0WfOvQDhAV/rIMDI/fgfPPdXeiXkyncZ/NsILm5P9ZSiockiR4satpB3dLxqyymbO7jUdEXLd41hrnVTpNTQmK4m6RnScbcKn9AF8xuNX99/PvLpIFwTIqPIbsADuPU4OJ6pV6pQy+aI13a51DbXoWbQZB1DCNg9uQkkXApAAAAFQCPvp5V2waOI4f0b0dICn0wXyl4TwAAAIBEhgS2eJQ7cQyoal+VOzGKXB4l6LHx2mltXbNVjFDAvqpSyodJNAReHXK/1dc59+Ua4o0N4PTSVt0QcJtXXSm5nDiSDeb7l77b8JCrruwpjw9iIcu13ONv1WKaecO/04G33mghHY7M5OpmKkkgPsbsjJfR/hCS0uihGpa66vd45AAAAIEAj8kG1d4zQ4d86XfheSiEsn8EBDWqUZhKHCPRbzuB2XZQj0HHpKloGLi/0QLRbVlPc8MpXoWylQerwyoTdWOI3Y7pKCozilYxcUyUbO1WWMzy3dS6IeoX93kFP5Jeg7u4p1HVTL8m5/CqY6TH3QMPlLx2BHKklaIdteIzkaAjqzI="

// sshKeyEnv is a testEnv with alice, bob and an admin.
func sshKeyEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t, nil)
	e.addUser("admin", store.RoleAdmin)
	e.addUser("alice", store.RoleMember)
	e.addUser("bob", store.RoleMember)
	return e
}

// addKey adds a key through the API as the user and returns the answer.
func (e *testEnv) addKey(user, name, line, password string) map[string]any {
	e.t.Helper()
	rec := e.send(request(http.MethodPost, "/api/v1/me/ssh-keys",
		map[string]any{"name": name, "public_key": line, "current_password": password}, bearer(e.token(user))))
	requireStatus(e.t, rec, http.StatusCreated)
	return decode(e.t, rec)
}

func TestListSSHKeys(t *testing.T) {
	t.Parallel()
	t.Run("empty, with the server on", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		rec := e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusOK)
		got := decode(t, rec)
		if keys, ok := got["keys"].([]any); !ok || len(keys) != 0 {
			t.Errorf("keys = %v, want []", got["keys"])
		}
		server, _ := got["server"].(map[string]any)
		if server["enabled"] != true || server["host_fingerprint"] != "SHA256:testhostkeyfingerprint" ||
			server["command"] != "ssh -p 2222 alice@chat.example.org" {
			t.Errorf("server = %v", server)
		}
	})
	t.Run("with the server off", func(t *testing.T) {
		t.Parallel()
		e := newTestEnvSSH(t, nil, app.SSHServerInfo{})
		e.addUser("alice", store.RoleMember)
		rec := e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusOK)
		server, _ := decode(t, rec)["server"].(map[string]any)
		if server["enabled"] != false || server["host_fingerprint"] != "" || server["command"] != "" {
			t.Errorf("server = %v", server)
		}
	})
	t.Run("the keys of the user only", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		mine := e.addKey("alice", "laptop", keyLine(t), testPassword)
		e.addKey("bob", "phone", keyLine(t), testPassword)
		rec := e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusOK)
		keys, _ := decode(t, rec)["keys"].([]any)
		if len(keys) != 1 {
			t.Fatalf("keys = %v", keys)
		}
		k, _ := keys[0].(map[string]any)
		if k["id"] != mine["id"] || k["name"] != "laptop" || k["type"] != "ssh-ed25519" || k["last_used_at"] != nil {
			t.Errorf("key = %v", k)
		}
		if strings.Contains(rec.Body.String(), "AAAA") {
			t.Error("the answer holds a key line")
		}
	})
	t.Run("an admin sees their own only", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		e.addKey("alice", "laptop", keyLine(t), testPassword)
		rec := e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("admin"))))
		requireStatus(t, rec, http.StatusOK)
		if keys, _ := decode(t, rec)["keys"].([]any); len(keys) != 0 {
			t.Errorf("keys = %v", keys)
		}
	})
}

func TestAddSSHKey(t *testing.T) {
	t.Parallel()
	rsa3072 := rsaLine(t, 3072)
	tests := []struct {
		name   string
		body   any
		status int
		field  string
	}{
		{"ed25519", map[string]any{"name": "laptop", "public_key": keyLine(t), "current_password": testPassword}, http.StatusCreated, ""},
		{"rsa 3072", map[string]any{"name": "old", "public_key": rsa3072, "current_password": testPassword}, http.StatusCreated, ""},
		{"rsa 2048", map[string]any{"name": "old", "public_key": rsaLine(t, 2048), "current_password": testPassword}, http.StatusUnprocessableEntity, "public_key"},
		{"dsa", map[string]any{"name": "old", "public_key": dsaLine, "current_password": testPassword}, http.StatusUnprocessableEntity, "public_key"},
		{"text that is not a key", map[string]any{"name": "k", "public_key": "hello", "current_password": testPassword}, http.StatusUnprocessableEntity, "public_key"},
		{"no key", map[string]any{"name": "k", "public_key": "", "current_password": testPassword}, http.StatusUnprocessableEntity, "public_key"},
		{"no name", map[string]any{"name": "", "public_key": keyLine(t), "current_password": testPassword}, http.StatusUnprocessableEntity, "name"},
		{"a name of 41 characters", map[string]any{"name": strings.Repeat("é", 41), "public_key": keyLine(t), "current_password": testPassword}, http.StatusUnprocessableEntity, "name"},
		{"a name of 40 characters", map[string]any{"name": strings.Repeat("é", 40), "public_key": keyLine(t), "current_password": testPassword}, http.StatusCreated, ""},
		{"a wrong password", map[string]any{"name": "k", "public_key": keyLine(t), "current_password": "not the password"}, http.StatusUnprocessableEntity, "current_password"},
		{"no password", map[string]any{"name": "k", "public_key": keyLine(t)}, http.StatusUnprocessableEntity, "current_password"},
		{"an unknown field", map[string]any{"name": "k", "public_key": keyLine(t), "current_password": testPassword, "user_id": 1}, http.StatusUnprocessableEntity, "user_id"},
		{"a name that is a number", `{"name": 5, "public_key": "x", "current_password": "x"}`, http.StatusUnprocessableEntity, "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := sshKeyEnv(t)
			rec := e.send(request(http.MethodPost, "/api/v1/me/ssh-keys", tt.body, bearer(e.token("alice"))))
			requireStatus(t, rec, tt.status)
			listed, _ := decode(t, e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice")))))["keys"].([]any)
			if tt.status != http.StatusCreated {
				if _, ok := errorFields(t, rec)[tt.field]; !ok {
					t.Errorf("fields = %v, want %q", errorFields(t, rec), tt.field)
				}
				if len(listed) != 0 {
					t.Errorf("a refused key was stored: %v", listed)
				}
				return
			}
			key := decode(t, rec)
			if key["id"] == nil || key["fingerprint"] == nil || !strings.HasPrefix(key["fingerprint"].(string), "SHA256:") ||
				key["last_used_at"] != nil || key["created_at"] == nil {
				t.Errorf("key = %v", key)
			}
			if strings.Contains(rec.Body.String(), "AAAA") {
				t.Error("the answer holds the key line")
			}
		})
	}
}

func TestAddSSHKeyIsListedAndAudited(t *testing.T) {
	t.Parallel()
	e := sshKeyEnv(t)
	line := keyLine(t)
	key := e.addKey("alice", "work laptop", line, testPassword)
	rec := e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice"))))
	keys, _ := decode(t, rec)["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["fingerprint"] != key["fingerprint"] {
		t.Fatalf("keys = %v", keys)
	}
	rows, err := e.st.ListAuditEvents(t.Context(), store.AuditFilter{EntityType: store.EntityUser}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Action != store.ActionUserSSHKeyAdded ||
		!strings.Contains(rows[0].Summary, "work laptop") || strings.Contains(rows[0].Summary+rows[0].Details, strings.Fields(line)[1]) {
		t.Errorf("audit = %+v", rows)
	}
}

func TestAddSSHKeyClashes(t *testing.T) {
	t.Parallel()
	t.Run("a key that is registered", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		line := keyLine(t)
		e.addKey("bob", "bobs", line, testPassword)
		var messages []string
		for _, user := range []string{"alice", "bob"} {
			rec := e.send(request(http.MethodPost, "/api/v1/me/ssh-keys",
				map[string]any{"name": "again", "public_key": line, "current_password": testPassword}, bearer(e.token(user))))
			requireStatus(t, rec, http.StatusConflict)
			if errorCode(t, rec) != "conflict" {
				t.Errorf("code = %s", errorCode(t, rec))
			}
			msg, _ := errorFields(t, rec)["public_key"].(string)
			messages = append(messages, msg)
			if msg == "" || strings.Contains(strings.ToLower(msg), "bob") {
				t.Errorf("fields = %v", errorFields(t, rec))
			}
		}
		if messages[0] != messages[1] {
			t.Errorf("the message says who owns the key: %q, %q", messages[0], messages[1])
		}
	})
	t.Run("the eleventh key", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		for i := range app.SSHKeyMaxPerUser {
			e.addKey("alice", "key "+strconv.Itoa(i), keyLine(t), testPassword)
		}
		rec := e.send(request(http.MethodPost, "/api/v1/me/ssh-keys",
			map[string]any{"name": "eleventh", "public_key": keyLine(t), "current_password": testPassword}, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusConflict)
		if _, ok := errorFields(t, rec)["public_key"]; !ok {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
	})
}

func TestRemoveSSHKey(t *testing.T) {
	t.Parallel()
	t.Run("removes", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		k := e.addKey("alice", "laptop", keyLine(t), testPassword)
		path := "/api/v1/me/ssh-keys/" + strconv.Itoa(int(k["id"].(float64)))
		rec := e.send(request(http.MethodDelete, path, map[string]any{"current_password": testPassword}, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusNoContent)
		keys, _ := decode(t, e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice")))))["keys"].([]any)
		if len(keys) != 0 {
			t.Errorf("keys = %v", keys)
		}
		rows, _ := e.st.ListAuditEvents(t.Context(), store.AuditFilter{EntityType: store.EntityUser}, 10, 0)
		if len(rows) != 2 || rows[0].Action != store.ActionUserSSHKeyRemoved {
			t.Errorf("audit = %+v", rows)
		}
		// Again: gone.
		rec = e.send(request(http.MethodDelete, path, map[string]any{"current_password": testPassword}, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusNotFound)
	})
	t.Run("a wrong password", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		k := e.addKey("alice", "laptop", keyLine(t), testPassword)
		path := "/api/v1/me/ssh-keys/" + strconv.Itoa(int(k["id"].(float64)))
		rec := e.send(request(http.MethodDelete, path, map[string]any{"current_password": "nope"}, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusUnprocessableEntity)
		if _, ok := errorFields(t, rec)["current_password"]; !ok {
			t.Errorf("fields = %v", errorFields(t, rec))
		}
		keys, _ := decode(t, e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("alice")))))["keys"].([]any)
		if len(keys) != 1 {
			t.Errorf("the key is gone: %v", keys)
		}
	})
	t.Run("another user's key and an unknown key", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		theirs := e.addKey("bob", "phone", keyLine(t), testPassword)
		for name, id := range map[string]string{"another user's": strconv.Itoa(int(theirs["id"].(float64))), "unknown": "9999"} {
			rec := e.send(request(http.MethodDelete, "/api/v1/me/ssh-keys/"+id, map[string]any{"current_password": testPassword}, bearer(e.token("alice"))))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: status %d, want 404", name, rec.Code)
			}
		}
		// An admin has no power over the keys of others.
		rec := e.send(request(http.MethodDelete, "/api/v1/me/ssh-keys/"+strconv.Itoa(int(theirs["id"].(float64))), map[string]any{"current_password": testPassword}, bearer(e.token("admin"))))
		requireStatus(t, rec, http.StatusNotFound)
		keys, _ := decode(t, e.send(request(http.MethodGet, "/api/v1/me/ssh-keys", nil, bearer(e.token("bob")))))["keys"].([]any)
		if len(keys) != 1 {
			t.Errorf("the key of bob was removed: %v", keys)
		}
	})
	t.Run("a bad ID and a missing body", func(t *testing.T) {
		t.Parallel()
		e := sshKeyEnv(t)
		rec := e.send(request(http.MethodDelete, "/api/v1/me/ssh-keys/abc", map[string]any{"current_password": testPassword}, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusNotFound)
		rec = e.send(request(http.MethodDelete, "/api/v1/me/ssh-keys/1", nil, bearer(e.token("alice"))))
		requireStatus(t, rec, http.StatusUnsupportedMediaType)
	})
}

func TestSSHKeysCountInTheLimiter(t *testing.T) {
	t.Parallel()
	e := sshKeyEnv(t)
	token := e.token("alice")
	for range 5 {
		rec := e.send(request(http.MethodPost, "/api/v1/me/ssh-keys",
			map[string]any{"name": "k", "public_key": keyLine(t), "current_password": "wrong"}, bearer(token), fromAddr("198.51.100.7:1111")))
		requireStatus(t, rec, http.StatusUnprocessableEntity)
	}
	rec := e.send(request(http.MethodPost, "/api/v1/me/ssh-keys",
		map[string]any{"name": "k", "public_key": keyLine(t), "current_password": testPassword}, bearer(token), fromAddr("198.51.100.8:1111")))
	requireStatus(t, rec, http.StatusTooManyRequests)
	if errorCode(t, rec) != "rate_limited" {
		t.Errorf("code = %s", errorCode(t, rec))
	}
}

func TestSSHKeyRoutesNeedLogin(t *testing.T) {
	t.Parallel()
	e := sshKeyEnv(t)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me/ssh-keys"},
		{http.MethodPost, "/api/v1/me/ssh-keys"},
		{http.MethodDelete, "/api/v1/me/ssh-keys/1"},
	} {
		rec := e.send(request(tt.method, tt.path, map[string]any{"current_password": "x"}))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: status %d, want 401", tt.method, tt.path, rec.Code)
		}
	}
}
