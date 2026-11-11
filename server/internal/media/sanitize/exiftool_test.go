package sanitize_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

func testRunner(t *testing.T) *sanitize.Runner {
	t.Helper()
	tools := sanitizetest.LookTools(t)
	r := &sanitize.Runner{Exiftool: tools.Exiftool, TmpDir: t.TempDir(), Workers: 1}
	t.Cleanup(r.Close)
	return r
}

func TestExifOptionsDontCarryOver(t *testing.T) {
	t.Parallel()
	r := testRunner(t)
	// The sample has maker notes, which -fast2 skips.
	sample, err := filepath.Abs(filepath.Join("testdata", "exif-py", "jpg", "Canon_DIGITAL_IXUS_400.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sample); err != nil {
		t.Fatalf("exif-py samples missing: run `just fetch-samples`")
	}
	fast, err := r.Exif(context.Background(), "-fast2", "-j", "-G1", sample)
	if err != nil {
		t.Fatal(err)
	}
	full, err := r.Exif(context.Background(), "-j", "-G1", sample)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fast), `"Canon:`) {
		t.Fatal("-fast2 read maker notes; the sample no longer tests anything")
	}
	if !strings.Contains(string(full), `"Canon:`) {
		t.Fatal("-fast2 carried over to the next command on the same worker")
	}
}

func TestExifStatus(t *testing.T) {
	t.Parallel()
	r := testRunner(t)
	missing := filepath.Join(r.TmpDir, "missing.jpg")
	_, err := r.Exif(context.Background(), "-j", missing)
	var te sanitize.ToolError
	if !errors.As(err, &te) || te.Kind != sanitize.ToolCrash || !strings.Contains(err.Error(), "exit status 1: Error: File not found") {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := r.Exif(context.Background(), "-j", sanitizetest.Clean(t, "clean.jpg")); err != nil {
		t.Fatalf("worker unusable after a failed command: %v", err)
	}
}

func TestExifCancelled(t *testing.T) {
	t.Parallel()
	r := testRunner(t)
	sample := sanitizetest.Clean(t, "clean.jpg")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Exif(ctx, "-j", sample)
	var te sanitize.ToolError
	if !errors.As(err, &te) || te.Kind != sanitize.ToolTimeout {
		t.Fatalf("cancelled: %v", err)
	}
	if _, err := r.Exif(context.Background(), "-j", sample); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestExifBadArgument(t *testing.T) {
	t.Parallel()
	r := testRunner(t)
	for _, arg := range []string{"", "a\nb", "#x", " -all="} {
		if _, err := r.Exif(context.Background(), arg); err == nil {
			t.Errorf("%q accepted", arg)
		}
	}
}
