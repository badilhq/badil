package config_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/badilhq/badil/internal/config"
)

const (
	commitName  = "badil-gitops"
	commitEmail = "badil-gitops@users.noreply.github.com"
)

// newKey returns a new OpenPGP entity of name <email>.
func newKey(t *testing.T, email string) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity(commitName, "", email, &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// armored returns the ASCII armor of blockType around what write writes.
func armored(t *testing.T, blockType string, write func(w io.Writer) error) string {
	t.Helper()
	var plain, out bytes.Buffer
	if err := write(&plain); err != nil {
		t.Fatal(err)
	}
	w, err := armor.Encode(&out, blockType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(plain.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// privateKey returns the armored private keys of entities.
func privateKey(t *testing.T, entities ...*openpgp.Entity) string {
	t.Helper()
	return armored(t, openpgp.PrivateKeyType, func(w io.Writer) error {
		for _, e := range entities {
			if err := e.SerializePrivate(w, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// signingEnv returns a valid env with the commit identity and key set.
func signingEnv(name, email, key string) map[string]string {
	env := validEnv()
	env["GITOPS_COMMIT_NAME"] = name
	env["GITOPS_COMMIT_EMAIL"] = email
	env["GITOPS_SIGNING_KEY"] = key
	return env
}

func TestLoad_CommitIdentityAndSigningKey(t *testing.T) {
	key := newKey(t, commitEmail)
	cases := []struct {
		name, commitName, commitEmail, key string
		wantKey                            bool
	}{
		{name: "none"},
		{name: "identity", commitName: commitName, commitEmail: commitEmail},
		{
			name: "identity and key", commitName: commitName, commitEmail: commitEmail,
			key: privateKey(t, key), wantKey: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, signingEnv(tc.commitName, tc.commitEmail, tc.key))
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.GitOpsCommitName != tc.commitName || cfg.GitOpsCommitEmail != tc.commitEmail {
				t.Errorf("GitOpsCommitName, GitOpsCommitEmail = %q, %q; want %q, %q",
					cfg.GitOpsCommitName, cfg.GitOpsCommitEmail, tc.commitName, tc.commitEmail)
			}
			switch {
			case !tc.wantKey && cfg.GitOpsSigningKey != nil:
				t.Error("GitOpsSigningKey set, want nil")
			case tc.wantKey && (cfg.GitOpsSigningKey == nil ||
				cfg.GitOpsSigningKey.PrimaryKey.KeyId != key.PrimaryKey.KeyId):
				t.Errorf("GitOpsSigningKey = %v, want the key %X", cfg.GitOpsSigningKey, key.PrimaryKey.KeyId)
			}
		})
	}
}

func TestLoad_InvalidCommitIdentityOrSigningKey(t *testing.T) {
	key := newKey(t, commitEmail)
	public := armored(t, openpgp.PublicKeyType, key.Serialize)
	encrypted := newKey(t, commitEmail)
	if err := encrypted.EncryptPrivateKeys([]byte("passphrase"), nil); err != nil {
		t.Fatal(err)
	}
	encryptedKey := armored(t, openpgp.PrivateKeyType, func(w io.Writer) error {
		return encrypted.SerializePrivateWithoutSigning(w, nil)
	})
	noSign := newKey(t, commitEmail)
	for _, id := range noSign.Identities {
		id.SelfSignature.FlagSign = false
	}
	// Every case with a key sets the commit identity, unless it says noID.
	cases := []struct {
		name, commitName, commitEmail, key string
		noID                               bool
		want                               string
	}{
		{
			name: "name without email", commitName: commitName,
			want: "GITOPS_COMMIT_NAME and GITOPS_COMMIT_EMAIL must be set together",
		},
		{
			name: "email without name", commitEmail: commitEmail,
			want: "GITOPS_COMMIT_NAME and GITOPS_COMMIT_EMAIL must be set together",
		},
		{
			name: "key without identity", key: privateKey(t, key), noID: true,
			want: "GITOPS_SIGNING_KEY requires GITOPS_COMMIT_NAME and GITOPS_COMMIT_EMAIL",
		},
		{
			name: "not a key", key: "not a key",
			want: "GITOPS_SIGNING_KEY: openpgp: invalid argument: no armored data found",
		},
		{name: "public key", key: public, want: "GITOPS_SIGNING_KEY: no private key that can sign"},
		{
			name: "key that cannot sign", key: privateKey(t, noSign),
			want: "GITOPS_SIGNING_KEY: no private key that can sign",
		},
		{
			name: "two keys", key: privateKey(t, key, newKey(t, commitEmail)),
			want: "GITOPS_SIGNING_KEY: 2 private keys that can sign, want exactly one",
		},
		{
			name: "passphrase", key: encryptedKey,
			want: "GITOPS_SIGNING_KEY is passphrase-protected: badil needs a key without passphrase",
		},
		{
			name: "email not an identity", key: privateKey(t, newKey(t, "other@example.com")),
			want: "GITOPS_COMMIT_EMAIL " + commitEmail + " is not an identity of GITOPS_SIGNING_KEY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, email := tc.commitName, tc.commitEmail
			if tc.key != "" && !tc.noID {
				name, email = commitName, commitEmail
			}
			setEnv(t, signingEnv(name, email, tc.key))
			_, err := config.Load()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
