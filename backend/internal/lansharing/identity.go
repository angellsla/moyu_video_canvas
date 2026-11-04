package lansharing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"infinite-canvas/backend/internal/localcrypto"
)

const identityFileName = "identity.json"

type Identity struct {
	privateKey ed25519.PrivateKey
}

type identityFile struct {
	Version                int    `json:"version"`
	EncryptedPrivateKeyB64 string `json:"encryptedPrivateKey"`
}

func LoadIdentity(dataDir string) (*Identity, error) {
	dir := filepath.Join(strings.TrimSpace(dataDir), "lan-sharing")
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("LAN sharing data directory is empty")
	}
	path := filepath.Join(dir, identityFileName)
	encoded, err := os.ReadFile(path)
	if err == nil {
		var stored identityFile
		if err := json.Unmarshal(encoded, &stored); err != nil || stored.Version != 1 || stored.EncryptedPrivateKeyB64 == "" {
			return nil, errors.New("LAN sharing identity file is invalid")
		}
		ciphertext, err := base64.RawStdEncoding.DecodeString(stored.EncryptedPrivateKeyB64)
		if err != nil {
			return nil, errors.New("LAN sharing identity encoding is invalid")
		}
		plaintext, err := localcrypto.Decrypt(dataDir, string(ciphertext))
		if err != nil {
			return nil, err
		}
		privateKey, err := base64.RawStdEncoding.DecodeString(plaintext)
		if err != nil || len(privateKey) != ed25519.PrivateKeySize {
			return nil, errors.New("LAN sharing identity key is invalid")
		}
		return &Identity{privateKey: ed25519.PrivateKey(privateKey)}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sealed, err := localcrypto.Encrypt(dataDir, base64.RawStdEncoding.EncodeToString(privateKey))
	if err != nil {
		return nil, err
	}
	encoded, err = json.Marshal(identityFile{Version: 1, EncryptedPrivateKeyB64: base64.RawStdEncoding.EncodeToString([]byte(sealed))})
	if err != nil {
		return nil, err
	}
	if err := writeIdentityFile(path, encoded); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		return LoadIdentity(dataDir)
	}
	return &Identity{privateKey: privateKey}, nil
}

func writeIdentityFile(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func (i *Identity) ID() string {
	if i == nil || len(i.privateKey) != ed25519.PrivateKeySize {
		return ""
	}
	return Fingerprint(i.privateKey.Public().(ed25519.PublicKey))
}

func (i *Identity) PublicKey() ed25519.PublicKey {
	if i == nil || len(i.privateKey) != ed25519.PrivateKeySize {
		return nil
	}
	publicKey := i.privateKey.Public().(ed25519.PublicKey)
	return append(ed25519.PublicKey(nil), publicKey...)
}

func Fingerprint(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])
}

func (i *Identity) TLSCertificate() (tls.Certificate, error) {
	if i == nil || len(i.privateKey) != ed25519.PrivateKeySize {
		return tls.Certificate{}, errors.New("LAN sharing identity is not initialized")
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	publicKey := i.privateKey.Public().(ed25519.PublicKey)
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: i.ID()},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey, i.privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(i.privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}),
	)
}

func (i *Identity) ClientTLSConfig(expectedPeerID string) (*tls.Config, error) {
	certificate, err := i.TLSCertificate()
	if err != nil {
		return nil, err
	}
	expectedPeerID = strings.TrimSpace(expectedPeerID)
	if expectedPeerID == "" {
		return nil, errors.New("expected peer identity is required")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{certificate},
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("peer certificate count is invalid")
			}
			publicKey, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok || Fingerprint(publicKey) != expectedPeerID {
				return errors.New("peer identity fingerprint mismatch")
			}
			return nil
		},
	}, nil
}

func (i *Identity) ServerTLSConfig(isTrusted func(string) bool) (*tls.Config, error) {
	certificate, err := i.TLSCertificate()
	if err != nil {
		return nil, err
	}
	if isTrusted == nil {
		return nil, errors.New("peer trust verifier is required")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("peer certificate count is invalid")
			}
			publicKey, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok || !isTrusted(Fingerprint(publicKey)) {
				return errors.New("peer identity is not trusted")
			}
			return nil
		},
	}, nil
}
