package github_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/badilhq/badil/internal/github"
)

func generateTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	encodeErr := pem.Encode(&buf, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	return buf.Bytes()
}

func TestNewApp(t *testing.T) {
	t.Run("valid key", func(t *testing.T) {
		app, err := github.NewApp(github.AppConfig{
			AppID:         12345,
			PrivateKey:    generateTestKey(t),
			WebhookSecret: "test-secret",
		})
		if err != nil {
			t.Fatalf("NewApp() error = %v", err)
		}
		if app == nil {
			t.Fatal("NewApp() returned nil")
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		_, err := github.NewApp(github.AppConfig{
			AppID:         12345,
			PrivateKey:    []byte("not-a-pem-key"),
			WebhookSecret: "test-secret",
		})
		if err == nil {
			t.Fatal("NewApp() expected error for invalid key")
		}
	})
}
