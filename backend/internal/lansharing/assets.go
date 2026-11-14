package lansharing

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const peerAssetMetadataHeader = "X-BeefTV-Asset-Metadata"

type PeerAssetHTTPError struct {
	StatusCode int
}

func (err *PeerAssetHTTPError) Error() string {
	return fmt.Sprintf("LAN asset peer returned HTTP %d", err.StatusCode)
}

// AssetMetadata is the immutable identity and descriptive data exchanged with
// peers. The content hash and size are verified against the streamed bytes.
type AssetMetadata struct {
	ContentHash string `json:"contentHash"`
	Size        int64  `json:"size"`
	Name        string `json:"name"`
	MIMEType    string `json:"mimeType"`
	Kind        string `json:"kind"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
}

// DownloadPeerAsset fetches one complete asset from an explicitly trusted
// peer. The endpoint is constrained to a private IPv4 address and the TLS
// certificate is pinned to peerID before any asset bytes are accepted.
func (m *Manager) DownloadPeerAsset(ctx context.Context, peerID, address string, port int, catalogID, contentHash string, destination io.Writer, maxBytes int64) (AssetMetadata, error) {
	if m == nil || destination == nil || ctx == nil || maxBytes < 1 {
		return AssetMetadata{}, ErrInvalid
	}
	peerID = strings.ToLower(strings.TrimSpace(peerID))
	addressIP := net.ParseIP(strings.TrimSpace(address))
	catalogID = strings.TrimSpace(catalogID)
	contentHash = strings.ToLower(strings.TrimSpace(contentHash))
	if !validNodeID(peerID) || addressIP == nil || addressIP.To4() == nil || !isPrivateLANAddress(addressIP) || port < 1 || port > 65535 || !validAssetCatalogID(catalogID) || !validAssetHash(contentHash) {
		return AssetMetadata{}, ErrInvalid
	}
	if m.db == nil || !m.isTrusted(peerID) {
		return AssetMetadata{}, ErrPeerNotTrusted
	}
	if !m.catalogPeerAllowed(ctx, catalogID, peerID) {
		return AssetMetadata{}, ErrPeerNotTrusted
	}
	if m.identity == nil {
		return AssetMetadata{}, errors.New("LAN sharing identity is not initialized")
	}
	clientTLS, err := m.clientTLSConfig(peerID)
	if err != nil {
		return AssetMetadata{}, err
	}
	transport := &http.Transport{
		Proxy: nil, TLSClientConfig: clientTLS, ForceAttemptHTTP2: false,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	endpoint := url.URL{
		Scheme: "https",
		Host:   net.JoinHostPort(addressIP.To4().String(), strconv.Itoa(port)),
		Path:   "/catalogs/" + catalogID + "/assets/" + contentHash,
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return AssetMetadata{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return AssetMetadata{}, fmt.Errorf("connect to LAN asset peer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return AssetMetadata{}, &PeerAssetHTTPError{StatusCode: response.StatusCode}
	}
	metadata, err := decodePeerAssetMetadata(response.Header.Get(peerAssetMetadataHeader))
	if err != nil {
		return AssetMetadata{}, err
	}
	length := response.ContentLength
	if length < 1 || length != metadata.Size || length > maxBytes || metadata.ContentHash != contentHash || response.Header.Get("X-BeefTV-Content-SHA256") != contentHash {
		return AssetMetadata{}, ErrTransferSizeMismatch
	}
	if err := CopyVerified(ctx, destination, response.Body, length, contentHash, maxBytes); err != nil {
		return AssetMetadata{}, err
	}
	return metadata, nil
}

func encodePeerAssetMetadata(metadata AssetMetadata) (string, error) {
	if err := validatePeerAssetMetadata(metadata); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

// EncodeAssetMetadata encodes the validated metadata header used by peer asset
// responses.
func EncodeAssetMetadata(metadata AssetMetadata) (string, error) {
	return encodePeerAssetMetadata(metadata)
}

func decodePeerAssetMetadata(raw string) (AssetMetadata, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(encoded) == 0 || len(encoded) > 4096 {
		return AssetMetadata{}, errors.New("LAN asset metadata is invalid")
	}
	var metadata AssetMetadata
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return AssetMetadata{}, errors.New("LAN asset metadata is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AssetMetadata{}, errors.New("LAN asset metadata is invalid")
	}
	if err := validatePeerAssetMetadata(metadata); err != nil {
		return AssetMetadata{}, err
	}
	return metadata, nil
}

func validatePeerAssetMetadata(metadata AssetMetadata) error {
	if !validAssetHash(metadata.ContentHash) || metadata.Size < 1 || metadata.Name == "" || len(metadata.Name) > 255 || strings.ContainsAny(metadata.Name, "/\\\r\n\x00") || metadata.Name == "." || metadata.Name == ".." {
		return errors.New("LAN asset metadata is invalid")
	}
	for _, char := range metadata.Name {
		if char < 0x20 || char == 0x7f {
			return errors.New("LAN asset metadata is invalid")
		}
	}
	mediaType, _, err := mime.ParseMediaType(metadata.MIMEType)
	if err != nil || mediaType == "" || len(metadata.MIMEType) > 120 {
		return errors.New("LAN asset MIME type is invalid")
	}
	if metadata.Kind != "image" && metadata.Kind != "video" && metadata.Kind != "audio" && metadata.Kind != "file" {
		return errors.New("LAN asset kind is invalid")
	}
	if metadata.Width < 0 || metadata.Height < 0 || metadata.DurationMs < 0 {
		return errors.New("LAN asset dimensions are invalid")
	}
	return nil
}

func validAssetHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validAssetCatalogID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}
