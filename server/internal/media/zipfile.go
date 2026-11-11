package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"sort"
)

// A virtual Store-method zip: headers are built in memory, file bodies are
// read from the pinned cache files on demand. Sizes and CRCs are known
// before the download starts, so the response has an exact Content-Length
// and supports Range requests. Entry times are fixed at 1980-01-01 so the
// archive carries no dates.

type zipSource struct {
	name string
	path string // file body, or
	data []byte // in-memory body
	size int64
	crc  uint32
}

type segment struct {
	off  int64
	size int64
	data []byte // header bytes, or
	src  int    // index into sources (file body)
}

type zipLayout struct {
	sources []zipSource
	segs    []segment
	size    int64
}

const (
	zip64Limit = 0xFFFFFFFF
	dosDate    = 0x0021 // 1980-01-01
)

func le16(b *bytes.Buffer, v uint16) { _ = binary.Write(b, binary.LittleEndian, v) }
func le32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
func le64(b *bytes.Buffer, v uint64) { _ = binary.Write(b, binary.LittleEndian, v) }

func clamp32(v int64) uint32 {
	if v >= zip64Limit {
		return zip64Limit
	}
	return uint32(v)
}

// layoutZip computes the archive for sources.
func layoutZip(sources []zipSource) *zipLayout {
	z := &zipLayout{sources: sources}
	add := func(data []byte, src int) {
		size := int64(len(data))
		if data == nil {
			size = sources[src].size
		}
		z.segs = append(z.segs, segment{off: z.size, size: size, data: data, src: src})
		z.size += size
	}
	offsets := make([]int64, len(sources))
	for i, s := range sources {
		offsets[i] = z.size
		big := s.size >= zip64Limit
		var h bytes.Buffer
		le32(&h, 0x04034b50)
		if big {
			le16(&h, 45)
		} else {
			le16(&h, 20)
		}
		le16(&h, 0)       // flags
		le16(&h, 0)       // method: store
		le16(&h, 0)       // time
		le16(&h, dosDate) // date
		le32(&h, s.crc)
		le32(&h, clamp32(s.size))
		le32(&h, clamp32(s.size))
		le16(&h, uint16(len(s.name)))
		if big {
			le16(&h, 20)
		} else {
			le16(&h, 0)
		}
		h.WriteString(s.name)
		if big {
			le16(&h, 0x0001)
			le16(&h, 16)
			le64(&h, uint64(s.size))
			le64(&h, uint64(s.size))
		}
		add(h.Bytes(), -1)
		if s.data != nil {
			add(s.data, -1)
		} else {
			add(nil, i)
		}
	}
	cdStart := z.size
	var cd bytes.Buffer
	for i, s := range sources {
		big := s.size >= zip64Limit || offsets[i] >= zip64Limit
		le32(&cd, 0x02014b50)
		le16(&cd, 45) // made by
		if big {
			le16(&cd, 45)
		} else {
			le16(&cd, 20)
		}
		le16(&cd, 0)
		le16(&cd, 0)
		le16(&cd, 0)
		le16(&cd, dosDate)
		le32(&cd, s.crc)
		if big {
			le32(&cd, zip64Limit)
			le32(&cd, zip64Limit)
		} else {
			le32(&cd, uint32(s.size))
			le32(&cd, uint32(s.size))
		}
		le16(&cd, uint16(len(s.name)))
		if big {
			le16(&cd, 28)
		} else {
			le16(&cd, 0)
		}
		le16(&cd, 0) // comment
		le16(&cd, 0) // disk
		le16(&cd, 0) // internal attrs
		le32(&cd, 0) // external attrs
		if big {
			le32(&cd, zip64Limit)
		} else {
			le32(&cd, uint32(offsets[i]))
		}
		cd.WriteString(s.name)
		if big {
			le16(&cd, 0x0001)
			le16(&cd, 24)
			le64(&cd, uint64(s.size))
			le64(&cd, uint64(s.size))
			le64(&cd, uint64(offsets[i]))
		}
	}
	cdSize := int64(cd.Len())
	var end bytes.Buffer
	n := len(sources)
	if cdStart >= zip64Limit || cdSize >= zip64Limit || n >= 0xFFFF {
		eocd64 := cdStart + cdSize
		le32(&end, 0x06064b50)
		le64(&end, 44)
		le16(&end, 45)
		le16(&end, 45)
		le32(&end, 0)
		le32(&end, 0)
		le64(&end, uint64(n))
		le64(&end, uint64(n))
		le64(&end, uint64(cdSize))
		le64(&end, uint64(cdStart))
		le32(&end, 0x07064b50)
		le32(&end, 0)
		le64(&end, uint64(eocd64))
		le32(&end, 1)
	}
	le32(&end, 0x06054b50)
	le16(&end, 0)
	le16(&end, 0)
	le16(&end, uint16(min(n, 0xFFFF)))
	le16(&end, uint16(min(n, 0xFFFF)))
	le32(&end, clamp32(cdSize))
	le32(&end, clamp32(cdStart))
	le16(&end, 0)
	add(append(cd.Bytes(), end.Bytes()...), -1)
	return z
}

// zipReader reads a layout; Close closes the body files.
type zipReader struct {
	*io.SectionReader
	z     *zipLayout
	files map[int]*os.File
}

func (z *zipLayout) open() *zipReader {
	r := &zipReader{z: z, files: map[int]*os.File{}}
	r.SectionReader = io.NewSectionReader(r, 0, z.size)
	return r
}

// ReadAt implements io.ReaderAt over the segments.
func (r *zipReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.z.size {
		return 0, io.EOF
	}
	n := 0
	i := sort.Search(len(r.z.segs), func(i int) bool { return r.z.segs[i].off+r.z.segs[i].size > off })
	for ; i < len(r.z.segs) && n < len(p); i++ {
		s := r.z.segs[i]
		within := off + int64(n) - s.off
		want := min(int64(len(p)-n), s.size-within)
		if s.data != nil {
			n += copy(p[n:n+int(want)], s.data[within:])
			continue
		}
		f := r.files[s.src]
		if f == nil {
			var err error
			if f, err = os.Open(r.z.sources[s.src].path); err != nil {
				return n, err
			}
			r.files[s.src] = f
		}
		m, err := f.ReadAt(p[n:n+int(want)], within)
		n += m
		if err != nil && (!errors.Is(err, io.EOF) || int64(m) != want) {
			return n, err
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *zipReader) Close() error {
	for _, f := range r.files {
		_ = f.Close()
	}
	return nil
}

func fileCRC(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	return h.Sum32(), nil
}
