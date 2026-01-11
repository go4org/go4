// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package ziputil is a fork of parts of the Go standard library's archive/zip
// package, exposing details of the zip file format that standard library does
// not make available.
//
// In particular, it enables reading both the Table of Contents (the list of all
// files in a zip) and individual files out of a zip file from an HTTP server
// using the minimum number and size of HTTP requests. A naive implementation
// would implement an io.ReaderAt in terms of HTTP Range requests, but
// archive/zip makes way too many ReadAt calls for that to be efficient. A
// slightly less naive version would then implement a ReaderAt that does
// page-aligned chunking and caching. That's better, but still not good. This
// package permits doing 1-2 range requests to get the TOC, and then exactly one
// streaming HTTP request per random file downloaded, sized correctly.
package ziputil

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"go4.org/readerutil"
)

var (
	errFormat = zip.ErrFormat
)

// Compression methods.
const (
	methodStore   uint16 = 0 // no compression
	methodDeflate uint16 = 8 // DEFLATE compressed
)

const (
	fileHeaderSignature      = 0x04034b50
	directoryHeaderSignature = 0x02014b50
	directoryEndSignature    = 0x06054b50
	directory64LocSignature  = 0x07064b50
	directory64EndSignature  = 0x06064b50
	dataDescriptorSignature  = 0x08074b50 // de-facto standard; required by OS X Finder
	fileHeaderLen            = 30         // + filename + extra
	directoryHeaderLen       = 46         // + filename + extra + comment
	directoryEndLen          = 22         // + comment
	dataDescriptorLen        = 16         // four uint32: descriptor signature, crc32, compressed size, size
	dataDescriptor64Len      = 24         // two uint32: signature, crc32 | two uint64: compressed size, size
	directory64LocLen        = 20         //
	directory64EndLen        = 56         // + extra

	// Constants for the first byte in CreatorVersion.
	creatorFAT    = 0
	creatorUnix   = 3
	creatorNTFS   = 11
	creatorVFAT   = 14
	creatorMacOSX = 19

	// Version numbers.
	zipVersion20 = 20 // 2.0
	zipVersion45 = 45 // 4.5 (reads and writes zip64 archives)

	// Limits for non zip64 files.
	uint16max = (1 << 16) - 1
	uint32max = (1 << 32) - 1

	// Extra header IDs.
	//
	// IDs 0..31 are reserved for official use by PKWARE.
	// IDs above that range are defined by third-party vendors.
	// Since ZIP lacked high precision timestamps (nor an official specification
	// of the timezone used for the date fields), many competing extra fields
	// have been invented. Pervasive use effectively makes them "official".
	//
	// See http://mdfs.net/Docs/Comp/Archiving/Zip/ExtraField
	zip64ExtraID       = 0x0001 // Zip64 extended information
	ntfsExtraID        = 0x000a // NTFS
	unixExtraID        = 0x000d // UNIX
	extTimeExtraID     = 0x5455 // Extended timestamp
	infoZipUnixExtraID = 0x5855 // Info-ZIP Unix extension
)

// ZipTOCSize reports, as a function of the total zip file size and a small
// suffix of the file, the total suffix length needed to be able to read the
// zip's full Table of Contents (TOC).
//
// A valid zip file's footer (its "End of Central Directory") is usually in the
// final 22 bytes of the file, so a 22 byte zipFooter if often but not always
// sufficient. The Go standard library does a 1 KiB suffix read, followed by a
// 65 KiB read, and looks no further.
//
// The function reports ok=false if it couldn't find a valid EOCD in the footer
// buffer provided. Callers can either pass in 65 KiB to start, or first try 22
// bytes followed by 1 KiB, followed by 65 KiB. The provided buffer can be any
// size, including the full size of the file.
//
// When the footer is found, the function reports ok=true and the total suffix
// size needed to read the zip's table of contents. That size includes the
// length of the footer itself.
func ZipTOCSize(size int64, zipFooter []byte) (tocSize int64, ok bool) {
	numFakeZeros := size - int64(len(zipFooter))
	if numFakeZeros < 0 {
		// Invalid arguments to function.
		return 0, false
	}
	ra := readerutil.NewMultiReaderAt(
		readerutil.ZeroSizeReaderAt(numFakeZeros),
		bytes.NewReader(zipFooter),
	)
	end, baseOffset, err := readDirectoryEnd(ra, size)
	if err != nil {
		return 0, false
	}
	tocStartOff := baseOffset + int64(end.directoryOffset)
	tocSize = size - tocStartOff
	if tocSize < 0 || tocSize > size {
		// Something went wrong above.
		return 0, false
	}
	return tocSize, true
}

// Reader is the result of [ParseTOC].
type Reader struct {
	// File are the files in the zip file.
	File []*FileHeader

	// BaseOffset is where in the zip file the zip contents
	// begin. This is often zero, but some zip files have
	// prefixes (such as shell scripts).
	BaseOffset int64

	// Comment is the optional comment from the end of a zip file.
	Comment string
}

// ParseTOC parses the table of contents from a zip file with
// the provided size.
func ParseTOC(size int64, toc []byte) (*Reader, error) {
	numFakeZeros := size - int64(len(toc))
	if numFakeZeros < 0 {
		return nil, errors.New("invalid arguments to ParseTOC")
	}
	ra := readerutil.NewMultiReaderAt(
		readerutil.ZeroSizeReaderAt(numFakeZeros),
		bytes.NewReader(toc),
	)
	end, baseOffset, err := readDirectoryEnd(ra, size)
	if err != nil {
		return nil, err
	}
	r := new(Reader)
	r.BaseOffset = baseOffset
	r.Comment = end.comment

	rs := io.NewSectionReader(ra, 0, size)
	if _, err = rs.Seek(r.BaseOffset+int64(end.directoryOffset), io.SeekStart); err != nil {
		return nil, err
	}
	buf := bufio.NewReader(rs)
	for {
		f := &FileHeader{Reader: r}
		err = readDirectoryHeader(f, buf)
		if err == zip.ErrFormat || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, err
		}
		f.HeaderOffset += r.BaseOffset
		r.File = append(r.File, f)
	}
	if uint16(len(r.File)) != uint16(end.directoryRecords) { // only compare 16 bits here
		// Return the readDirectoryHeader error if we read
		// the wrong number of directory entries.
		return nil, fmt.Errorf("zip: wrong number of directory entries: got %d, want %d (mod uint16)", len(r.File), end.directoryRecords)
	}
	return r, nil
}

func readDirectoryEnd(r io.ReaderAt, size int64) (dir *directoryEnd, baseOffset int64, err error) {
	// look for directoryEndSignature in the last 1k, then in the last 65k
	var buf []byte
	var directoryEndOffset int64
	for i, bLen := range []int64{1024, 65 * 1024} {
		if bLen > size {
			bLen = size
		}
		buf = make([]byte, int(bLen))
		if _, err := r.ReadAt(buf, size-bLen); err != nil && err != io.EOF {
			return nil, 0, err
		}
		if p := findSignatureInBlock(buf); p >= 0 {
			buf = buf[p:]
			directoryEndOffset = size - bLen + int64(p)
			break
		}
		if i == 1 || bLen == size {
			return nil, 0, errFormat
		}
	}

	// read header into struct
	b := readBuf(buf[4:]) // skip signature
	d := &directoryEnd{
		diskNbr:            uint32(b.uint16()),
		dirDiskNbr:         uint32(b.uint16()),
		dirRecordsThisDisk: uint64(b.uint16()),
		directoryRecords:   uint64(b.uint16()),
		directorySize:      uint64(b.uint32()),
		directoryOffset:    uint64(b.uint32()),
		commentLen:         b.uint16(),
	}
	l := int(d.commentLen)
	if l > len(b) {
		return nil, 0, errors.New("zip: invalid comment length")
	}
	d.comment = string(b[:l])

	// These values mean that the file can be a zip64 file
	if d.directoryRecords == 0xffff || d.directorySize == 0xffff || d.directoryOffset == 0xffffffff {
		p, err := findDirectory64End(r, directoryEndOffset)
		if err == nil && p >= 0 {
			directoryEndOffset = p
			err = readDirectory64End(r, p, d)
		}
		if err != nil {
			return nil, 0, err
		}
	}

	maxInt64 := uint64(1<<63 - 1)
	if d.directorySize > maxInt64 || d.directoryOffset > maxInt64 {
		return nil, 0, errFormat
	}

	baseOffset = directoryEndOffset - int64(d.directorySize) - int64(d.directoryOffset)

	// Make sure directoryOffset points to somewhere in our file.
	if o := baseOffset + int64(d.directoryOffset); o < 0 || o >= size {
		return nil, 0, errFormat
	}

	// If the directory end data tells us to use a non-zero baseOffset,
	// but we would find a valid directory entry if we assume that the
	// baseOffset is 0, then just use a baseOffset of 0.
	// We've seen files in which the directory end data gives us
	// an incorrect baseOffset.
	if baseOffset > 0 {
		off := int64(d.directoryOffset)
		rs := io.NewSectionReader(r, off, size-off)
		if readDirectoryHeader(new(FileHeader), rs) == nil {
			baseOffset = 0
		}
	}

	return d, baseOffset, nil
}

// FileHeader describes a file within a ZIP file.
// See the [ZIP specification] for details.
//
// [ZIP specification]: https://support.pkware.com/pkzip/appnote
type FileHeader struct {
	// Name is the name of the file.
	//
	// It must be a relative path, not start with a drive letter (such as "C:"),
	// and must use forward slashes instead of back slashes. A trailing slash
	// indicates that this file is a directory and should have no data.
	Name string

	// Comment is any arbitrary user-defined string shorter than 64KiB.
	Comment string

	// NonUTF8 indicates that Name and Comment are not encoded in UTF-8.
	//
	// By specification, the only other encoding permitted should be CP-437,
	// but historically many ZIP readers interpret Name and Comment as whatever
	// the system's local character encoding happens to be.
	//
	// This flag should only be set if the user intends to encode a non-portable
	// ZIP file for a specific localized region. Otherwise, the Writer
	// automatically sets the ZIP format's UTF-8 flag for valid UTF-8 strings.
	NonUTF8 bool

	CreatorVersion uint16
	ReaderVersion  uint16
	Flags          uint16

	// Method is the compression method. If zero, Store is used.
	Method uint16

	// Modified is the modified time of the file.
	//
	// When reading, an extended timestamp is preferred over the legacy MS-DOS
	// date field, and the offset between the times is used as the timezone.
	// If only the MS-DOS date is present, the timezone is assumed to be UTC.
	//
	// When writing, an extended timestamp (which is timezone-agnostic) is
	// always emitted. The legacy MS-DOS date field is encoded according to the
	// location of the Modified time.
	Modified time.Time

	// ModifiedTime is an MS-DOS-encoded time.
	//
	// Deprecated: Use Modified instead.
	ModifiedTime uint16

	// ModifiedDate is an MS-DOS-encoded date.
	//
	// Deprecated: Use Modified instead.
	ModifiedDate uint16

	// CRC32 is the CRC32 checksum of the file content.
	CRC32 uint32

	// CompressedSize is the compressed size of the file in bytes.
	// If either the uncompressed or compressed size of the file
	// does not fit in 32 bits, CompressedSize is set to ^uint32(0).
	//
	// Deprecated: Use CompressedSize64 instead.
	CompressedSize uint32

	// UncompressedSize is the uncompressed size of the file in bytes.
	// If either the uncompressed or compressed size of the file
	// does not fit in 32 bits, UncompressedSize is set to ^uint32(0).
	//
	// Deprecated: Use UncompressedSize64 instead.
	UncompressedSize uint32

	// CompressedSize64 is the compressed size of the file in bytes.
	CompressedSize64 uint64

	// UncompressedSize64 is the uncompressed size of the file in bytes.
	UncompressedSize64 uint64

	Extra         []byte
	ExternalAttrs uint32 // Meaning depends on CreatorVersion

	// Reader is the Reader which parsed this header.
	Reader *Reader

	// HeaderOffset is where in the zip file the local file header
	// for this file is located.
	HeaderOffset int64
}

// readDirectoryHeader attempts to read a directory header from r.
// It returns io.ErrUnexpectedEOF if it cannot read a complete header,
// and ErrFormat if it doesn't find a valid header signature.
func readDirectoryHeader(f *FileHeader, r io.Reader) error {
	var buf [directoryHeaderLen]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return err
	}
	b := readBuf(buf[:])
	if sig := b.uint32(); sig != directoryHeaderSignature {
		return errFormat
	}
	f.CreatorVersion = b.uint16()
	f.ReaderVersion = b.uint16()
	f.Flags = b.uint16()
	f.Method = b.uint16()
	f.ModifiedTime = b.uint16()
	f.ModifiedDate = b.uint16()
	f.CRC32 = b.uint32()
	f.CompressedSize = b.uint32()
	f.UncompressedSize = b.uint32()
	f.CompressedSize64 = uint64(f.CompressedSize)
	f.UncompressedSize64 = uint64(f.UncompressedSize)
	filenameLen := int(b.uint16())
	extraLen := int(b.uint16())
	commentLen := int(b.uint16())
	b = b[4:] // skipped start disk number and internal attributes (2x uint16)
	f.ExternalAttrs = b.uint32()
	f.HeaderOffset = int64(b.uint32())
	d := make([]byte, filenameLen+extraLen+commentLen)
	if _, err := io.ReadFull(r, d); err != nil {
		return err
	}
	f.Name = string(d[:filenameLen])
	f.Extra = d[filenameLen : filenameLen+extraLen]
	f.Comment = string(d[filenameLen+extraLen:])

	// Determine the character encoding.
	utf8Valid1, utf8Require1 := detectUTF8(f.Name)
	utf8Valid2, utf8Require2 := detectUTF8(f.Comment)
	switch {
	case !utf8Valid1 || !utf8Valid2:
		// Name and Comment definitely not UTF-8.
		f.NonUTF8 = true
	case !utf8Require1 && !utf8Require2:
		// Name and Comment use only single-byte runes that overlap with UTF-8.
		f.NonUTF8 = false
	default:
		// Might be UTF-8, might be some other encoding; preserve existing flag.
		// Some ZIP writers use UTF-8 encoding without setting the UTF-8 flag.
		// Since it is impossible to always distinguish valid UTF-8 from some
		// other encoding (e.g., GBK or Shift-JIS), we trust the flag.
		f.NonUTF8 = f.Flags&0x800 == 0
	}

	needUSize := f.UncompressedSize == ^uint32(0)
	needCSize := f.CompressedSize == ^uint32(0)
	needHeaderOffset := f.HeaderOffset == int64(^uint32(0))

	// Best effort to find what we need.
	// Other zip authors might not even follow the basic format,
	// and we'll just ignore the Extra content in that case.
	var modified time.Time
parseExtras:
	for extra := readBuf(f.Extra); len(extra) >= 4; { // need at least tag and size
		fieldTag := extra.uint16()
		fieldSize := int(extra.uint16())
		if len(extra) < fieldSize {
			break
		}
		fieldBuf := extra.sub(fieldSize)

		switch fieldTag {
		case zip64ExtraID:

			// update directory values from the zip64 extra block.
			// They should only be consulted if the sizes read earlier
			// are maxed out.
			// See golang.org/issue/13367.
			if needUSize {
				needUSize = false
				if len(fieldBuf) < 8 {
					return errFormat
				}
				f.UncompressedSize64 = fieldBuf.uint64()
			}
			if needCSize {
				needCSize = false
				if len(fieldBuf) < 8 {
					return errFormat
				}
				f.CompressedSize64 = fieldBuf.uint64()
			}
			if needHeaderOffset {
				needHeaderOffset = false
				if len(fieldBuf) < 8 {
					return errFormat
				}
				f.HeaderOffset = int64(fieldBuf.uint64())
			}
		case ntfsExtraID:
			if len(fieldBuf) < 4 {
				continue parseExtras
			}
			fieldBuf.uint32()        // reserved (ignored)
			for len(fieldBuf) >= 4 { // need at least tag and size
				attrTag := fieldBuf.uint16()
				attrSize := int(fieldBuf.uint16())
				if len(fieldBuf) < attrSize {
					continue parseExtras
				}
				attrBuf := fieldBuf.sub(attrSize)
				if attrTag != 1 || attrSize != 24 {
					continue // Ignore irrelevant attributes
				}

				const ticksPerSecond = 1e7    // Windows timestamp resolution
				ts := int64(attrBuf.uint64()) // ModTime since Windows epoch
				secs := ts / ticksPerSecond
				nsecs := (1e9 / ticksPerSecond) * (ts % ticksPerSecond)
				epoch := time.Date(1601, time.January, 1, 0, 0, 0, 0, time.UTC)
				modified = time.Unix(epoch.Unix()+secs, nsecs)
			}
		case unixExtraID, infoZipUnixExtraID:
			if len(fieldBuf) < 8 {
				continue parseExtras
			}
			fieldBuf.uint32()              // AcTime (ignored)
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		case extTimeExtraID:
			if len(fieldBuf) < 5 || fieldBuf.uint8()&1 == 0 {
				continue parseExtras
			}
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		}
	}

	msdosModified := msDosTimeToTime(f.ModifiedDate, f.ModifiedTime)
	f.Modified = msdosModified
	if !modified.IsZero() {
		f.Modified = modified.UTC()

		// If legacy MS-DOS timestamps are set, we can use the delta between
		// the legacy and extended versions to estimate timezone offset.
		//
		// A non-UTC timezone is always used (even if offset is zero).
		// Thus, FileHeader.Modified.Location() == time.UTC is useful for
		// determining whether extended timestamps are present.
		// This is necessary for users that need to do additional time
		// calculations when dealing with legacy ZIP formats.
		if f.ModifiedTime != 0 || f.ModifiedDate != 0 {
			f.Modified = modified.In(timeZone(msdosModified.Sub(modified)))
		}
	}

	// Assume that uncompressed size 2³²-1 could plausibly happen in
	// an old zip32 file that was sharding inputs into the largest chunks
	// possible (or is just malicious; search the web for 42.zip).
	// If needUSize is true still, it means we didn't see a zip64 extension.
	// As long as the compressed size is not also 2³²-1 (implausible)
	// and the header is not also 2³²-1 (equally implausible),
	// accept the uncompressed size 2³²-1 as valid.
	// If nothing else, this keeps archive/zip working with 42.zip.
	_ = needUSize

	if needCSize || needHeaderOffset {
		return errFormat
	}

	return nil
}

// findDirectory64End tries to read the zip64 locator just before the
// directory end and returns the offset of the zip64 directory end if
// found.
func findDirectory64End(r io.ReaderAt, directoryEndOffset int64) (int64, error) {
	locOffset := directoryEndOffset - directory64LocLen
	if locOffset < 0 {
		return -1, nil // no need to look for a header outside the file
	}
	buf := make([]byte, directory64LocLen)
	if _, err := r.ReadAt(buf, locOffset); err != nil {
		return -1, err
	}
	b := readBuf(buf)
	if sig := b.uint32(); sig != directory64LocSignature {
		return -1, nil
	}
	if b.uint32() != 0 { // number of the disk with the start of the zip64 end of central directory
		return -1, nil // the file is not a valid zip64-file
	}
	p := b.uint64()      // relative offset of the zip64 end of central directory record
	if b.uint32() != 1 { // total number of disks
		return -1, nil // the file is not a valid zip64-file
	}
	return int64(p), nil
}

// readDirectory64End reads the zip64 directory end and updates the
// directory end with the zip64 directory end values.
func readDirectory64End(r io.ReaderAt, offset int64, d *directoryEnd) (err error) {
	buf := make([]byte, directory64EndLen)
	if _, err := r.ReadAt(buf, offset); err != nil {
		return err
	}

	b := readBuf(buf)
	if sig := b.uint32(); sig != directory64EndSignature {
		return errFormat
	}

	b = b[12:]                        // skip dir size, version and version needed (uint64 + 2x uint16)
	d.diskNbr = b.uint32()            // number of this disk
	d.dirDiskNbr = b.uint32()         // number of the disk with the start of the central directory
	d.dirRecordsThisDisk = b.uint64() // total number of entries in the central directory on this disk
	d.directoryRecords = b.uint64()   // total number of entries in the central directory
	d.directorySize = b.uint64()      // size of the central directory
	d.directoryOffset = b.uint64()    // offset of start of central directory with respect to the starting disk number

	return nil
}

func findSignatureInBlock(b []byte) int {
	for i := len(b) - directoryEndLen; i >= 0; i-- {
		// defined from directoryEndSignature in struct.go
		if b[i] == 'P' && b[i+1] == 'K' && b[i+2] == 0x05 && b[i+3] == 0x06 {
			// n is length of comment
			n := int(b[i+directoryEndLen-2]) | int(b[i+directoryEndLen-1])<<8
			if n+directoryEndLen+i > len(b) {
				// Truncated comment.
				// Some parsers (such as Info-ZIP) ignore the truncated comment
				// rather than treating it as a hard error.
				return -1
			}
			return i
		}
	}
	return -1
}

type readBuf []byte

func (b *readBuf) uint8() uint8 {
	v := (*b)[0]
	*b = (*b)[1:]
	return v
}

func (b *readBuf) uint16() uint16 {
	v := binary.LittleEndian.Uint16(*b)
	*b = (*b)[2:]
	return v
}

func (b *readBuf) uint32() uint32 {
	v := binary.LittleEndian.Uint32(*b)
	*b = (*b)[4:]
	return v
}

func (b *readBuf) uint64() uint64 {
	v := binary.LittleEndian.Uint64(*b)
	*b = (*b)[8:]
	return v
}

func (b *readBuf) sub(n int) readBuf {
	b2 := (*b)[:n]
	*b = (*b)[n:]
	return b2
}

type directoryEnd struct {
	diskNbr            uint32 // unused
	dirDiskNbr         uint32 // unused
	dirRecordsThisDisk uint64 // unused
	directoryRecords   uint64
	directorySize      uint64
	directoryOffset    uint64 // relative to file
	commentLen         uint16
	comment            string
}

// msDosTimeToTime converts an MS-DOS date and time into a time.Time.
// The resolution is 2s.
// See: https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-dosdatetimetofiletime
func msDosTimeToTime(dosDate, dosTime uint16) time.Time {
	return time.Date(
		// date bits 0-4: day of month; 5-8: month; 9-15: years since 1980
		int(dosDate>>9+1980),
		time.Month(dosDate>>5&0xf),
		int(dosDate&0x1f),

		// time bits 0-4: second/2; 5-10: minute; 11-15: hour
		int(dosTime>>11),
		int(dosTime>>5&0x3f),
		int(dosTime&0x1f*2),
		0, // nanoseconds

		time.UTC,
	)
}

// detectUTF8 reports whether s is a valid UTF-8 string, and whether the string
// must be considered UTF-8 encoding (i.e., not compatible with CP-437, ASCII,
// or any other common encoding).
func detectUTF8(s string) (valid, require bool) {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		// Officially, ZIP uses CP-437, but many readers use the system's
		// local character encoding. Most encoding are compatible with a large
		// subset of CP-437, which itself is ASCII-like.
		//
		// Forbid 0x7e and 0x5c since EUC-KR and Shift-JIS replace those
		// characters with localized currency and overline characters.
		if r < 0x20 || r > 0x7d || r == 0x5c {
			if !utf8.ValidRune(r) || (r == utf8.RuneError && size == 1) {
				return false, false
			}
			require = true
		}
	}
	return true, require
}

// timeZone returns a *time.Location based on the provided offset.
// If the offset is non-sensible, then this uses an offset of zero.
func timeZone(offset time.Duration) *time.Location {
	const (
		minOffset   = -12 * time.Hour  // E.g., Baker island at -12:00
		maxOffset   = +14 * time.Hour  // E.g., Line island at +14:00
		offsetAlias = 15 * time.Minute // E.g., Nepal at +5:45
	)
	offset = offset.Round(offsetAlias)
	if offset < minOffset || maxOffset < offset {
		offset = 0
	}
	return time.FixedZone("", int(offset/time.Second))
}

// openReadSlop is how many extra bytes OpenWithReader should add to its size
// estimation to account for possible differences between the expected and
// actual size of the underlying reader, since the fileHeader length can't be
// known exactly.
const openReadSlop = 512

// OpenWithReader opens a zip.File using an alterate reader for the compressed
// data.
//
// For example, you can use this (along with [ZipTOCSize]), to do one or two
// HTTP Range requests against a remote zip file to first read its TOC, then use
// this function to read individual files within the zip without downloading the
// entire zip file. The getRawReader function would then do an HTTP Range
// request for the given offset and size.
//
// Callers are responsible for closing the returned io.ReadCloser, which then
// also closes the rawReader returned by getRawReader.
func OpenWithReader(h *FileHeader, getRawReader func(offsize, size int64) (rawReader io.ReadCloser, err error)) (io.ReadCloser, error) {
	var decomp zip.Decompressor
	switch h.Method {
	case zip.Store:
		decomp = io.NopCloser
	case zip.Deflate:
		decomp = newFlateReader
	default:
		return nil, fmt.Errorf("unsupported storage method %d", h.Method)
	}

	compSize := int64(h.CompressedSize64)
	descSize := int64(0)
	if h.hasDataDesc() {
		descSize = dataDescriptorLen
	}
	readSize := compSize + descSize

	// Pad length for fileHeader, filename, extras, and a bit of slop space, in
	// case the local file header's name/extras are longer (very unlikely)
	readSize += fileHeaderLen
	fileHeaderVariableLen := int64(len(h.Name)) + int64(len(h.Extra)) + openReadSlop
	readSize += fileHeaderVariableLen

	rawReader, err := getRawReader(h.HeaderOffset, readSize)
	if err != nil {
		return nil, fmt.Errorf("opening raw reader: %w", err)
	}

	// Skip over the local file header to get to the body.
	if variableLen, err := skipHeader(rawReader); err != nil {
		rawReader.Close()
		return nil, err
	} else if variableLen > fileHeaderVariableLen {
		rawReader.Close()
		return nil, fmt.Errorf("file header length of %d was greater than expected %d", variableLen, fileHeaderVariableLen)
	}

	var desr io.Reader // or nil if none
	if h.hasDataDesc() {
		desr = rawReader
	}

	decompressReader := decomp(io.LimitReader(rawReader, compSize))
	return &checksumReader{
		rawRC:      rawReader,
		bodyReader: decompressReader,
		hash:       crc32.NewIEEE(),
		f:          h,
		desr:       desr,
	}, nil
}

var flateReaderPool sync.Pool

func newFlateReader(r io.Reader) io.ReadCloser {
	fr, ok := flateReaderPool.Get().(io.ReadCloser)
	if ok {
		fr.(flate.Resetter).Reset(r, nil)
	} else {
		fr = flate.NewReader(r)
	}
	return &pooledFlateReader{fr: fr}
}

type pooledFlateReader struct {
	mu sync.Mutex // guards Close and Read
	fr io.ReadCloser
}

func (r *pooledFlateReader) Read(p []byte) (n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fr == nil {
		return 0, errors.New("Read after Close")
	}
	return r.fr.Read(p)
}

func (r *pooledFlateReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	if r.fr != nil {
		err = r.fr.Close()
		flateReaderPool.Put(r.fr)
		r.fr = nil
	}
	return err
}

func (h *FileHeader) hasDataDesc() bool {
	return h.Flags&0x8 != 0
}

// skipHeader skips over the local file header and returns the length of
// the variable length fields (filename and extra) that were skipped,
// not including the fixed 30 byte header.
func skipHeader(r io.Reader) (variableLen int64, err error) {
	var buf [fileHeaderLen]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	b := readBuf(buf[:])
	if sig := b.uint32(); sig != fileHeaderSignature {
		return 0, zip.ErrFormat
	}
	b = b[22:] // skip over most of the header
	filenameLen := b.uint16()
	extraLen := b.uint16()
	variableLen = int64(filenameLen) + int64(extraLen)
	if _, err := io.CopyN(io.Discard, r, variableLen); err != nil {
		return 0, err
	}
	return variableLen, nil
}

type checksumReader struct {
	bodyReader io.ReadCloser
	rawRC      io.ReadCloser // the getRawReader result from OpenReader (e.g. http.Response.Body)
	hash       hash.Hash32
	nread      uint64    // number of bytes read so far
	desr       io.Reader // if non-nil, where to read data descriptor
	f          *FileHeader
	err        error // sticky error
}

func (r *checksumReader) setErr(err error) error {
	if r.err != nil {
		return r.err
	}
	if err == nil {
		return nil
	}
	r.err = err
	r.rawRC.Close()
	return err
}

func (r *checksumReader) Read(b []byte) (n int, err error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err = r.bodyReader.Read(b)
	r.hash.Write(b[:n])
	r.nread += uint64(n)
	if r.nread > r.f.UncompressedSize64 {
		return 0, r.setErr(zip.ErrFormat)
	}
	if err == nil {
		return
	}
	if err == io.EOF {
		if r.nread != r.f.UncompressedSize64 {
			return 0, r.setErr(io.ErrUnexpectedEOF)
		}
		if r.desr != nil {
			if err1 := readDataDescriptor(r.desr, r.f); err1 != nil {
				if err1 == io.EOF {
					err = io.ErrUnexpectedEOF
				} else {
					err = err1
				}
			} else if r.hash.Sum32() != r.f.CRC32 {
				err = zip.ErrChecksum
			}
		} else {
			// If there's not a data descriptor, we still compare
			// the CRC32 of what we've read against the file header
			// or TOC's CRC32, if it seems like it was set.
			if r.f.CRC32 != 0 && r.hash.Sum32() != r.f.CRC32 {
				err = zip.ErrChecksum
			}
		}
	}
	return n, r.setErr(err)
}

func (r *checksumReader) Close() error {
	// In case the rawRC was an http.Response.Body, make a best effort attempt
	// to read the final [openReadSlop]] bytes we might not've needed. This
	// increases the change that the HTTP connection can be reused, at least
	// with HTTP/1.1, which doesn't really help my motivating example (Google
	// Takeout + Google Drive, which are HTTP/2+, where this optimization
	// doesn't matter)
	io.CopyN(io.Discard, r.rawRC, openReadSlop)
	r.rawRC.Close()
	return r.bodyReader.Close()
}

func readDataDescriptor(r io.Reader, f *FileHeader) error {
	var buf [dataDescriptorLen]byte
	// The spec says: "Although not originally assigned a
	// signature, the value 0x08074b50 has commonly been adopted
	// as a signature value for the data descriptor record.
	// Implementers should be aware that ZIP files may be
	// encountered with or without this signature marking data
	// descriptors and should account for either case when reading
	// ZIP files to ensure compatibility."
	//
	// dataDescriptorLen includes the size of the signature but
	// first read just those 4 bytes to see if it exists.
	if _, err := io.ReadFull(r, buf[:4]); err != nil {
		return err
	}
	off := 0
	maybeSig := readBuf(buf[:4])
	if maybeSig.uint32() != dataDescriptorSignature {
		// No data descriptor signature. Keep these four
		// bytes.
		off += 4
	}
	if _, err := io.ReadFull(r, buf[off:12]); err != nil {
		return err
	}
	b := readBuf(buf[:12])
	if b.uint32() != f.CRC32 {
		return zip.ErrChecksum
	}

	// The two sizes that follow here can be either 32 bits or 64 bits
	// but the spec is not very clear on this and different
	// interpretations has been made causing incompatibilities. We
	// already have the sizes from the central directory so we can
	// just ignore these.

	return nil
}
