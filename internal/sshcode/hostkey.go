package sshcode

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rm4n0s/errors"
	"golang.org/x/crypto/ssh"
)

// hostKeyMode is the permission of the host key file.
const hostKeyMode = 0o600

// LoadHostKey returns the host key kept at path: the PEM of an OpenSSH private
// key. When the file does not exist it makes an Ed25519 key and stores it
// there, so that the next start, and the other processes that share the
// directory, use the same key.
//
// The key is made exclusively. It is written to a temporary file in the same
// directory with mode 0600 and then linked to path, and the temporary file is
// removed. A reader therefore never sees a half-written key, and of two
// processes that start together one link wins and the other reads the winner's
// key.
//
// It fails with tagHostKeyReadFailed when the file cannot be read,
// tagHostKeyInvalid when it is not a private key, and the tags of the making
// when the key cannot be made or stored (a missing directory among them).
func LoadHostKey(path string) (ssh.Signer, error) {
	signer, err := readHostKey(path)
	if err == nil {
		return signer, nil
	}
	if appErr, ok := errors.FromError(err); !ok || appErr.Tag != tagHostKeyMissing {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, errors.NewErr(tagHostKeyGenerateFailed, err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, errors.NewErr(tagHostKeyGenerateFailed, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ssh_host_key-*")
	if err != nil {
		return nil, errors.NewErr(tagHostKeyTempFailed, err).SetMetadata("path", path)
	}
	defer func() {
		// The temporary file is removed on every path. The link keeps the key.
		_ = os.Remove(tmp.Name())
	}()
	if err := tmp.Chmod(hostKeyMode); err != nil {
		_ = tmp.Close()
		return nil, errors.NewErr(tagHostKeyWriteFailed, err).SetMetadata("path", path)
	}
	if _, err := tmp.Write(pem.EncodeToMemory(block)); err != nil {
		_ = tmp.Close()
		return nil, errors.NewErr(tagHostKeyWriteFailed, err).SetMetadata("path", path)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, errors.NewErr(tagHostKeyWriteFailed, err).SetMetadata("path", path)
	}
	if err := tmp.Close(); err != nil {
		return nil, errors.NewErr(tagHostKeyWriteFailed, err).SetMetadata("path", path)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, errors.NewErr(tagHostKeyLinkFailed, err).SetMetadata("path", path)
		}
		// Another process made the key first: read its key.
		return readHostKey(path)
	}
	return ssh.NewSignerFromKey(priv)
}

// readHostKey reads and parses the host key at path. A file that does not
// exist fails with tagHostKeyMissing, which LoadHostKey handles.
func readHostKey(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errors.NewErr(tagHostKeyMissing, err).SetMetadata("path", path)
		}
		return nil, errors.NewErr(tagHostKeyReadFailed, err).SetMetadata("path", path)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, errors.NewErr(tagHostKeyInvalid, err).SetMetadata("path", path)
	}
	return signer, nil
}

const (
	tagHostKeyMissing        = "HostKeyMissing"
	tagHostKeyReadFailed     = "HostKeyReadFailed"
	tagHostKeyInvalid        = "HostKeyInvalid"
	tagHostKeyGenerateFailed = "HostKeyGenerateFailed"
	tagHostKeyTempFailed     = "HostKeyTempFailed"
	tagHostKeyWriteFailed    = "HostKeyWriteFailed"
	tagHostKeyLinkFailed     = "HostKeyLinkFailed"
)
