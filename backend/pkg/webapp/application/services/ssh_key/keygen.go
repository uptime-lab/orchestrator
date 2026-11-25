package ssh_key

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// generateEd25519Keypair creates a fresh ed25519 keypair and returns the public
// key in authorized_keys format (single line, with the given comment) and the
// private key in OpenSSH PEM format. ed25519 is small, fast and modern — a good
// default for a machine-to-machine connection key.
func generateEd25519Keypair(comment string) (publicAuthorized string, privatePEM string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key: %w", err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key: %w", err)
	}
	// MarshalAuthorizedKey appends a trailing newline; keep a single trimmed line
	// plus the comment so it matches the uploaded-key format the validator expects.
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		authorized = authorized + " " + comment
	}

	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}
	privatePEM = string(pem.EncodeToMemory(block))

	return authorized, privatePEM, nil
}
