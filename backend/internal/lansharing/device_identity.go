package lansharing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"infinite-canvas/backend/internal/localcrypto"
)

func hardwareNodeID(value string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || id == uuid.Nil || id == uuid.Max {
		return "", errors.New("SMBIOS system UUID is missing or invalid")
	}
	digest := sha256.Sum256([]byte(id.String()))
	return hex.EncodeToString(digest[:]), nil
}

func loadDeviceIdentityAt(dataDir, identityDir, systemUUID string) (*Identity, error) {
	nodeID, err := hardwareNodeID(systemUUID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(identityDir, identityFileName)
	encoded, err := os.ReadFile(path)
	if err == nil {
		var stored identityFile
		if json.Unmarshal(encoded, &stored) != nil || stored.Version != 2 || !validNodeID(stored.NodeID) {
			return nil, errors.New("device identity file is invalid")
		}
		ciphertext, err := base64.RawStdEncoding.DecodeString(stored.EncryptedPrivateKeyB64)
		if err != nil {
			return nil, errors.New("device identity encoding is invalid")
		}
		plaintext, err := localcrypto.Decrypt(identityDir, string(ciphertext))
		if err != nil {
			return nil, fmt.Errorf("decrypt device identity: %w", err)
		}
		key, err := base64.RawStdEncoding.DecodeString(plaintext)
		if err != nil || len(key) != ed25519.PrivateKeySize {
			return nil, errors.New("device identity key is invalid")
		}
		return &Identity{nodeID: nodeID, privateKey: ed25519.PrivateKey(key)}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Preserve the signing key during the move out of an existing workspace.
	// The new encryption key lives beside the identity, independently of dataDir.
	var key ed25519.PrivateKey
	if _, err := os.Stat(filepath.Join(dataDir, "lan-sharing", identityFileName)); err == nil {
		legacy, err := LoadIdentity(dataDir)
		if err != nil {
			return nil, err
		}
		key = legacy.privateKey
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
	}
	sealed, err := localcrypto.Encrypt(identityDir, base64.RawStdEncoding.EncodeToString(key))
	if err != nil {
		return nil, err
	}
	encoded, err = json.Marshal(identityFile{Version: 2, NodeID: nodeID, EncryptedPrivateKeyB64: base64.RawStdEncoding.EncodeToString([]byte(sealed))})
	if err != nil {
		return nil, err
	}
	if err := writeIdentityFile(path, encoded); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadDeviceIdentityAt(dataDir, identityDir, systemUUID)
		}
		return nil, err
	}
	return &Identity{nodeID: nodeID, privateKey: key, keyFresh: true}, nil
}

// RawSMBIOSData starts with an 8-byte header, followed by variable-length
// records and double-NUL-terminated string sections (Microsoft RSMB format).
func systemUUIDFromSMBIOS(raw []byte) (string, error) {
	if len(raw) < 8 || uint64(binary.LittleEndian.Uint32(raw[4:8])) != uint64(len(raw)-8) {
		return "", errors.New("SMBIOS table header is invalid")
	}
	modern := raw[1] > 2 || (raw[1] == 2 && raw[2] >= 6)
	for table := raw[8:]; len(table) >= 4; {
		length := int(table[1])
		if length < 4 || length > len(table) {
			break
		}
		if table[0] == 1 && length >= 24 {
			var id uuid.UUID
			copy(id[:], table[8:24])
			if modern {
				id[0], id[1], id[2], id[3] = id[3], id[2], id[1], id[0]
				id[4], id[5] = id[5], id[4]
				id[6], id[7] = id[7], id[6]
			}
			if _, err := hardwareNodeID(id.String()); err != nil {
				return "", err
			}
			return id.String(), nil
		}
		if table[0] == 127 {
			break
		}
		end := length
		for end+1 < len(table) && (table[end] != 0 || table[end+1] != 0) {
			end++
		}
		if end+1 >= len(table) {
			break
		}
		table = table[end+2:]
	}
	return "", errors.New("SMBIOS system UUID was not found")
}
