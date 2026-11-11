package sanitize_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

func newRunner(t *testing.T) *sanitize.Runner {
	t.Helper()
	tools := sanitizetest.LookTools(t)
	return &sanitize.Runner{Exiftool: tools.Exiftool, FFmpeg: tools.FFmpeg, TmpDir: t.TempDir()}
}

func TestCorpus(t *testing.T) {
	t.Parallel()
	tools := sanitizetest.LookTools(t)
	for _, f := range sanitizetest.Corpus(t) {
		t.Run(f.Name, func(t *testing.T) {
			t.Parallel()
			r := newRunner(t)
			ctx := context.Background()

			bad, err := r.Check(ctx, f.Path)
			if err != nil {
				t.Fatalf("check input: %v", err)
			}
			if len(bad) == 0 {
				t.Fatal("verifier accepted the input")
			}

			out := r.Process(ctx, f.Path, f.Op, sanitize.Taken{})
			if f.Container == sanitize.ContainerUnknown {
				if _, ok := out.(sanitize.Rejected); !ok {
					t.Fatalf("unsupported input: got %T %v, want Rejected", out, out)
				}
				return
			}
			v, ok := out.(sanitize.Verified)
			if !ok {
				t.Fatalf("process: %T %v", out, out)
			}
			wantKind := f.Container
			if f.Op == sanitize.OpRemux {
				wantKind = sanitize.ContainerMP4
			}
			if v.Container() != wantKind {
				t.Errorf("kind: got %s, want %s", v.Container(), wantKind)
			}
			if bad, err := r.Check(ctx, v.Path()); err != nil || len(bad) > 0 {
				t.Fatalf("re-check output: %v %v", bad, err)
			}
			if f.Lossless {
				if a, b := codedData(t, tools, f.Path, f.Container), codedData(t, tools, v.Path(), v.Container()); !bytes.Equal(a, b) {
					t.Errorf("coded image data changed (%d → %d bytes)", len(a), len(b))
				}
			}
			if got := orientation(t, tools, v.Path(), v.Container()); got != f.Orientation {
				t.Errorf("orientation: got %d, want %d", got, f.Orientation)
			}
			decodes(t, tools, v.Path())
			if out := readAll(t, v.Path()); strings.Contains(out, "49.2827") || strings.Contains(out, "Jane") {
				t.Error("identifying text survives in the output bytes")
			}
		})
	}
}

func readAll(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// codedData returns the bytes that make up the picture: JPEG scan data,
// PNG IDAT, WebP VP8/VP8L, or per-packet hashes for HEIF and video.
func codedData(t *testing.T, tools sanitizetest.Tools, p string, k sanitize.Container) []byte {
	t.Helper()
	data := []byte(readAll(t, p))
	switch k {
	case sanitize.ContainerJPEG:
		return jpegImageData(t, data)
	case sanitize.ContainerPNG:
		var out []byte
		for i := 8; i+8 <= len(data); {
			n := int(binary.BigEndian.Uint32(data[i:]))
			if string(data[i+4:i+8]) == "IDAT" {
				out = append(out, data[i+8:i+8+n]...)
			}
			i += 12 + n
		}
		return out
	case sanitize.ContainerWebP:
		for _, tag := range []string{"VP8 ", "VP8L"} {
			if i := bytes.Index(data, []byte(tag)); i >= 0 {
				n := int(binary.LittleEndian.Uint32(data[i+4:]))
				return data[i+8 : i+8+n]
			}
		}
		t.Fatal("webp: no image chunk")
	case sanitize.ContainerHEIC, sanitize.ContainerAVIF, sanitize.ContainerMP4, sanitize.ContainerMOV:
		out, err := exec.Command(tools.FFmpeg, "-v", "error", "-i", p, "-map", "0:v:0", "-map", "0:a?",
			"-c", "copy", "-f", "framemd5", "-").Output()
		if err != nil {
			t.Fatalf("framemd5: %v", err)
		}
		// Keep stream index and hash columns; timebases may differ.
		var lines []string
		for line := range strings.SplitSeq(string(out), "\n") {
			if line == "" || line[0] == '#' {
				continue
			}
			f := strings.Split(line, ",")
			lines = append(lines, strings.TrimSpace(f[0])+","+strings.TrimSpace(f[len(f)-1]))
		}
		return []byte(strings.Join(lines, "\n"))
	}
	t.Fatalf("no coded data for %s", k)
	return nil
}

// jpegImageData returns every non-metadata segment (tables, frame and scan
// headers, entropy-coded data) of the main image, skipping APPn and COM
// payloads (which may embed thumbnails), up to EOI.
func jpegImageData(t *testing.T, data []byte) []byte {
	t.Helper()
	var out []byte
	i := 2
	for i+2 <= len(data) {
		if data[i] != 0xFF {
			t.Fatalf("jpeg: expected marker at %d", i)
		}
		m := data[i+1]
		if m == 0xD9 {
			return out
		}
		if i+4 > len(data) {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		seg := data[i : i+2+n]
		i += 2 + n
		if (m >= 0xE0 && m <= 0xEF) || m == 0xFE {
			continue
		}
		out = append(out, seg...)
		if m == 0xDA {
			start := i
			for i+1 < len(data) {
				if data[i] == 0xFF && data[i+1] != 0x00 && (data[i+1] < 0xD0 || data[i+1] > 0xD7) && data[i+1] != 0xFF {
					break
				}
				i++
			}
			out = append(out, data[start:i]...)
		}
	}
	t.Fatal("jpeg: no EOI")
	return nil
}

func orientation(t *testing.T, tools sanitizetest.Tools, p string, k sanitize.Container) int {
	t.Helper()
	if k.IsVideo() {
		out, err := exec.Command(tools.FFprobe, "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream_side_data=rotation", "-of", "csv=p=0", p).Output()
		if err != nil {
			t.Fatalf("ffprobe: %v", err)
		}
		s := strings.TrimSpace(strings.Split(string(out), "\n")[0])
		if s == "" {
			return 0
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	out, err := exec.Command(tools.Exiftool, "-s3", "-n", "-Orientation", p).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func decodes(t *testing.T, tools sanitizetest.Tools, p string) {
	t.Helper()
	cmd := exec.Command(tools.FFmpeg, "-v", "error", "-xerror", "-i", p, "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("output doesn't decode: %v: %s", err, out)
	}
}
