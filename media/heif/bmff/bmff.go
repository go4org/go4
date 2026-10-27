/*
Copyright 2018 The go4 Authors

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

// Package bmff reads ISO BMFF boxes, as used by HEIF, MP4, QuickTime, etc.
//
// This is not so much as a generic BMFF reader as it is a BMFF reader
// as needed by its users. Only boxes necessary for the go4.org/media/heif
// package and for reading QuickTime-style ("mdta") metadata have explicit
// parsers.
//
// This package makes no API compatibility promises; it exists
// primarily for use by the go4.org/media/heif package.
package bmff

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// NewReader returns a Reader that reads boxes from r.
//
// If r implements io.Seeker (and is not a *bufio.Reader), the Reader
// seeks past the unread remainder of boxes instead of reading it, which
// matters for files with large media data boxes.
func NewReader(r io.Reader) *Reader {
	if br, ok := r.(*bufio.Reader); ok {
		return &Reader{br: bufReader{Reader: br}}
	}
	seeker, _ := r.(io.Seeker)
	return &Reader{br: bufReader{Reader: bufio.NewReader(r)}, src: r, seeker: seeker}
}

type Reader struct {
	br          bufReader
	src         io.Reader // the underlying reader of br, if seeker is non-nil
	seeker      io.Seeker // or nil
	pos         int64     // offset of the next box header
	lastBox     *box      // or nil
	noMoreBoxes bool      // a box with size 0 (the final box) was seen
}

type BoxType [4]byte

// Common box types.
var (
	TypeFtyp = BoxType{'f', 't', 'y', 'p'}
	TypeMeta = BoxType{'m', 'e', 't', 'a'}
	TypeMoov = BoxType{'m', 'o', 'o', 'v'}
)

func (t BoxType) String() string { return string(t[:]) }

func (t BoxType) EqualString(s string) bool {
	// Could be cleaner, but see ohttps://github.com/golang/go/issues/24765
	return len(s) == 4 && s[0] == t[0] && s[1] == t[1] && s[2] == t[2] && s[3] == t[3]
}

type parseFunc func(b box, br *bufio.Reader) (Box, error)

// Box represents a BMFF box.
type Box interface {
	Size() int64 // 0 means unknown (will read to end of file)
	Type() BoxType

	// Offset returns the offset of the box's header, relative to the
	// start of the input of the Reader that read the box.
	Offset() int64

	// HeaderSize returns the size of the box's header, which precedes
	// the body: 8 bytes, or 16 for boxes with a 64-bit size.
	HeaderSize() int64

	// Parses parses the box, populating the fields
	// in the returned concrete type.
	//
	// If Parse has already been called, Parse returns nil.
	// If the box type is unknown, the returned error is ErrUnknownBox
	// and it's guaranteed that no bytes have been read from the box.
	Parse() (Box, error)

	// Body returns the inner bytes of the box, ignoring the header.
	// The body may start with the 4 byte header of a "Full Box" if the
	// box's type derives from a full box. Most users will use Parse
	// instead.
	// Body will return a new reader at the beginning of the box if the
	// outer box has already been parsed.
	Body() io.Reader
}

// ErrUnknownBox is returned by Box.Parse for unrecognized box types.
var ErrUnknownBox = errors.New("heif: unknown box")

type parserFunc func(b *box, br *bufReader) (Box, error)

func boxType(s string) BoxType {
	if len(s) != 4 {
		panic("bogus boxType length")
	}
	return BoxType{s[0], s[1], s[2], s[3]}
}

var parsers = map[BoxType]parserFunc{
	boxType("data"): parseDataBox,
	boxType("dinf"): parseDataInformationBox,
	boxType("dref"): parseDataReferenceBox,
	boxType("ftyp"): parseFileTypeBox,
	boxType("hdlr"): parseHandlerBox,
	boxType("iinf"): parseItemInfoBox,
	boxType("ilst"): parseItemListBox,
	boxType("infe"): parseItemInfoEntry,
	boxType("iloc"): parseItemLocationBox,
	boxType("ipco"): parseItemPropertyContainerBox,
	boxType("ipma"): parseItemPropertyAssociation,
	boxType("iprp"): parseItemPropertiesBox,
	boxType("iref"): parseItemReferenceBox,
	boxType("irot"): parseImageRotation,
	boxType("ispe"): parseImageSpatialExtentsProperty,
	boxType("keys"): parseKeysBox,
	boxType("meta"): parseMetaBox,
	boxType("mvhd"): parseMovieHeaderBox,
	boxType("pitm"): parsePrimaryItemBox,
}

type box struct {
	size       int64 // 0 means unknown, will read to end of file (box container)
	boxType    BoxType
	offset     int64 // of the header, relative to the Reader's input
	headerSize int64
	body       io.Reader
	parsed     Box    // if non-nil, the Parsed result
	slurp      []byte // if non-nil, the contents slurped to memory
}

func (b *box) Size() int64       { return b.size }
func (b *box) Type() BoxType     { return b.boxType }
func (b *box) Offset() int64     { return b.offset }
func (b *box) HeaderSize() int64 { return b.headerSize }

func (b *box) Body() io.Reader {
	if b.slurp != nil {
		return bytes.NewReader(b.slurp)
	}
	return b.body
}

func (b *box) Parse() (Box, error) {
	if b.parsed != nil {
		return b.parsed, nil
	}
	parser, ok := parsers[b.Type()]
	if !ok {
		return nil, ErrUnknownBox
	}
	v, err := parser(b, &bufReader{Reader: newBodyReader(b)})
	if err != nil {
		return nil, err
	}
	b.parsed = v
	return v, nil
}

// newBodyReader returns a buffered reader of b's body. HEIF files have
// many small boxes, such as one "infe" box per item, so the buffer is
// no larger than a slurped body, but large enough for the parsers'
// largest Peek.
func newBodyReader(b *box) *bufio.Reader {
	const minSize, maxSize = 64, 4096
	size := maxSize
	if b.slurp != nil {
		size = min(max(len(b.slurp), minSize), maxSize)
	}
	return bufio.NewReaderSize(b.Body(), size)
}

type FullBox struct {
	*box
	Version uint8
	Flags   uint32 // 24 bits
}

// ReadBox reads the next box.
//
// If the previously read box was not read to completion, ReadBox consumes
// the rest of its data.
//
// At the end, the error is io.EOF.
func (r *Reader) ReadBox() (Box, error) {
	if r.noMoreBoxes {
		return nil, io.EOF
	}
	if r.lastBox != nil {
		if err := r.skipRest(r.lastBox); err != nil {
			return nil, err
		}
	}
	var buf [8]byte

	_, err := io.ReadFull(r.br, buf[:4])
	if err != nil {
		return nil, err
	}
	box := &box{
		size:       int64(binary.BigEndian.Uint32(buf[:4])),
		offset:     r.pos,
		headerSize: 8,
	}

	_, err = io.ReadFull(r.br, box.boxType[:]) // 4 more bytes
	if err != nil {
		return nil, err
	}

	// Special cases for size:
	var remain int64
	switch box.size {
	case 1:
		// 1 means it's actually a 64-bit size, after the type.
		_, err = io.ReadFull(r.br, buf[:8])
		if err != nil {
			return nil, err
		}
		box.size = int64(binary.BigEndian.Uint64(buf[:8]))
		box.headerSize = 16
		if box.size < 0 {
			// Go uses int64 for sizes typically, but BMFF uses uint64.
			// We assume for now that nobody actually uses boxes larger
			// than int64.
			return nil, fmt.Errorf("unexpectedly large box %q", box.boxType)
		}
		remain = box.size - 2*4 - 8
	case 0:
		// 0 means unknown & to read to end of file. No more boxes.
		r.noMoreBoxes = true
	default:
		remain = box.size - 2*4
	}
	if remain < 0 {
		return nil, fmt.Errorf("Box header for %q has size %d, suggesting %d (negative) bytes remain", box.boxType, box.size, remain)
	}
	if box.size > 0 {
		box.body = io.LimitReader(r.br, remain)
		r.pos += box.size
	} else {
		box.body = r.br
	}
	r.lastBox = box
	return box, nil
}

// skipRest consumes the unread remainder of b, the most recently read box.
func (r *Reader) skipRest(b *box) error {
	lr, ok := b.body.(*io.LimitedReader)
	if !ok {
		// A box extending to the end of the input. No boxes follow it.
		return nil
	}
	if buffered := int64(r.br.Buffered()); r.seeker != nil && lr.N > buffered {
		// The underlying reader is positioned after the buffered bytes.
		if _, err := r.seeker.Seek(lr.N-buffered, io.SeekCurrent); err == nil {
			r.br.Reset(r.src)
			lr.N = 0
			return nil
		}
		// Not actually seekable (e.g. a pipe). Fall back to reading.
	}
	_, err := io.Copy(io.Discard, lr)
	return err
}

// ReadAndParseBox wraps the ReadBox method, ensuring that the read box is of type typ
// and parses successfully. It returns the parsed box.
func (r *Reader) ReadAndParseBox(typ BoxType) (Box, error) {
	box, err := r.ReadBox()
	if err != nil {
		return nil, fmt.Errorf("error reading %q box: %v", typ, err)
	}
	if box.Type() != typ {
		return nil, fmt.Errorf("error reading %q box: got box type %q instead", typ, box.Type())
	}
	pbox, err := box.Parse()
	if err != nil {
		return nil, fmt.Errorf("error parsing read %q box: %v", typ, err)
	}
	return pbox, nil
}

func readFullBox(outer *box, br *bufReader) (fb FullBox, err error) {
	fb.box = outer
	// Parse FullBox header.
	buf, err := br.Peek(4)
	if err != nil {
		return FullBox{}, fmt.Errorf("failed to read 4 bytes of FullBox: %v", err)
	}
	fb.Version = buf[0]
	buf[0] = 0
	fb.Flags = binary.BigEndian.Uint32(buf[:4])
	br.Discard(4)
	return fb, nil
}

type FileTypeBox struct {
	*box
	MajorBrand   string   // 4 bytes
	MinorVersion string   // 4 bytes
	Compatible   []string // all 4 bytes
}

func parseFileTypeBox(outer *box, br *bufReader) (Box, error) {
	buf, err := br.Peek(8)
	if err != nil {
		return nil, err
	}
	ft := &FileTypeBox{
		box:          outer,
		MajorBrand:   string(buf[:4]),
		MinorVersion: string(buf[4:8]),
	}
	br.Discard(8)
	for {
		buf, err := br.Peek(4)
		if err == io.EOF {
			return ft, nil
		}
		if err != nil {
			return nil, err
		}
		ft.Compatible = append(ft.Compatible, string(buf[:4]))
		br.Discard(4)
	}
}

type MetaBox struct {
	FullBox

	// QuickTime is whether the box lacks the FullBox version and
	// flags header, as QuickTime "meta" boxes do. If so, Version and
	// Flags are zero.
	QuickTime bool

	Children []Box
}

func parseMetaBox(outer *box, br *bufReader) (Box, error) {
	// An ISO BMFF meta box is a FullBox, but a QuickTime one starts
	// directly with its "hdlr" child box.
	if buf, err := br.Peek(8); err == nil && string(buf[4:8]) == "hdlr" {
		mb := &MetaBox{FullBox: FullBox{box: outer}, QuickTime: true}
		return mb, br.parseAppendBoxes(&mb.Children)
	}
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	mb := &MetaBox{FullBox: fb}
	return mb, br.parseAppendBoxes(&mb.Children)
}

func (br *bufReader) parseAppendBoxes(dst *[]Box) error {
	if br.err != nil {
		return br.err
	}
	boxr := NewReader(br.Reader)
	for {
		inner, err := boxr.ReadBox()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			br.err = err
			return err
		}
		slurp, err := readBody(inner)
		if err != nil {
			br.err = err
			return err
		}
		inner.(*box).slurp = slurp
		*dst = append(*dst, inner)
	}
}

// readBody reads all of b's body. If the body is of a known, modest
// size, it's read into a buffer of that size, rather than growing one.
// As with io.ReadAll, a body truncated by the end of its input is not
// an error.
func readBody(b Box) ([]byte, error) {
	const maxPrealloc = 1 << 20 // don't trust larger declared sizes
	n := b.Size() - b.HeaderSize()
	if b.Size() <= 0 || n > maxPrealloc {
		return io.ReadAll(b.Body())
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(b.Body(), buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return buf[:got], err
}

// ItemInfoEntry represents an "infe" box.
//
// TODO: currently only parses Version 2 boxes.
type ItemInfoEntry struct {
	FullBox

	ItemID          uint16
	ProtectionIndex uint16
	ItemType        string // always 4 bytes

	Name string

	// If Type == "mime":
	ContentType     string
	ContentEncoding string

	// If Type == "uri ":
	ItemURIType string
}

func parseItemInfoEntry(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	ie := &ItemInfoEntry{FullBox: fb}
	if fb.Version != 2 {
		return nil, fmt.Errorf("TODO: found version %d infe box. Only 2 is supported now.", fb.Version)
	}

	ie.ItemID, _ = br.readUint16()
	ie.ProtectionIndex, _ = br.readUint16()
	if !br.ok() {
		return nil, br.err
	}
	buf, err := br.Peek(4)
	if err != nil {
		return nil, err
	}
	ie.ItemType = string(buf[:4])
	ie.Name, _ = br.readString()

	switch ie.ItemType {
	case "mime":
		ie.ContentType, _ = br.readString()
		if br.anyRemain() {
			ie.ContentEncoding, _ = br.readString()
		}
	case "uri ":
		ie.ItemURIType, _ = br.readString()
	}
	if !br.ok() {
		return nil, br.err
	}
	return ie, nil
}

// ItemInfoBox represents an "iinf" box.
type ItemInfoBox struct {
	FullBox
	Count     uint16
	ItemInfos []*ItemInfoEntry
}

func parseItemInfoBox(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	ib := &ItemInfoBox{FullBox: fb}

	ib.Count, _ = br.readUint16()

	var itemInfos []Box
	br.parseAppendBoxes(&itemInfos)
	if br.ok() {
		for _, box := range itemInfos {
			pb, err := box.Parse()
			if err != nil {
				return nil, fmt.Errorf("error parsing ItemInfoEntry in ItemInfoBox: %v", err)
			}
			if iie, ok := pb.(*ItemInfoEntry); ok {
				ib.ItemInfos = append(ib.ItemInfos, iie)
			}
		}
	}
	if !br.ok() {
		return FullBox{}, br.err
	}
	return ib, nil
}

// bufReader adds some HEIF/BMFF-specific methods around a *bufio.Reader.
type bufReader struct {
	*bufio.Reader
	err error // sticky error
}

// ok reports whether all previous reads have been error-free.
func (br *bufReader) ok() bool { return br.err == nil }

func (br *bufReader) anyRemain() bool {
	if br.err != nil {
		return false
	}
	_, err := br.Peek(1)
	return err == nil
}

func (br *bufReader) readUintN(bits uint8) (uint64, error) {
	if br.err != nil {
		return 0, br.err
	}
	if bits == 0 {
		return 0, nil
	}
	nbyte := bits / 8
	buf, err := br.Peek(int(nbyte))
	if err != nil {
		br.err = err
		return 0, err
	}
	defer br.Discard(int(nbyte))
	switch bits {
	case 8:
		return uint64(buf[0]), nil
	case 16:
		return uint64(binary.BigEndian.Uint16(buf[:2])), nil
	case 32:
		return uint64(binary.BigEndian.Uint32(buf[:4])), nil
	case 64:
		return binary.BigEndian.Uint64(buf[:8]), nil
	default:
		br.err = fmt.Errorf("invalid uintn read size")
		return 0, br.err
	}
}

func (br *bufReader) readUint8() (uint8, error) {
	if br.err != nil {
		return 0, br.err
	}
	v, err := br.ReadByte()
	if err != nil {
		br.err = err
		return 0, err
	}
	return v, nil
}

func (br *bufReader) readUint16() (uint16, error) {
	if br.err != nil {
		return 0, br.err
	}
	buf, err := br.Peek(2)
	if err != nil {
		br.err = err
		return 0, err
	}
	v := binary.BigEndian.Uint16(buf[:2])
	br.Discard(2)
	return v, nil
}

func (br *bufReader) readUint32() (uint32, error) {
	if br.err != nil {
		return 0, br.err
	}
	buf, err := br.Peek(4)
	if err != nil {
		br.err = err
		return 0, err
	}
	v := binary.BigEndian.Uint32(buf[:4])
	br.Discard(4)
	return v, nil
}

func (br *bufReader) readString() (string, error) {
	if br.err != nil {
		return "", br.err
	}
	s0, err := br.ReadString(0)
	if err != nil {
		br.err = err
		return "", err
	}
	s := strings.TrimSuffix(s0, "\x00")
	if len(s) == len(s0) {
		err = fmt.Errorf("unexpected non-null terminated string")
		br.err = err
		return "", err
	}
	return s, nil
}

// HEIF: ipco
type ItemPropertyContainerBox struct {
	*box
	Properties []Box // of ItemProperty or ItemFullProperty
}

func parseItemPropertyContainerBox(outer *box, br *bufReader) (Box, error) {
	ipc := &ItemPropertyContainerBox{box: outer}
	return ipc, br.parseAppendBoxes(&ipc.Properties)
}

// HEIF: iprp
type ItemPropertiesBox struct {
	*box
	PropertyContainer *ItemPropertyContainerBox
	Associations      []*ItemPropertyAssociation // at least 1
}

func parseItemPropertiesBox(outer *box, br *bufReader) (Box, error) {
	ip := &ItemPropertiesBox{
		box: outer,
	}

	var boxes []Box
	err := br.parseAppendBoxes(&boxes)
	if err != nil {
		return nil, err
	}
	if len(boxes) < 2 {
		return nil, fmt.Errorf("expect at least 2 boxes in children; got 0")
	}

	cb, err := boxes[0].Parse()
	if err != nil {
		return nil, fmt.Errorf("failed to parse first box, %q: %v", boxes[0].Type(), err)
	}

	var ok bool
	ip.PropertyContainer, ok = cb.(*ItemPropertyContainerBox)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T for ItemPropertieBox.PropertyContainer", cb)
	}

	// Association boxes
	ip.Associations = make([]*ItemPropertyAssociation, 0, len(boxes)-1)
	for _, box := range boxes[1:] {
		boxp, err := box.Parse()
		if err != nil {
			return nil, fmt.Errorf("failed to parse association box: %v", err)
		}
		ipa, ok := boxp.(*ItemPropertyAssociation)
		if !ok {
			return nil, fmt.Errorf("unexpected box %q instead of ItemPropertyAssociation", boxp.Type())
		}
		ip.Associations = append(ip.Associations, ipa)
	}
	return ip, nil
}

type ItemPropertyAssociation struct {
	FullBox
	EntryCount uint32
	Entries    []ItemPropertyAssociationItem
}

// not a box
type ItemProperty struct {
	Essential bool
	Index     uint16
}

// not a box
type ItemPropertyAssociationItem struct {
	ItemID            uint32
	AssociationsCount int            // as declared
	Associations      []ItemProperty // as parsed
}

func parseItemPropertyAssociation(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	ipa := &ItemPropertyAssociation{FullBox: fb}
	count, _ := br.readUint32()
	ipa.EntryCount = count

	for i := uint64(0); i < uint64(count) && br.ok(); i++ {
		var itemID uint32
		if fb.Version < 1 {
			itemID16, _ := br.readUint16()
			itemID = uint32(itemID16)
		} else {
			itemID, _ = br.readUint32()
		}
		assocCount, _ := br.readUint8()
		ipai := ItemPropertyAssociationItem{
			ItemID:            itemID,
			AssociationsCount: int(assocCount),
		}
		for j := 0; j < int(assocCount) && br.ok(); j++ {
			first, _ := br.readUint8()
			essential := first&(1<<7) != 0
			first &^= byte(1 << 7)

			var index uint16
			if fb.Flags&1 != 0 {
				second, _ := br.readUint8()
				index = uint16(first)<<8 | uint16(second)
			} else {
				index = uint16(first)
			}
			ipai.Associations = append(ipai.Associations, ItemProperty{
				Essential: essential,
				Index:     index,
			})
		}
		ipa.Entries = append(ipa.Entries, ipai)
	}
	if !br.ok() {
		return nil, br.err
	}
	return ipa, nil
}

type ImageSpatialExtentsProperty struct {
	FullBox
	ImageWidth  uint32
	ImageHeight uint32
}

func parseImageSpatialExtentsProperty(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	w, err := br.readUint32()
	if err != nil {
		return nil, err
	}
	h, err := br.readUint32()
	if err != nil {
		return nil, err
	}
	return &ImageSpatialExtentsProperty{
		FullBox:     fb,
		ImageWidth:  w,
		ImageHeight: h,
	}, nil
}

type OffsetLength struct {
	Offset, Length uint64
}

// not a box
type ItemLocationBoxEntry struct {
	ItemID             uint32 // 16 bits before version 2
	ConstructionMethod uint8  // actually uint4
	DataReferenceIndex uint16
	BaseOffset         uint64 // 0, 4, or 8 bytes, depending on encoding
	ExtentCount        uint16

	// Extents are relative to BaseOffset. Their extent_index values,
	// used only by construction method 2, are not retained.
	Extents []OffsetLength
}

// box "iloc"
type ItemLocationBox struct {
	FullBox

	offsetSize, lengthSize, baseOffsetSize, indexSize uint8 // actually uint4, in bytes

	ItemCount uint32 // 16 bits before version 2
	Items     []ItemLocationBoxEntry
}

func parseItemLocationBox(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	ilb := &ItemLocationBox{
		FullBox: fb,
	}
	buf, err := br.Peek(4)
	if err != nil {
		return nil, err
	}
	ilb.offsetSize = buf[0] >> 4
	ilb.lengthSize = buf[0] & 15
	ilb.baseOffsetSize = buf[1] >> 4
	if fb.Version > 0 { // versions 1 and 2
		ilb.indexSize = buf[1] & 15
	}
	br.Discard(2)

	if fb.Version < 2 {
		n, _ := br.readUint16()
		ilb.ItemCount = uint32(n)
	} else {
		ilb.ItemCount, _ = br.readUint32()
	}

	for i := uint32(0); br.ok() && i < ilb.ItemCount; i++ {
		var ent ItemLocationBoxEntry
		if fb.Version < 2 {
			id, _ := br.readUint16()
			ent.ItemID = uint32(id)
		} else {
			ent.ItemID, _ = br.readUint32()
		}
		if fb.Version > 0 { // versions 1 and 2
			cmeth, _ := br.readUint16()
			ent.ConstructionMethod = byte(cmeth & 15)
		}
		ent.DataReferenceIndex, _ = br.readUint16()
		ent.BaseOffset, _ = br.readUintN(ilb.baseOffsetSize * 8)
		ent.ExtentCount, _ = br.readUint16()
		for j := 0; br.ok() && j < int(ent.ExtentCount); j++ {
			var ol OffsetLength
			if fb.Version > 0 {
				br.readUintN(ilb.indexSize * 8) // extent_index
			}
			ol.Offset, _ = br.readUintN(ilb.offsetSize * 8)
			ol.Length, _ = br.readUintN(ilb.lengthSize * 8)
			if br.err != nil {
				return nil, br.err
			}
			ent.Extents = append(ent.Extents, ol)
		}
		ilb.Items = append(ilb.Items, ent)
	}
	if !br.ok() {
		return nil, br.err
	}
	return ilb, nil
}

// a "hdlr" box.
type HandlerBox struct {
	FullBox
	HandlerType string // always 4 bytes; usually "pict" for iOS Camera images
	Name        string
}

func parseHandlerBox(gen *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(gen, br)
	if err != nil {
		return nil, err
	}
	hb := &HandlerBox{
		FullBox: fb,
	}
	buf, err := br.Peek(20)
	if err != nil {
		return nil, err
	}
	hb.HandlerType = string(buf[4:8])
	br.Discard(20)

	// The name is null-terminated in ISO BMFF, but QuickTime handler
	// boxes may omit it entirely or leave it unterminated.
	name, err := br.ReadString(0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	hb.Name = strings.TrimSuffix(name, "\x00")
	return hb, nil
}

// a "dinf" box
type DataInformationBox struct {
	*box
	Children []Box
}

func parseDataInformationBox(gen *box, br *bufReader) (Box, error) {
	dib := &DataInformationBox{box: gen}
	return dib, br.parseAppendBoxes(&dib.Children)
}

// a "dref" box.
type DataReferenceBox struct {
	FullBox
	EntryCount uint32
	Children   []Box
}

func parseDataReferenceBox(gen *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(gen, br)
	if err != nil {
		return nil, err
	}
	drb := &DataReferenceBox{FullBox: fb}
	drb.EntryCount, _ = br.readUint32()
	return drb, br.parseAppendBoxes(&drb.Children)
}

// "pitm" box
type PrimaryItemBox struct {
	FullBox
	ItemID uint32 // 16 bits in version 0
}

func parsePrimaryItemBox(gen *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(gen, br)
	if err != nil {
		return nil, err
	}
	pib := &PrimaryItemBox{FullBox: fb}
	if fb.Version == 0 {
		id, _ := br.readUint16()
		pib.ItemID = uint32(id)
	} else {
		pib.ItemID, _ = br.readUint32()
	}
	if !br.ok() {
		return nil, br.err
	}
	return pib, nil
}

// ItemReferenceBox is an "iref" box.
type ItemReferenceBox struct {
	FullBox
	References []ItemReference
}

// ItemReference is a typed reference from one item to others,
// such as "thmb" (FromItemID is a thumbnail of ToItemIDs) or
// "cdsc" (FromItemID describes ToItemIDs). It is not a box.
type ItemReference struct {
	Type       BoxType
	FromItemID uint32
	ToItemIDs  []uint32
}

func parseItemReferenceBox(gen *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(gen, br)
	if err != nil {
		return nil, err
	}
	irb := &ItemReferenceBox{FullBox: fb}
	var refs []Box
	if err := br.parseAppendBoxes(&refs); err != nil {
		return nil, err
	}
	for _, rb := range refs {
		rr := &bufReader{Reader: bufio.NewReader(rb.Body())}
		readID := func() uint32 {
			if fb.Version == 0 {
				v, _ := rr.readUint16()
				return uint32(v)
			}
			v, _ := rr.readUint32()
			return v
		}
		ref := ItemReference{Type: rb.Type(), FromItemID: readID()}
		n, _ := rr.readUint16()
		for i := 0; rr.ok() && i < int(n); i++ {
			ref.ToItemIDs = append(ref.ToItemIDs, readID())
		}
		if !rr.ok() {
			return nil, fmt.Errorf("error parsing %q item reference: %v", rb.Type(), rr.err)
		}
		irb.References = append(irb.References, ref)
	}
	return irb, nil
}

// ImageRotation is a HEIF "irot" rotation property.
type ImageRotation struct {
	*box
	Angle uint8 // 1 means 90 degrees counter-clockwise, 2 means 180 counter-clockwise
}

func parseImageRotation(gen *box, br *bufReader) (Box, error) {
	v, err := br.readUint8()
	if err != nil {
		return nil, err
	}
	return &ImageRotation{box: gen, Angle: v & 3}, nil
}
