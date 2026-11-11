package sanitize

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Verified is a file that passed the verifier. It has no exported fields
// and only this package constructs it (Process, Reverify), so code holding a
// Verified holds bytes that were checked.
type Verified struct {
	path      string
	container Container
	size      int64
}

func (v Verified) Path() string { return v.path }

func (v Verified) Container() Container { return v.container }

func (v Verified) ContentType() string { return v.container.ContentType() }

func (v Verified) Ext() string { return v.container.Ext() }

func (v Verified) Size() int64 { return v.size }

func (v Verified) IsZero() bool { return v.path == "" }

func (v Verified) Open() (*os.File, error) {
	if v.IsZero() {
		return nil, errors.New("sanitize: open of zero Verified")
	}
	return os.Open(v.path)
}

// MoveTo renames the file to dst (same filesystem) and returns the handle
// for its new location. The bytes are unchanged, so it stays verified.
func (v Verified) MoveTo(dst string) (Verified, error) {
	if v.IsZero() {
		return Verified{}, errors.New("sanitize: move of zero Verified")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return Verified{}, err
	}
	if err := syncFile(v.path); err != nil {
		return Verified{}, err
	}
	if err := os.Rename(v.path, dst); err != nil {
		return Verified{}, fmt.Errorf("sanitize: commit: %w", err)
	}
	return Verified{path: dst, container: v.container, size: v.size}, nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
