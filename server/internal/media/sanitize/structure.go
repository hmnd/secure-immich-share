package sanitize

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Container is a file format, sniffed from a file's bytes.
type Container string

const (
	ContainerUnknown Container = ""
	ContainerJPEG    Container = "jpeg"
	ContainerPNG     Container = "png"
	ContainerWebP    Container = "webp"
	ContainerHEIC    Container = "heic"
	ContainerAVIF    Container = "avif"
	ContainerMP4     Container = "mp4"
	ContainerMOV     Container = "mov"
)

// ContentType is the MIME type for the container.
func (k Container) ContentType() string {
	switch k {
	case ContainerJPEG:
		return "image/jpeg"
	case ContainerPNG:
		return "image/png"
	case ContainerWebP:
		return "image/webp"
	case ContainerHEIC:
		return "image/heic"
	case ContainerAVIF:
		return "image/avif"
	case ContainerMP4:
		return "video/mp4"
	case ContainerMOV:
		return "video/quicktime"
	}
	return "application/octet-stream"
}

func (k Container) Ext() string {
	switch k {
	case ContainerJPEG:
		return "jpg"
	case ContainerUnknown:
		return "bin"
	}
	return string(k)
}

// IsVideo reports whether k is a video container.
func (k Container) IsVideo() bool { return k == ContainerMP4 || k == ContainerMOV }

// Sniff detects the container from the first bytes of a file.
func Sniff(path string) (Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return ContainerUnknown, err
	}
	defer f.Close()
	head := make([]byte, 64)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ContainerUnknown, err
	}
	return sniffBytes(head[:n]), nil
}

func sniffBytes(b []byte) Container {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ContainerJPEG
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ContainerPNG
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ContainerWebP
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		size := int(binary.BigEndian.Uint32(b[0:4]))
		if size < 16 || size > len(b) {
			size = len(b)
		}
		major := string(b[8:12])
		var compat []string
		for i := 16; i+4 <= size; i += 4 {
			compat = append(compat, string(b[i:i+4]))
		}
		return isoKind(major, compat)
	}
	return ContainerUnknown
}

func isoKind(major string, compat []string) Container {
	switch major {
	case "avif", "avis":
		return ContainerAVIF
	case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
		for _, c := range compat {
			if c == "avif" {
				return ContainerAVIF
			}
		}
		return ContainerHEIC
	case "qt  ":
		return ContainerMOV
	case "isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "M4VP", "dash", "3gp4", "3gp5", "3g2a":
		return ContainerMP4
	}
	return ContainerUnknown
}

// checkStructure walks the container and fails on anything after the
// format's terminator and on segments or chunks that carry metadata. It
// works on the bytes directly, independent of exiftool.
func checkStructure(path string, k Container) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	switch k {
	case ContainerJPEG:
		return checkJPEG(r)
	case ContainerPNG:
		return checkPNG(r)
	case ContainerWebP:
		return checkWebP(r, st.Size())
	case ContainerHEIC, ContainerAVIF:
		return checkISO(f, st.Size(), isoImageBoxes)
	case ContainerMP4, ContainerMOV:
		return checkISO(f, st.Size(), isoVideoBoxes)
	}
	return fmt.Errorf("unsupported kind %q", k)
}

var errTrailing = errors.New("data after end of image")

func expectEOF(r *bufio.Reader) error {
	if _, err := r.ReadByte(); err == nil {
		return errTrailing
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// checkJPEG walks marker segments. Only JFIF (APP0), Exif (APP1), ICC
// (APP2) and Adobe (APP14) application segments are allowed; comments and
// every other APPn (XMP, MPF, maker data) fail. The file must end exactly at
// EOI.
func checkJPEG(r *bufio.Reader) error {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return errors.New("jpeg: missing SOI")
	}
	var pending byte // marker already consumed by the entropy scan
	for {
		marker := pending
		pending = 0
		if marker == 0 {
			var err error
			if marker, err = nextMarker(r); err != nil {
				return err
			}
		}
		switch {
		case marker == 0xD9: // EOI
			return expectEOF(r)
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01:
			continue
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return fmt.Errorf("jpeg: segment %02X: %w", marker, err)
		}
		n := int(binary.BigEndian.Uint16(lenBuf[:])) - 2
		if n < 0 {
			return fmt.Errorf("jpeg: segment %02X: bad length", marker)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return fmt.Errorf("jpeg: segment %02X: %w", marker, err)
		}
		switch {
		case marker == 0xFE:
			return errors.New("jpeg: comment segment")
		case marker == 0xE0:
			if !bytes.HasPrefix(payload, []byte("JFIF\x00")) {
				return errors.New("jpeg: APP0 is not JFIF")
			}
		case marker == 0xE1:
			if !bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
				return errors.New("jpeg: APP1 is not Exif")
			}
		case marker == 0xE2:
			if !bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00")) {
				return errors.New("jpeg: APP2 is not ICC")
			}
		case marker == 0xEE:
			if !bytes.HasPrefix(payload, []byte("Adobe")) {
				return errors.New("jpeg: APP14 is not Adobe")
			}
		case marker >= 0xE0 && marker <= 0xEF:
			return fmt.Errorf("jpeg: APP%d segment", marker-0xE0)
		case marker == 0xDA: // SOS: entropy-coded data follows
			m, err := scanEntropy(r)
			if err != nil {
				return err
			}
			pending = m
		}
	}
}

// nextMarker reads 0xFF (fill bytes allowed) and returns the marker code.
func nextMarker(r *bufio.Reader) (byte, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("jpeg: no EOI: %w", err)
	}
	if b != 0xFF {
		return 0, fmt.Errorf("jpeg: expected marker, got %02X", b)
	}
	for {
		m, err := r.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("jpeg: no EOI: %w", err)
		}
		if m != 0xFF {
			return m, nil
		}
	}
}

// scanEntropy consumes scan data and returns the first marker that isn't
// byte stuffing or a restart marker.
func scanEntropy(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("jpeg: no EOI: %w", err)
		}
		if b != 0xFF {
			continue
		}
		for {
			m, err := r.ReadByte()
			if err != nil {
				return 0, fmt.Errorf("jpeg: no EOI: %w", err)
			}
			switch {
			case m == 0xFF:
				continue
			case m == 0x00 || (m >= 0xD0 && m <= 0xD7):
			default:
				return m, nil
			}
			break
		}
	}
}

var pngAllowed = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "cHRM": true, "gAMA": true,
	"iCCP": true, "sBIT": true, "sRGB": true, "bKGD": true, "pHYs": true, "cICP": true, "mDCv": true, "cLLi": true,
}

// checkPNG walks chunks: only image and colour chunks are allowed (no text,
// eXIf or time chunks), and the file must end exactly after IEND.
func checkPNG(r *bufio.Reader) error {
	sig := make([]byte, 8)
	if _, err := io.ReadFull(r, sig); err != nil {
		return err
	}
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return fmt.Errorf("png: no IEND: %w", err)
		}
		n := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		if !pngAllowed[typ] {
			return fmt.Errorf("png: chunk %q", typ)
		}
		if _, err := io.CopyN(io.Discard, r, n+4); err != nil {
			return fmt.Errorf("png: chunk %q: %w", typ, err)
		}
		if typ == "IEND" {
			return expectEOF(r)
		}
	}
}

var webpAllowed = map[string]bool{"VP8 ": true, "VP8L": true, "VP8X": true, "ALPH": true, "ICCP": true, "EXIF": true}

// checkWebP checks the RIFF size covers the file exactly and only image,
// alpha, ICC and EXIF chunks are present (EXIF content is checked by the
// tag verifier).
func checkWebP(r *bufio.Reader, size int64) error {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	if int64(binary.LittleEndian.Uint32(hdr[4:8]))+8 != size {
		return errTrailing
	}
	read := int64(12)
	for read < size {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return fmt.Errorf("webp: %w", err)
		}
		typ := string(ch[0:4])
		n := int64(binary.LittleEndian.Uint32(ch[4:8]))
		n += n & 1
		if !webpAllowed[typ] {
			return fmt.Errorf("webp: chunk %q", typ)
		}
		if _, err := io.CopyN(io.Discard, r, n); err != nil {
			return fmt.Errorf("webp: chunk %q: %w", typ, err)
		}
		read += 8 + n
	}
	if read != size {
		return errTrailing
	}
	return expectEOF(r)
}

// zeroPadding requires a padding box's payload to be zeros, so it can't
// carry leftover data.
func zeroPadding(f io.ReaderAt, off, n int64) error {
	if n > 1<<20 {
		return errors.New("iso: padding box too large")
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil {
		return fmt.Errorf("iso: %w", err)
	}
	for _, b := range buf {
		if b != 0 {
			return errors.New("iso: padding box with data")
		}
	}
	return nil
}

var (
	isoImageBoxes = map[string]bool{"ftyp": true, "meta": true, "mdat": true, "free": true, "skip": true}
	isoVideoBoxes = map[string]bool{"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true, "wide": true}
)

// checkISO walks top-level ISOBMFF boxes: ftyp first, only the allowed
// types, each at most once (bar padding), covering the file exactly.
func checkISO(f io.ReaderAt, size int64, allowed map[string]bool) error {
	var off int64
	seen := map[string]bool{}
	for off < size {
		var hdr [16]byte
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return fmt.Errorf("iso: %w", err)
		}
		n := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		head := int64(8)
		switch n {
		case 1:
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return fmt.Errorf("iso: %w", err)
			}
			large := binary.BigEndian.Uint64(hdr[8:16])
			if large > uint64(size) {
				return fmt.Errorf("iso: box %q: bad size", typ)
			}
			n = int64(large)
			head = 16
		case 0:
			n = size - off
		}
		if off == 0 && typ != "ftyp" {
			return errors.New("iso: first box is not ftyp")
		}
		if !allowed[typ] {
			return fmt.Errorf("iso: top-level box %q", typ)
		}
		if seen[typ] && typ != "free" && typ != "skip" {
			return fmt.Errorf("iso: second %q box", typ)
		}
		seen[typ] = true
		if n < head || n > size-off {
			return fmt.Errorf("iso: box %q: bad size", typ)
		}
		if typ == "free" || typ == "skip" {
			if err := zeroPadding(f, off+head, n-head); err != nil {
				return err
			}
		}
		off += n
	}
	return nil
}
