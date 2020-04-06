/*
Copyright 2020 The Go4 AUTHORS

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package mem provides access to bytes in memory.
package mem

import (
	"strconv"
	"strings"
	"unsafe"
)

// RO is a read-only view of some bytes of memory. It may be be backed
// by a string or []byte. Notably, unlike a string, the memory is not
// guaranteed to be immutable. While the length is fixed, the
// underlying bytes might change if interleaved with code that's
// modifying the underlying memory.
type RO struct {
	_ [0]func() // not comparable; don't want to be a map key or support ==
	m unsafeString
}

func (r RO) Len() int                  { return len(r.m) }
func (r RO) At(i int) byte             { return r.m[i] }
func (r RO) Slice(from, to int) RO     { return RO{m: r.m[from:to]} }
func (r RO) SliceFrom(from int) RO     { return RO{m: r.m[from:]} }
func (r RO) SliceTo(to int) RO         { return RO{m: r.m[:to]} }
func (r RO) Copy(dest []byte) int      { return copy(dest, r.m) }
func (r RO) Append(dest []byte) []byte { return append(dest, r.m...) }
func (r RO) Equal(r2 RO) bool          { return r.m == r2.m }
func (r RO) EqualString(s string) bool { return string(r.m) == s }
func (r RO) EqualBytes(b []byte) bool  { return string(r.m) == string(b) }
func (r RO) Reader() *Reader           { return &Reader{sr: strings.NewReader(string(r.m))} }

func (r RO) ParseInt(base, bitSize int) (int64, error) {
	return strconv.ParseInt(string(r.m), base, bitSize)
}
func (r RO) ParseUint(base, bitSize int) (uint64, error) {
	return strconv.ParseUint(string(r.m), base, bitSize)
}

// Reader is like a bytes.Reader or strings.Reader.
type Reader struct {
	sr *strings.Reader
}

func (r *Reader) Len() int                                     { return r.sr.Len() }
func (r *Reader) Size() int64                                  { return r.sr.Size() }
func (r *Reader) Read(b []byte) (int, error)                   { return r.sr.Read(b) }
func (r *Reader) ReadAt(b []byte, off int64) (int, error)      { return r.sr.ReadAt(b, off) }
func (r *Reader) ReadByte() (byte, error)                      { return r.sr.ReadByte() }
func (r *Reader) ReadRune() (ch rune, size int, err error)     { return r.sr.ReadRune() }
func (r *Reader) Seek(offset int64, whence int) (int64, error) { return r.sr.Seek(offset, whence) }

// TODO: add Reader.WriteTo, but don't use strings.Reader.WriteTo because it uses io.WriteString, leaking our unsafe string

// unsafeString is a string that's not really a Go string.
// It might be pointing into a []byte. Don't let it escape to callers.
// We contain the unsafety to this package.
type unsafeString string

// S returns a read-only view of the string s.
func S(s string) RO { return RO{m: unsafeString(s)} }

// B returns a read-only view of the byte slice b.
func B(b []byte) RO {
	if len(b) == 0 {
		return RO{m: ""}
	}
	type stringHeader struct {
		P   *byte
		Len int
	}
	return RO{m: *(*unsafeString)(unsafe.Pointer(&stringHeader{&b[0], len(b)}))}
}
