package store_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/badilhq/badil/internal/lifecycle"
	"github.com/badilhq/badil/internal/store"
	"github.com/badilhq/badil/internal/store/storetest"
)

// TestApply_SignedCommit checks that with a signing key, the commit Apply
// pushes carries a signature the key's public part verifies, and is authored
// and committed by the store's identity.
func TestApply_SignedCommit(t *testing.T) {
	id := store.CommitIdentity{Name: "badil-gitops", Email: "badil-gitops@users.noreply.github.com"}
	key, err := openpgp.NewEntity(id.Name, "", id.Email, &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var public bytes.Buffer
	w, err := armor.Encode(&public, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = key.Serialize(w); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	remote := newRemote(t, applyFiles)
	st, err := store.Open(context.Background(), store.CloneConfig{URL: remote.URL()}, id,
		store.WithRepoURL(storetest.GitopsURL), store.WithSigningKey(key))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err = apply(st, lifecycle.OpSync, rename("a", "renamed", "chore(preview): sync a")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	head := remote.Commits()[0]
	if head.Subject() != "chore(preview): sync a" {
		t.Fatalf("head subject = %q, want Apply's commit", head.Subject())
	}
	signer, err := head.Object.Verify(public.String())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if signer.PrimaryKey.KeyId != key.PrimaryKey.KeyId {
		t.Errorf("signer = %X, want %X", signer.PrimaryKey.KeyId, key.PrimaryKey.KeyId)
	}
	for _, sig := range []struct{ name, email string }{
		{head.Author.Name, head.Author.Email}, {head.Committer.Name, head.Committer.Email},
	} {
		if sig.name != id.Name || sig.email != id.Email {
			t.Errorf("signature = %s <%s>, want %s <%s>", sig.name, sig.email, id.Name, id.Email)
		}
	}
}
