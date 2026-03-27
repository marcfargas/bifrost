package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// AttachmentManager handles file storage for task attachments.
type AttachmentManager struct {
	store       store.Store
	dataDir     string
	maxFileSize int64
}

// NewAttachmentManager creates an AttachmentManager. maxFileSizeStr is a
// human-readable size like "10MB" or "500KB"; pass "" or "0" for no limit.
func NewAttachmentManager(s store.Store, dataDir, maxFileSizeStr string) (*AttachmentManager, error) {
	var maxSize int64
	if maxFileSizeStr != "" && maxFileSizeStr != "0" {
		sz, err := parseSize(maxFileSizeStr)
		if err != nil {
			return nil, fmt.Errorf("attachments: parse max size %q: %w", maxFileSizeStr, err)
		}
		maxSize = sz
	}
	return &AttachmentManager{
		store:       s,
		dataDir:     dataDir,
		maxFileSize: maxSize,
	}, nil
}

// StorePath returns the directory used to store files for an attachment.
func (m *AttachmentManager) StorePath(attachmentID string) string {
	return filepath.Join(m.dataDir, "attachments", attachmentID)
}

// FilePath returns the full path for an attachment's file.
func (m *AttachmentManager) FilePath(attachmentID, filename string) string {
	return filepath.Join(m.StorePath(attachmentID), filename)
}

// Store copies srcPath into hub storage, detects content type, saves metadata,
// and returns the new Attachment record.
func (m *AttachmentManager) Store(ctx context.Context, taskID, uploadedBy, srcPath string) (*protocol.Attachment, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("attachments: stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachments: source is not a regular file: %s", srcPath)
	}

	size := info.Size()
	if m.maxFileSize > 0 && size > m.maxFileSize {
		return nil, fmt.Errorf("attachments: file size %d exceeds limit %d", size, m.maxFileSize)
	}

	ct, err := detectContentType(srcPath)
	if err != nil {
		return nil, fmt.Errorf("attachments: detect content type: %w", err)
	}

	attID := uuid.New().String()
	filename := filepath.Base(srcPath)
	destDir := m.StorePath(attID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("attachments: mkdir: %w", err)
	}

	destPath := m.FilePath(attID, filename)
	if err := copyFile(srcPath, destPath); err != nil {
		return nil, fmt.Errorf("attachments: copy file: %w", err)
	}

	att := &protocol.Attachment{
		AttachmentID: attID,
		TaskID:       taskID,
		Filename:     filename,
		ContentType:  ct,
		Size:         size,
		UploadedBy:   uploadedBy,
		UploadedAt:   time.Now(),
	}
	if err := m.store.SaveAttachment(ctx, att); err != nil {
		return nil, fmt.Errorf("attachments: save metadata: %w", err)
	}

	return att, nil
}

// Retrieve copies the attachment file to destDir and returns the destination path.
func (m *AttachmentManager) Retrieve(ctx context.Context, attachmentID, destDir string) (string, error) {
	att, err := m.store.GetAttachment(ctx, attachmentID)
	if err != nil {
		return "", fmt.Errorf("attachments: get metadata: %w", err)
	}
	if att == nil {
		return "", fmt.Errorf("attachments: not found: %s", attachmentID)
	}

	srcPath := m.FilePath(attachmentID, att.Filename)
	destPath := filepath.Join(destDir, att.Filename)

	if err := copyFile(srcPath, destPath); err != nil {
		return "", fmt.Errorf("attachments: retrieve copy: %w", err)
	}

	return destPath, nil
}

// Delete removes an attachment's files from disk. Metadata is left in the store
// for audit purposes; callers that need full deletion should also call
// store.DeleteAttachmentsBefore.
func (m *AttachmentManager) Delete(_ context.Context, attachmentID string) error {
	dir := m.StorePath(attachmentID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("attachments: delete files: %w", err)
	}
	return nil
}

// copyFile copies src to dst, creating dst if needed.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// detectContentType sniffs the first 512 bytes of the file to determine MIME type.
func detectContentType(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "application/octet-stream", nil
	}

	return http.DetectContentType(buf[:n]), nil
}

// parseSize parses a human-readable size string like "10MB", "500KB", "1GB".
// Supported suffixes: B, KB, MB, GB (case-insensitive).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)

	multipliers := []struct {
		suffix string
		factor int64
	}{
		{"GB", 1 << 30},
		{"MB", 1 << 20},
		{"KB", 1 << 10},
		{"B", 1},
	}

	for _, m := range multipliers {
		if numStr, ok := strings.CutSuffix(upper, m.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(numStr), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			return n * m.factor, nil
		}
	}

	// No suffix — treat as bytes.
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	return n, nil
}
