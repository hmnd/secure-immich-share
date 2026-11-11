package sanitize_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

// Mutation tests: start from a file the verifier accepts, add one forbidden
// thing, and require a rejection. This guards against the allowlist slowly
// getting looser.

func verifiedBase(t *testing.T, r *sanitize.Runner, clean string, op sanitize.Op) string {
	t.Helper()
	out := r.Process(context.Background(), sanitizetest.Clean(t, clean), op, sanitize.Taken{})
	v, ok := out.(sanitize.Verified)
	if !ok {
		t.Fatalf("clean %s: %T %v", clean, out, out)
	}
	return v.Path()
}

func copyTo(t *testing.T, src, name string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

// writeTag writes one tag with exiftool and fails if the file didn't change.
func writeTag(t *testing.T, exiftool, p, tag string) {
	t.Helper()
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(exiftool, "-q", "-overwrite_original", tag, p).CombinedOutput(); err != nil {
		t.Fatalf("exiftool %s: %v %s", tag, err, out)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatalf("exiftool %s: nothing written", tag)
	}
}

func mustReject(t *testing.T, r *sanitize.Runner, p string) {
	t.Helper()
	bad, err := r.Check(context.Background(), p)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(bad) == 0 {
		t.Fatal("verifier accepted the mutated file")
	}
}

func TestMutationImageTags(t *testing.T) {
	t.Parallel()
	tools := sanitizetest.LookTools(t)
	r := newRunner(t)
	base := verifiedBase(t, r, "clean.jpg", sanitize.OpStrip)
	tags := []string{
		"-GPSLatitude=49.28", "-GPSLongitude=123.12", "-GPSAltitude=70", "-GPSDateStamp=2024:05:01",
		"-Make=Apple", "-Model=iPhone", "-SerialNumber=X1", "-LensSerialNumber=L2", "-LensModel=Lens",
		"-Software=17.4", "-DateTimeOriginal=2024:05:01 10:00:00",
		"-CreateDate=2024:05:01 10:00:00", "-ModifyDate=2024:05:01 10:00:00", "-OffsetTime=-07:00",
		"-SubSecTimeOriginal=123", "-Artist=Jane", "-Copyright=Jane", "-ImageDescription=home",
		"-UserComment=home", "-OwnerName=Jane", "-ImageUniqueID=abc",
		"-HostComputer=mac", "-DocumentName=doc", "-XMP-dc:Creator=Jane", "-XMP-dc:Title=home",
		"-XMP-photoshop:City=Vancouver", "-XMP-exif:GPSLatitude=49.28", "-IPTC:City=Vancouver",
		"-IPTC:Keywords=private", "-IPTC:By-line=Jane", "-Comment=hello", "-ThumbnailImage<=" + base,
	}
	for _, tag := range tags {
		t.Run(strings.TrimPrefix(strings.SplitN(tag, "=", 2)[0], "-"), func(t *testing.T) {
			t.Parallel()
			p := copyTo(t, base, "m.jpg")
			writeTag(t, tools.Exiftool, p, tag)
			mustReject(t, r, p)
		})
	}
}

func TestMutationVideoTags(t *testing.T) {
	t.Parallel()
	tools := sanitizetest.LookTools(t)
	r := newRunner(t)
	base := verifiedBase(t, r, "clean.mp4", sanitize.OpRemux)
	tags := []string{
		"-Keys:GPSCoordinates=49.28 -123.12", "-UserData:GPSCoordinates=49.28 -123.12",
		"-Keys:Make=Apple", "-Keys:Model=iPhone", "-Keys:Software=17", "-Keys:CreationDate=2024:05:01 10:00:00",
		"-Keys:ContentIdentifier=ABC", "-Keys:LocationName=Home", "-UserData:Make=Apple",
		"-ItemList:Author=Jane", "-ItemList:Title=home", "-ItemList:Comment=home", "-XMP-dc:Creator=Jane",
		"-QuickTime:CreateDate=2024:05:01 10:00:00", "-Track1:TrackCreateDate=2024:05:01 10:00:00",
		"-Track1:MediaModifyDate=2024:05:01 10:00:00",
	}
	for _, tag := range tags {
		t.Run(strings.TrimPrefix(strings.SplitN(tag, "=", 2)[0], "-"), func(t *testing.T) {
			t.Parallel()
			p := copyTo(t, base, "m.mp4")
			writeTag(t, tools.Exiftool, p, tag)
			mustReject(t, r, p)
		})
	}
}

func TestMutationTrailingBytes(t *testing.T) {
	t.Parallel()
	r := newRunner(t)
	cases := []struct {
		clean, name string
		op          sanitize.Op
	}{
		{"clean.jpg", "m.jpg", sanitize.OpStrip},
		{"clean.png", "m.png", sanitize.OpStrip},
		{"clean.webp", "m.webp", sanitize.OpStrip},
		{"clean.avif", "m.avif", sanitize.OpStrip},
		{"clean.mp4", "m.mp4", sanitize.OpRemux},
	}
	trailers := map[string][]byte{
		"junk":     []byte("GPS 49.2827,-123.1207"),
		"eoi-like": []byte("secret\xff\xd9"),
		"mp4":      []byte("\x00\x00\x00\x10ftypisom\x00\x00\x02\x00"),
	}
	for _, c := range cases {
		base := verifiedBase(t, r, c.clean, c.op)
		for name, trailer := range trailers {
			t.Run(c.clean+"/"+name, func(t *testing.T) {
				t.Parallel()
				p := copyTo(t, base, c.name)
				f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.Write(trailer); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
				mustReject(t, r, p)
			})
		}
	}
}

func TestMutationJPEGSegments(t *testing.T) {
	t.Parallel()
	r := newRunner(t)
	base := verifiedBase(t, r, "clean.jpg", sanitize.OpStrip)
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	segment := func(marker byte, payload string) []byte {
		n := len(payload) + 2
		return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
	}
	cases := map[string][]byte{
		"comment": segment(0xFE, "Jane at home"),
		"xmp":     segment(0xE1, "http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>"),
		"mpf":     segment(0xE2, "MPF\x00II*\x00"),
		"app5":    segment(0xE5, "vendor data"),
	}
	for name, seg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mutated := append(append(append([]byte{}, data[:2]...), seg...), data[2:]...)
			p := filepath.Join(t.TempDir(), "m.jpg")
			if err := os.WriteFile(p, mutated, 0o600); err != nil {
				t.Fatal(err)
			}
			mustReject(t, r, p)
		})
	}
}
