/*
Copyright 2026 The go4 Authors

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

package bmff

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"reflect"
	"testing"
)

// mkbox returns a box of type typ with the concatenated bodies.
func mkbox(typ string, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(b)))
	out = append(out, typ...)
	return append(out, b...)
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// fullHeader is a FullBox version and flags header.
func fullHeader(version uint8) []byte { return []byte{version, 0, 0, 0} }

func mvhdV0(timeScale, duration uint32) []byte {
	return mkbox("mvhd", fullHeader(0), u32(1), u32(2), u32(timeScale), u32(duration), make([]byte, 80))
}

func dataBox(typ uint32, val []byte) []byte {
	return mkbox("data", u32(typ), u32(0), val)
}

func keyEntry(name string) []byte {
	return append(u32(uint32(8+len(name))), "mdta"+name...)
}

// quickTimeMeta returns a QuickTime-style meta box (without a FullBox
// header) with an empty, unterminated handler name.
func quickTimeMeta() []byte {
	return mkbox("meta",
		mkbox("hdlr", fullHeader(0), u32(0), []byte("mdta"), make([]byte, 12)),
		mkbox("keys", fullHeader(0), u32(2),
			keyEntry("com.apple.quicktime.content.identifier"),
			keyEntry("com.apple.quicktime.live-photo.auto")),
		mkbox("ilst",
			mkbox(string(u32(1)), dataBox(1, []byte("7C16BD72-9110-4AB6-80F6-6BF098D5F43E"))),
			mkbox(string(u32(2)), dataBox(22, []byte{1}))),
	)
}

// readBoxes reads all boxes from r. Boxes must be parsed before the
// next ReadBox call, so it parses those of known type, saving any
// parse errors for parse to report.
func readBoxes(t *testing.T, r *Reader) []Box {
	t.Helper()
	var boxes []Box
	for {
		b, err := r.ReadBox()
		if err == io.EOF {
			return boxes
		}
		if err != nil {
			t.Fatalf("ReadBox: %v", err)
		}
		if _, err := b.Parse(); err != nil && err != ErrUnknownBox {
			b = parseErrBox{b, err}
		}
		boxes = append(boxes, b)
	}
}

// parseErrBox is a Box whose Parse returns err.
type parseErrBox struct {
	Box
	err error
}

func (b parseErrBox) Parse() (Box, error) { return nil, b.err }

func parse(t *testing.T, b Box) Box {
	t.Helper()
	pb, err := b.Parse()
	if err != nil {
		t.Fatalf("parsing %q: %v", b.Type(), err)
	}
	return pb
}

func TestQuickTimeMeta(t *testing.T) {
	moovBody := append(mvhdV0(600, 444), quickTimeMeta()...)
	boxes := readBoxes(t, NewReader(bytes.NewReader(moovBody)))
	if len(boxes) != 2 {
		t.Fatalf("got %d boxes; want 2", len(boxes))
	}

	mh := parse(t, boxes[0]).(*MovieHeaderBox)
	if mh.TimeScale != 600 || mh.Duration != 444 || mh.CreationTime != 1 || mh.ModificationTime != 2 {
		t.Errorf("mvhd = %+v", mh)
	}

	mb := parse(t, boxes[1]).(*MetaBox)
	if !mb.QuickTime {
		t.Error("QuickTime = false; want true")
	}
	if len(mb.Children) != 3 {
		t.Fatalf("got %d meta children; want 3", len(mb.Children))
	}
	hb := parse(t, mb.Children[0]).(*HandlerBox)
	if hb.HandlerType != "mdta" || hb.Name != "" {
		t.Errorf("hdlr = %q, %q", hb.HandlerType, hb.Name)
	}
	kb := parse(t, mb.Children[1]).(*KeysBox)
	wantKeys := []MetadataKey{
		{"mdta", "com.apple.quicktime.content.identifier"},
		{"mdta", "com.apple.quicktime.live-photo.auto"},
	}
	if !reflect.DeepEqual(kb.Keys, wantKeys) {
		t.Errorf("keys = %q; want %q", kb.Keys, wantKeys)
	}
	ilb := parse(t, mb.Children[2]).(*ItemListBox)
	if len(ilb.Items) != 2 {
		t.Fatalf("got %d items; want 2", len(ilb.Items))
	}
	for i, want := range []struct {
		dataType uint32
		val      string
	}{
		{1, "7C16BD72-9110-4AB6-80F6-6BF098D5F43E"},
		{22, "\x01"},
	} {
		it := ilb.Items[i]
		if it.KeyIndex != uint32(i+1) || len(it.Data) != 1 {
			t.Fatalf("item %d: KeyIndex %d, %d data boxes", i, it.KeyIndex, len(it.Data))
		}
		if d := it.Data[0]; d.DataType != want.dataType || string(d.Value) != want.val {
			t.Errorf("item %d data = %d, %q; want %d, %q", i, d.DataType, d.Value, want.dataType, want.val)
		}
	}
}

func TestISOMetaIsFullBox(t *testing.T) {
	meta := mkbox("meta", fullHeader(0), mkbox("hdlr", fullHeader(0), u32(0), []byte("pict"), make([]byte, 12), []byte("name\x00")))
	boxes := readBoxes(t, NewReader(bytes.NewReader(meta)))
	mb := parse(t, boxes[0]).(*MetaBox)
	if mb.QuickTime {
		t.Error("QuickTime = true; want false")
	}
	if len(mb.Children) != 1 {
		t.Fatalf("got %d children; want 1", len(mb.Children))
	}
	if hb := parse(t, mb.Children[0]).(*HandlerBox); hb.HandlerType != "pict" || hb.Name != "name" {
		t.Errorf("hdlr = %q, %q", hb.HandlerType, hb.Name)
	}
}

func TestMovieHeaderV1(t *testing.T) {
	mvhd := mkbox("mvhd", fullHeader(1), u64(1<<33), u64(1<<34), u32(90000), u64(1<<35), make([]byte, 80))
	boxes := readBoxes(t, NewReader(bytes.NewReader(mvhd)))
	mh := parse(t, boxes[0]).(*MovieHeaderBox)
	if mh.CreationTime != 1<<33 || mh.ModificationTime != 1<<34 || mh.TimeScale != 90000 || mh.Duration != 1<<35 {
		t.Errorf("mvhd = %+v", mh)
	}
}

// sparseFile is an io.ReaderAt of size bytes, all zero except for
// head at the start and tail at the end. It counts bytes read.
type sparseFile struct {
	head, tail []byte
	size       int64
	read       int64
}

func (f *sparseFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.size {
		return 0, io.EOF
	}
	n := len(p)
	if rem := f.size - off; int64(n) > rem {
		n = int(rem)
	}
	tailOff := f.size - int64(len(f.tail))
	for i := range n {
		pos := off + int64(i)
		switch {
		case pos < int64(len(f.head)):
			p[i] = f.head[pos]
		case pos >= tailOff:
			p[i] = f.tail[pos-tailOff]
		default:
			p[i] = 0
		}
	}
	f.read += int64(n)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestReadBoxSeeksPastLargeBox(t *testing.T) {
	const mdatSize = 1 << 40
	ftyp := mkbox("ftyp", []byte("qt  "), u32(0), []byte("qt  "))
	mdatHeader := append(append(u32(1), "mdat"...), u64(mdatSize)...) // 64-bit size
	moov := mkbox("moov", mvhdV0(600, 444))
	head := append(ftyp, mdatHeader...)
	f := &sparseFile{
		head: head,
		tail: moov,
		size: int64(len(ftyp)) + mdatSize + int64(len(moov)),
	}
	boxes := readBoxes(t, NewReader(io.NewSectionReader(f, 0, f.size)))
	if len(boxes) != 3 {
		t.Fatalf("got %d boxes; want 3", len(boxes))
	}
	if f.read > 1<<20 {
		t.Errorf("read %d bytes; want to seek past mdat", f.read)
	}
	want := []struct {
		typ         string
		offset, hdr int64
	}{
		{"ftyp", 0, 8},
		{"mdat", int64(len(ftyp)), 16},
		{"moov", int64(len(ftyp)) + mdatSize, 8},
	}
	for i, w := range want {
		b := boxes[i]
		if !b.Type().EqualString(w.typ) || b.Offset() != w.offset || b.HeaderSize() != w.hdr {
			t.Errorf("box %d = %q at %d, header %d; want %q at %d, header %d",
				i, b.Type(), b.Offset(), b.HeaderSize(), w.typ, w.offset, w.hdr)
		}
	}
	moovBody := io.NewSectionReader(f, boxes[2].Offset()+boxes[2].HeaderSize(), boxes[2].Size()-boxes[2].HeaderSize())
	inner := readBoxes(t, NewReader(moovBody))
	if len(inner) != 1 || parse(t, inner[0]).(*MovieHeaderBox).Duration != 444 {
		t.Errorf("unexpected moov contents %v", inner)
	}
}

// onlyReader hides any io.Seeker implementation.
type onlyReader struct{ io.Reader }

func TestReadBoxUnseekable(t *testing.T) {
	in := append(mkbox("mdat", make([]byte, 100000)), mkbox("moov", mvhdV0(1, 2))...)
	boxes := readBoxes(t, NewReader(onlyReader{bytes.NewReader(in)}))
	if len(boxes) != 2 || !boxes[1].Type().EqualString("moov") || boxes[1].Offset() != 100008 {
		t.Fatalf("unexpected boxes %v", boxes)
	}
}

// unseekable claims to implement io.Seeker but always fails, like an
// *os.File for a pipe.
type unseekable struct{ io.Reader }

func (unseekable) Seek(int64, int) (int64, error) { return 0, os.ErrInvalid }

func TestReadBoxSeekFails(t *testing.T) {
	in := append(mkbox("mdat", make([]byte, 100000)), mkbox("moov", mvhdV0(1, 2))...)
	boxes := readBoxes(t, NewReader(unseekable{bytes.NewReader(in)}))
	if len(boxes) != 2 || !boxes[1].Type().EqualString("moov") {
		t.Fatalf("unexpected boxes %v", boxes)
	}
}

func TestKeysBoxTruncated(t *testing.T) {
	for _, keys := range [][]byte{
		mkbox("keys", fullHeader(0), u32(3), keyEntry("a")),                   // fewer entries than count
		mkbox("keys", fullHeader(0), u32(1), u32(1<<31), []byte("mdtashort")), // huge claimed size
		mkbox("keys", fullHeader(0), u32(1), u32(4)),                          // size below minimum
	} {
		boxes := readBoxes(t, NewReader(bytes.NewReader(keys)))
		if _, err := boxes[0].Parse(); err == nil {
			t.Errorf("Parse(%q) succeeded; want error", keys)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(append(mvhdV0(600, 444), quickTimeMeta()...))
	f.Add(mkbox("moov", quickTimeMeta()))
	for _, name := range []string{"../testdata/park.heic", "../testdata/rotate.heic"} {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b[:min(len(b), 4096)])
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var parseAll func(r *Reader, depth int)
		parseAll = func(r *Reader, depth int) {
			for range 100 {
				b, err := r.ReadBox()
				if err != nil {
					return
				}
				if b.Type() == TypeMoov && depth < 3 {
					parseAll(NewReader(b.Body()), depth+1)
					continue
				}
				pb, err := b.Parse()
				if err != nil {
					continue
				}
				if mb, ok := pb.(*MetaBox); ok {
					for _, c := range mb.Children {
						c.Parse()
					}
				}
			}
		}
		parseAll(NewReader(bytes.NewReader(in)), 0)
	})
}
