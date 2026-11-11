// Package sanitizetest generates the sanitizer corpus: real image and video
// files carrying GPS and other identifying metadata, encoded with ffmpeg and
// tagged with exiftool. Nothing is committed, so there are no third-party
// sample licences to track.
package sanitizetest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
)

// TB is the subset of testing.TB the corpus needs, so tests/cmd/fake-immich
// can build it outside tests.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Fixture is one corpus file.
type Fixture struct {
	Name      string
	Path      string
	Container sanitize.Container
	// Op is how the sanitizer handles it: strip for images, remux for video.
	Op sanitize.Op
	// Orientation is the EXIF orientation (images) or display rotation in
	// degrees (video) that must survive.
	Orientation int
	// Lossless reports whether the coded image data must be unchanged.
	Lossless bool
}

// Tools are the binaries the corpus and the sanitizer tests need.
type Tools struct {
	Exiftool, FFmpeg, FFprobe string
}

// LookTools finds the tools or fails the test.
func LookTools(t TB) Tools {
	t.Helper()
	var tools Tools
	for name, dst := range map[string]*string{"exiftool": &tools.Exiftool, "ffmpeg": &tools.FFmpeg, "ffprobe": &tools.FFprobe} {
		p, err := sanitize.LookTool(name)
		if err != nil {
			t.Fatalf("%s not found on PATH (needed by the sanitizer tests)", name)
		}
		*dst = p
	}
	return tools
}

var (
	corpusOnce sync.Once
	corpusDir  string
	corpus     []Fixture
	corpusErr  error
)

// Corpus generates the fixtures once per test binary and returns them.
// The files must not be modified.
func Corpus(t TB) []Fixture {
	t.Helper()
	tools := LookTools(t)
	corpusOnce.Do(func() {
		corpusDir, corpusErr = os.MkdirTemp("", "sis-corpus-")
		if corpusErr == nil {
			corpus, corpusErr = build(tools, corpusDir)
		}
	})
	if corpusErr != nil {
		t.Fatalf("corpus: %v", corpusErr)
	}
	return corpus
}

// RemoveCorpus deletes the generated files; call it from TestMain after
// m.Run.
func RemoveCorpus() {
	if corpusDir != "" {
		_ = os.RemoveAll(corpusDir)
	}
}

// ByName returns the named fixture.
func ByName(t TB, name string) Fixture {
	t.Helper()
	for _, f := range Corpus(t) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no fixture %q", name)
	return Fixture{}
}

type builder struct {
	tools Tools
	dir   string
	err   error
}

func (b *builder) run(name string, args ...string) {
	if b.err != nil {
		return
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = b.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		b.err = fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err, out)
	}
}

func (b *builder) ffmpeg(args ...string) {
	b.run(b.tools.FFmpeg, append([]string{"-nostdin", "-v", "error", "-y"}, args...)...)
}

func (b *builder) exif(file string, args ...string) {
	b.run(b.tools.Exiftool, append(append([]string{"-q", "-q", "-overwrite_original"}, args...), file)...)
}

func (b *builder) path(name string) string { return filepath.Join(b.dir, name) }

func (b *builder) concat(dst string, parts ...[]byte) {
	if b.err != nil {
		return
	}
	b.err = os.WriteFile(b.path(dst), bytes.Join(parts, nil), 0o600)
}

func (b *builder) read(name string) []byte {
	if b.err != nil {
		return nil
	}
	data, err := os.ReadFile(b.path(name))
	if err != nil {
		b.err = err
	}
	return data
}

// identifying is the set of tags written into every image fixture.
var identifying = []string{
	"-GPSLatitude=49.2827", "-GPSLatitudeRef=N", "-GPSLongitude=123.1207", "-GPSLongitudeRef=W",
	"-GPSAltitude=70", "-Make=Apple", "-Model=iPhone 15 Pro", "-SerialNumber=F2LXK0QZ1234",
	"-LensModel=iPhone 15 Pro back camera", "-Software=17.4.1", "-DateTimeOriginal=2024:05:01 10:00:00",
	"-OffsetTimeOriginal=-07:00", "-Artist=Jane Doe", "-ImageDescription=At home",
	"-XMP-dc:Creator=Jane Doe", "-XMP-photoshop:City=Vancouver", "-IPTC:City=Vancouver",
	"-IPTC:Keywords=private",
}

var videoIdentifying = []string{
	"-Keys:GPSCoordinates=49.2827 -123.1207", "-UserData:GPSCoordinates=49.2827 -123.1207",
	"-Keys:Make=Apple", "-Keys:Model=iPhone 15 Pro", "-Keys:Software=17.4.1",
	"-Keys:CreationDate=2024:05:01 10:00:00-07:00", "-Keys:ContentIdentifier=A1B2C3D4-0000-1111-2222-333344445555",
	"-ItemList:Author=Jane Doe",
}

func build(tools Tools, dir string) ([]Fixture, error) {
	b := &builder{tools: tools, dir: dir}
	src := "testsrc2=size=320x240:duration=1:rate=10"

	b.ffmpeg("-f", "lavfi", "-i", src, "-frames:v", "1", "-q:v", "3", "clean.jpg")
	b.ffmpeg("-f", "lavfi", "-i", src, "-frames:v", "1", "clean.png")
	b.ffmpeg("-f", "lavfi", "-i", src, "-frames:v", "1", "-c:v", "libwebp", "clean.webp")
	b.ffmpeg("-f", "lavfi", "-i", src, "-frames:v", "1", "-c:v", "libaom-av1", "-still-picture", "1", "-cpu-used", "8", "clean.avif")
	b.ffmpeg("-f", "lavfi", "-i", src, "-f", "lavfi", "-i", "sine=duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "clean.mp4")
	b.ffmpeg("-f", "lavfi", "-i", src, "-f", "lavfi", "-i", "sine=duration=1",
		"-c:v", "libx265", "-tag:v", "hvc1", "-x265-params", "log-level=error", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", "-f", "mov", "clean.mov")

	// iPhone-like: JPEG with GPS, camera, dates, XMP and IPTC, rotated.
	b.ffmpeg("-i", b.path("clean.jpg"), "-c", "copy", "gps.jpg")
	b.exif("gps.jpg", append(identifying, "-Orientation#=6")...)

	// Pixel motion photo: an MP4 with GPS appended after the JPEG's EOI.
	b.ffmpeg("-i", b.path("clean.mp4"), "-c", "copy", "motion-part.mp4")
	b.exif("motion-part.mp4", videoIdentifying...)
	motionPart := b.read("motion-part.mp4")
	b.concat("pixel-motion.jpg", b.read("gps.jpg"), motionPart)
	b.exif("pixel-motion.jpg", "-XMP-GCamera:MotionPhoto=1", "-XMP-GCamera:MicroVideoOffset="+fmt.Sprint(len(motionPart)))

	// Samsung trailer after EOI.
	b.concat("samsung.jpg", b.read("gps.jpg"), []byte("SEFH\x01\x00\x00\x00GPS 49.2827,-123.1207SEFT"))

	// PNG with eXIf, tEXt and iTXt (XMP).
	b.ffmpeg("-i", b.path("clean.png"), "-c", "copy", "gps.png")
	b.exif("gps.png", append(identifying[:4:4], "-PNG:Comment=Jane at home", "-PNG:Author=Jane Doe", "-XMP-dc:Creator=Jane Doe")...)

	// WebP with EXIF and XMP chunks.
	b.ffmpeg("-i", b.path("clean.webp"), "-c", "copy", "gps.webp")
	b.exif("gps.webp", append(identifying[:4:4], "-Make=Apple", "-XMP-dc:Creator=Jane Doe", "-Orientation#=8")...)

	// AVIF (HEIF container) with EXIF and XMP items.
	b.ffmpeg("-i", b.path("clean.avif"), "-c", "copy", "gps.avif")
	b.exif("gps.avif", append(identifying[:4:4], "-Make=Apple", "-Model=iPhone 15 Pro", "-XMP-dc:Creator=Jane Doe")...)

	// HEIC: the AVIF container relabelled with HEIC brands, so the HEIF
	// metadata paths are exercised without an HEVC still encoder.
	if b.err == nil {
		data := b.read("gps.avif")
		b.concat("gps.heic", relabelHEIC(data))
	}

	// MP4 with GPS in Keys, UserData and ItemList.
	b.ffmpeg("-i", b.path("clean.mp4"), "-c", "copy", "gps.mp4")
	b.exif("gps.mp4", videoIdentifying...)

	// MP4 with a timed text track carrying GPS fixes (a metadata track).
	b.run(tools.FFmpeg, "-nostdin", "-v", "error", "-y", "-i", b.path("clean.mp4"), "-f", "srt", "-i", writeSRT(b),
		"-map", "0", "-map", "1", "-c", "copy", "-c:s", "mov_text", "-metadata:s:s:0", "handler_name=GPS 49.2827,-123.1207",
		"gps-track.mp4")
	b.exif("gps-track.mp4", videoIdentifying[:2]...)

	// HEVC MOV, rotated 90°, Apple keys (live-photo motion part shape).
	b.ffmpeg("-display_rotation", "90", "-i", b.path("clean.mov"), "-c", "copy", "-f", "mov", "rotated.mov")
	b.exif("rotated.mov", videoIdentifying...)

	// Derivatives as Immich generates them: metadata-free JPEG and WebP.
	b.run(tools.Exiftool, "-q", "-q", "-all=", "-o", b.path("derivative.jpg"), b.path("clean.jpg"))
	b.run(tools.Exiftool, "-q", "-q", "-all=", "-o", b.path("derivative.webp"), b.path("clean.webp"))

	// TIFF (RAW/DNG stand-in): never stripped.
	b.ffmpeg("-i", b.path("clean.png"), "-frames:v", "1", "gps.tiff")
	b.exif("gps.tiff", identifying[:4]...)

	if b.err != nil {
		return nil, b.err
	}
	return []Fixture{
		{Name: "jpeg-gps", Path: b.path("gps.jpg"), Container: sanitize.ContainerJPEG, Op: sanitize.OpStrip, Orientation: 6, Lossless: true},
		{Name: "pixel-motion", Path: b.path("pixel-motion.jpg"), Container: sanitize.ContainerJPEG, Op: sanitize.OpStrip, Orientation: 6, Lossless: true},
		{Name: "pixel-motion-part", Path: b.path("motion-part.mp4"), Container: sanitize.ContainerMP4, Op: sanitize.OpRemux, Lossless: true},
		{Name: "samsung-trailer", Path: b.path("samsung.jpg"), Container: sanitize.ContainerJPEG, Op: sanitize.OpStrip, Orientation: 6, Lossless: true},
		{Name: "png-exif-itxt", Path: b.path("gps.png"), Container: sanitize.ContainerPNG, Op: sanitize.OpStrip, Orientation: 0, Lossless: true},
		{Name: "webp-xmp", Path: b.path("gps.webp"), Container: sanitize.ContainerWebP, Op: sanitize.OpStrip, Orientation: 8, Lossless: true},
		{Name: "avif-gps", Path: b.path("gps.avif"), Container: sanitize.ContainerAVIF, Op: sanitize.OpStrip, Lossless: true},
		{Name: "heic-gps", Path: b.path("gps.heic"), Container: sanitize.ContainerHEIC, Op: sanitize.OpStrip, Lossless: true},
		{Name: "mp4-gps", Path: b.path("gps.mp4"), Container: sanitize.ContainerMP4, Op: sanitize.OpRemux, Lossless: true},
		{Name: "mp4-gps-track", Path: b.path("gps-track.mp4"), Container: sanitize.ContainerMP4, Op: sanitize.OpRemux, Lossless: true},
		{Name: "mov-rotated", Path: b.path("rotated.mov"), Container: sanitize.ContainerMOV, Op: sanitize.OpRemux, Orientation: 90, Lossless: true},
		{Name: "tiff", Path: b.path("gps.tiff"), Container: sanitize.ContainerUnknown, Op: sanitize.OpStrip},
	}, nil
}

// Clean returns the path of a generated file without identifying metadata:
// clean.jpg, clean.png, clean.webp, clean.avif, clean.mp4, clean.mov, or
// the Immich-style derivatives derivative.jpg and derivative.webp.
func Clean(t TB, name string) string {
	t.Helper()
	Corpus(t)
	return filepath.Join(corpusDir, name)
}

func writeSRT(b *builder) string {
	p := b.path("gps.srt")
	if b.err == nil {
		b.err = os.WriteFile(p, []byte("1\n00:00:00,000 --> 00:00:00,900\nGPS 49.2827,-123.1207 2024-05-01T10:00:00Z\n"), 0o600)
	}
	return p
}

// relabelHEIC rewrites the ftyp box's brands to heic/mif1.
func relabelHEIC(data []byte) []byte {
	out := bytes.Clone(data)
	if len(out) < 16 || string(out[4:8]) != "ftyp" {
		return out
	}
	size := int(binary.BigEndian.Uint32(out[0:4]))
	copy(out[8:12], "heic")
	for i := 16; i+4 <= size; i += 4 {
		copy(out[i:i+4], "mif1")
	}
	if size >= 20 {
		copy(out[16:20], "heic")
	}
	return out
}
