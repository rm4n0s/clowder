package app

import (
	"context"
	"crypto/rsa"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rm4n0s/clowder/internal/clock"
	"github.com/rm4n0s/clowder/internal/store"
	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// The rules of the SSH keys of SPEC §7.16.
const (
	// SSHKeyMaxPerUser is the most keys a user has.
	SSHKeyMaxPerUser = 10
	// sshKeyNameMaxLength is the longest name of a key, in characters.
	sshKeyNameMaxLength = 40
	// sshRSAMinBits is the smallest accepted RSA key.
	sshRSAMinBits = 3072
)

// FieldPublicKey names the public key line of a new SSH key in the metadata
// "fields" of an error (the name of the key is FieldName).
const FieldPublicKey = "publicKey"

// Messages of the SSH key forms. They are fixed text; none says whose key a
// duplicate is.
const (
	messageKeyNameLength = "Use 1 to 40 characters."
	messageKeyLine       = "Paste the public key: the line of a .pub file."
	messageKeyOnlyOne    = "Paste one public key, without options."
	messageKeyType       = "This type of key is not accepted. Use ed25519, ecdsa, a security key or rsa of 3072 bits or more."
	messageKeyShort      = "RSA keys need at least 3072 bits."
	messageKeyTaken      = "This key is already registered."
	messageKeyLimit      = "You have 10 keys already. Remove one first."
)

// acceptedKeyTypes are the types of public key that SPEC §7.16 accepts. An
// ssh-rsa key must also have 3072 bits at least.
var acceptedKeyTypes = map[string]bool{
	ssh.KeyAlgoED25519:    true,
	ssh.KeyAlgoECDSA256:   true,
	ssh.KeyAlgoECDSA384:   true,
	ssh.KeyAlgoECDSA521:   true,
	ssh.KeyAlgoSKED25519:  true,
	ssh.KeyAlgoSKECDSA256: true,
	ssh.KeyAlgoRSA:        true,
}

// SSHKey is a registered key as the services show it: never the key itself,
// only what a person needs to recognise it.
type SSHKey struct {
	// ID is the ID of the key.
	ID uint64
	// Name is the name the user gave the key.
	Name string
	// Type is the type of the key, such as ssh-ed25519.
	Type string
	// Fingerprint is the SHA-256 fingerprint, as ssh-keygen -l writes it.
	Fingerprint string
	// CreatedAt is when the key was added.
	CreatedAt time.Time
	// LastUsedAt is when the key last fetched a code; nil until then.
	LastUsedAt *time.Time
}

// SSHServerInfo describes the SSH server for the pages and the API. The zero
// value means the server is off.
type SSHServerInfo struct {
	// Addr is the address the server listens on; empty when it is off.
	Addr string
	// PublicHost is the host shown in the ssh command.
	PublicHost string
	// HostFingerprint is the SHA-256 fingerprint of the host key.
	HostFingerprint string
}

// SSHServer is what a user sees of the SSH server.
type SSHServer struct {
	// Enabled is true when the server runs.
	Enabled bool
	// HostFingerprint is the SHA-256 fingerprint of the host key; empty when
	// the server is off.
	HostFingerprint string
	// Command is the ssh command that fetches a code; empty when the server
	// is off.
	Command string
}

// SSHKeys is the service of the SSH keys of a user (SPEC §7.16). A user
// manages their own keys only; adding and removing one needs the current
// password. Make it with NewSSHKeys. It is safe for concurrent use.
type SSHKeys struct {
	st       *store.Store
	clk      clock.Clock
	accounts *Accounts
	server   SSHServerInfo
}

// NewSSHKeys makes the service. The password is checked by accounts, with its
// hashing gate and its login limiter. A nil clk is the real clock. server
// describes the SSH server that the user is shown.
func NewSSHKeys(st *store.Store, clk clock.Clock, accounts *Accounts, server SSHServerInfo) *SSHKeys {
	if clk == nil {
		clk = clock.Real()
	}
	return &SSHKeys{st: st, clk: clk, accounts: accounts, server: server}
}

// Server returns what the user is shown of the SSH server, with the command
// for the user's name.
func (k *SSHKeys) Server(username string) SSHServer {
	if k.server.Addr == "" {
		return SSHServer{}
	}
	return SSHServer{
		Enabled:         true,
		HostFingerprint: k.server.HostFingerprint,
		Command:         sshCommand(k.server, username),
	}
}

// sshCommand is the ssh command that fetches a code for username.
func sshCommand(info SSHServerInfo, username string) string {
	port := "22"
	if _, p, err := net.SplitHostPort(info.Addr); err == nil && p != "" {
		port = p
	}
	var b strings.Builder
	b.WriteString("ssh ")
	if port != "22" {
		b.WriteString("-p " + port + " ")
	}
	b.WriteString(username + "@" + info.PublicHost)
	return b.String()
}

// List returns the keys of the actor, oldest first. A disabled or missing
// account fails with TagUnauthenticated.
func (k *SSHKeys) List(ctx context.Context, actor Actor) ([]SSHKey, error) {
	user, err := k.accounts.activeUser(ctx, actor)
	if err != nil {
		return nil, err
	}
	rows, err := k.st.SSHKeysOfUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	out := make([]SSHKey, len(rows))
	for i, row := range rows {
		out[i] = sshKeyOf(row)
	}
	return out, nil
}

// Add registers the public key line (the content of a .pub file) as a key of
// the actor with the name (1 to 40 characters, trimmed). The current password
// is checked first as ChangePassword checks it: a wrong one counts as a failed
// login of the username and of clientIP, and a username or address that the
// limiter blocks gets TagRateLimited. In one transaction it stores the key and
// writes the audit event user.ssh_key_added, with the name and the
// fingerprint and never the key.
//
// Failures: TagInvalidInput for a name or a line that is not valid (fields
// "name" and "publicKey") and for a wrong password (field "currentPassword");
// TagSSHKeyTypeRefused for a type outside the list of SPEC §7.16 (ssh-dss
// included); TagSSHKeyTooShort for an RSA key under 3,072 bits; TagConflict
// for a fingerprint that is registered already, whoever has it, and for a
// user who has 10 keys (both carry the message in the metadata "fields");
// TagUnauthenticated for a disabled or missing account.
func (k *SSHKeys) Add(ctx context.Context, actor Actor, name, line, password, clientIP string) (SSHKey, error) {
	name = strings.TrimSpace(name)
	problems := map[string]string{}
	if n := utf8.RuneCountInString(name); n < 1 || n > sshKeyNameMaxLength {
		problems[FieldName] = messageKeyNameLength
	}
	pub, onlyOne := parseKeyLine(line)
	switch {
	case pub == nil:
		problems[FieldPublicKey] = messageKeyLine
	case !onlyOne:
		problems[FieldPublicKey] = messageKeyOnlyOne
	}
	if len(problems) > 0 {
		return SSHKey{}, errors.New(TagInvalidInput, "the SSH key is not valid", "fields", problems)
	}
	if !acceptedKeyTypes[pub.Type()] {
		return SSHKey{}, errors.New(TagSSHKeyTypeRefused, "this type of SSH key is not accepted",
			"type", pub.Type(), "fields", map[string]string{FieldPublicKey: messageKeyType})
	}
	if pub.Type() == ssh.KeyAlgoRSA {
		if bits := rsaBits(pub); bits < sshRSAMinBits {
			return SSHKey{}, errors.New(TagSSHKeyTooShort, "the RSA key is too short",
				"bits", bits, "fields", map[string]string{FieldPublicKey: messageKeyShort})
		}
	}
	user, err := k.accounts.activeUser(ctx, actor)
	if err != nil {
		return SSHKey{}, err
	}
	allowed, match, err := k.accounts.checkPassword(ctx, user, password, clientIP)
	if err != nil {
		return SSHKey{}, err
	}
	if !allowed {
		return SSHKey{}, errors.New(TagRateLimited, "too many failed password attempts",
			"username", limiterKey(user.Username), "ip", clientIP)
	}
	if !match {
		return SSHKey{}, errors.New(TagInvalidInput, "the current password is wrong",
			"fields", map[string]string{FieldCurrentPassword: messageWrongPassword})
	}

	fingerprint := ssh.FingerprintSHA256(pub)
	row := &store.SSHKey{
		UserID:      user.ID,
		Name:        name,
		Type:        pub.Type(),
		Fingerprint: fingerprint,
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
	}
	err = k.st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.LockUser(ctx, user.ID); err != nil {
			return err
		}
		if _, err := k.accounts.activeUserOn(ctx, tx, actor); err != nil {
			return err
		}
		n, err := tx.CountSSHKeysOfUser(ctx, user.ID)
		if err != nil {
			return err
		}
		if n >= SSHKeyMaxPerUser {
			return errors.New(TagConflict, "the user has the most SSH keys already",
				"userID", user.ID, "fields", map[string]string{FieldPublicKey: messageKeyLimit})
		}
		// The check before the insert gives the usual refusal; the unique
		// index catches two requests that race.
		if _, err := tx.SSHKeyByFingerprint(ctx, fingerprint); err == nil {
			return errors.New(TagConflict, "the SSH key is registered already",
				"fields", map[string]string{FieldPublicKey: messageKeyTaken})
		} else if appErr, ok := errors.FromError(err); !ok || appErr.Tag != store.TagNotFound {
			return err
		}
		if err := tx.CreateSSHKey(ctx, row); err != nil {
			if appErr, ok := errors.FromError(err); ok && appErr.Tag == store.TagConflict {
				_ = appErr.SetMetadata("fields", map[string]string{FieldPublicKey: messageKeyTaken})
			}
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    user.ID,
			Action:     ActionUserSSHKeyAdded,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   user.ID,
			Summary:    user.DisplayName + " added the SSH key " + name + " (" + fingerprint + ")",
			Details:    map[string]any{"name": name, "fingerprint": fingerprint, "type": row.Type},
		})
	})
	if err != nil {
		return SSHKey{}, err
	}
	return sshKeyOf(*row), nil
}

// Remove deletes the key keyID of the actor, after it checks the current
// password as Add does, and writes the audit event user.ssh_key_removed. A key
// that does not exist and a key of another user both fail with TagNotFound.
// A wrong password fails with TagInvalidInput (field "currentPassword"), a
// blocked username or address with TagRateLimited, a disabled or missing
// account with TagUnauthenticated and the last key of a user whose SSH code is
// on with TagSSHCodeNeedsKey.
func (k *SSHKeys) Remove(ctx context.Context, actor Actor, keyID uint64, password, clientIP string) error {
	user, err := k.accounts.activeUser(ctx, actor)
	if err != nil {
		return err
	}
	allowed, match, err := k.accounts.checkPassword(ctx, user, password, clientIP)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New(TagRateLimited, "too many failed password attempts",
			"username", limiterKey(user.Username), "ip", clientIP)
	}
	if !match {
		return errors.New(TagInvalidInput, "the current password is wrong",
			"fields", map[string]string{FieldCurrentPassword: messageWrongPassword})
	}
	return k.st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.LockUser(ctx, user.ID); err != nil {
			return err
		}
		if _, err := k.accounts.activeUserOn(ctx, tx, actor); err != nil {
			return err
		}
		row, err := tx.SSHKeyByID(ctx, keyID)
		if err != nil {
			return err
		}
		if row.UserID != user.ID {
			return errors.New(TagNotFound, "the SSH key belongs to another user",
				"op", "removeSSHKey", "keyID", keyID)
		}
		// A user whose login asks for a code keeps a key that can fetch it:
		// the last key cannot go while the code is on.
		settings, err := tx.UserSettingsOf(ctx, user.ID)
		if err != nil {
			return err
		}
		if settings.SSHCodeRequired {
			n, err := tx.CountSSHKeysOfUser(ctx, user.ID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errors.New(TagSSHCodeNeedsKey, "the last SSH key cannot go while the SSH code is on",
					"userID", user.ID, "fields", map[string]string{FieldForm: MessageSSHCodeNeedsKey})
			}
		}
		if err := tx.DeleteSSHKey(ctx, keyID); err != nil {
			return err
		}
		return recordAudit(ctx, tx, auditEntry{
			ActorID:    user.ID,
			Action:     ActionUserSSHKeyRemoved,
			RoomID:     store.NoRoom,
			EntityType: EntityUser,
			EntityID:   user.ID,
			Summary:    user.DisplayName + " removed the SSH key " + row.Name + " (" + row.Fingerprint + ")",
			Details:    map[string]any{"name": row.Name, "fingerprint": row.Fingerprint, "type": row.Type},
		})
	})
}

// parseKeyLine reads one public key from the line of a .pub file. The key is
// nil when the line holds none; onlyOne is false when there is more than the
// key and its comment, such as options before it or a second key after it.
func parseKeyLine(line string) (pub ssh.PublicKey, onlyOne bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	pub, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, false
	}
	return pub, len(options) == 0 && strings.TrimSpace(string(rest)) == ""
}

// rsaBits returns the size in bits of the modulus of an RSA public key, 0 when
// pub is not one.
func rsaBits(pub ssh.PublicKey) int {
	cp, ok := pub.(ssh.CryptoPublicKey)
	if !ok {
		return 0
	}
	key, ok := cp.CryptoPublicKey().(*rsa.PublicKey)
	if !ok {
		return 0
	}
	return key.N.BitLen()
}

// sshKeyOf is the view of a stored key.
func sshKeyOf(row store.SSHKey) SSHKey {
	return SSHKey{
		ID: row.ID, Name: row.Name, Type: row.Type, Fingerprint: row.Fingerprint,
		CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt,
	}
}

const (
	// TagSSHKeyTypeRefused is the tag of a public key whose type is not on
	// the list of SPEC §7.16, ssh-dss included. The handlers answer 422; the
	// metadata "fields" has the message for the field "publicKey".
	TagSSHKeyTypeRefused = "SSHKeyTypeRefused"
	// TagSSHKeyTooShort is the tag of an RSA public key of less than 3,072
	// bits. The handlers answer 422; the metadata "fields" has the message
	// for the field "publicKey".
	TagSSHKeyTooShort = "SSHKeyTooShort"
)
