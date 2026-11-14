package lansharing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadIdentityPersistsStableFingerprint(t *testing.T) {
	dataDir := t.TempDir()
	first, err := LoadIdentity(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadIdentity(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == "" || first.ID() != second.ID() {
		t.Fatalf("identity fingerprint changed across load: first=%q second=%q", first.ID(), second.ID())
	}
	if len(first.PublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("public key length = %d", len(first.PublicKey()))
	}
}

func TestPinnedMutualTLSRequiresTrustedEd25519Peer(t *testing.T) {
	serverIdentity, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clientIdentity, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := serverIdentity.ServerTLSConfig(func(peerID string, key ed25519.PublicKey) bool {
		return peerID == clientIdentity.ID() && key.Equal(clientIdentity.PublicKey())
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()

	clientTLS, err := clientIdentity.ClientTLSConfig(serverIdentity.ID(), Fingerprint(serverIdentity.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "ok" {
		t.Fatalf("trusted request body=%q err=%v", body, err)
	}

	untrustedIdentity, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	untrustedTLS, err := untrustedIdentity.ClientTLSConfig(serverIdentity.ID(), Fingerprint(serverIdentity.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: untrustedTLS}}
	if _, err := untrusted.Get(server.URL); err == nil {
		t.Fatal("untrusted client certificate was accepted")
	}
	if _, err := clientIdentity.ClientTLSConfig(strings.Repeat("0", sha256.Size*2), Fingerprint(serverIdentity.PublicKey())); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintIsSHA256OfEd25519PublicKey(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(publicKey)
	if got := Fingerprint(publicKey); got != hex.EncodeToString(want[:]) {
		t.Fatalf("fingerprint = %q", got)
	}
}
