package lansharing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrTransferTooLarge     = errors.New("shared asset exceeds the transfer limit")
	ErrTransferSizeMismatch = errors.New("shared asset size mismatch")
	ErrTransferHashMismatch = errors.New("shared asset SHA-256 mismatch")
)

func CopyVerified(ctx context.Context, destination io.Writer, source io.Reader, expectedSize int64, expectedSHA256 string, maxBytes int64) error {
	if destination == nil || source == nil || expectedSize < 0 || maxBytes < 0 {
		return errors.New("shared asset transfer arguments are invalid")
	}
	if expectedSize > maxBytes {
		return ErrTransferTooLarge
	}
	expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
	if len(expectedSHA256) != sha256.Size*2 {
		return errors.New("shared asset SHA-256 is invalid")
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return errors.New("shared asset SHA-256 is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	hash := sha256.New()
	reader := contextReader{ctx: ctx, source: source}
	written, err := io.CopyN(io.MultiWriter(destination, hash), reader, expectedSize)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ErrTransferSizeMismatch
		}
		return fmt.Errorf("read shared asset body: %w", err)
	}
	if written != expectedSize {
		return ErrTransferSizeMismatch
	}
	var extra [1]byte
	n, readErr := reader.Read(extra[:])
	if n != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("check shared asset body length: %w", readErr)
		}
		return ErrTransferSizeMismatch
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA256) {
		return ErrTransferHashMismatch
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}
