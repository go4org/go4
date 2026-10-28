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

// Package heif reads HEIF containers, as found in Apple HEIC/HEVC images.
// This package does not decode images; it only reads the metadata.
//
// This package is a work in progress and makes no API compatibility
// promises.
package heif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"slices"

	"go4.org/media/heif/bmff"
)

// File represents a HEIF file.
//
// Methods on File should not be called concurrently.
type File struct {
	ra      io.ReaderAt
	primary *Item

	// Populated lazily, by getMeta:
	metaErr error
	meta    *BoxMeta
}

// BoxMeta contains the low-level BMFF metadata boxes.
type BoxMeta struct {
	FileType      *bmff.FileTypeBox
	Handler       *bmff.HandlerBox
	PrimaryItem   *bmff.PrimaryItemBox
	ItemInfo      *bmff.ItemInfoBox
	Properties    *bmff.ItemPropertiesBox
	ItemLocation  *bmff.ItemLocationBox
	ItemReference *bmff.ItemReferenceBox // or nil
}

// EXIFItemID returns the item ID of the EXIF part, or 0 if not found.
func (m *BoxMeta) EXIFItemID() uint32 {
	if m.ItemInfo == nil {
		return 0
	}
	for _, ife := range m.ItemInfo.ItemInfos {
		if ife.ItemType == "Exif" {
			return uint32(ife.ItemID)
		}
	}
	return 0
}

// Item represents an item in a HEIF file.
type Item struct {
	f *File

	ID         uint32
	Info       *bmff.ItemInfoEntry
	Location   *bmff.ItemLocationBoxEntry // location in file
	Properties []bmff.Box
}

// SpatialExtents returns the item's spatial extents property values, if present,
// not correcting from any camera rotation metadata.
func (it *Item) SpatialExtents() (width, height int, ok bool) {
	for _, p := range it.Properties {
		if p, ok := p.(*bmff.ImageSpatialExtentsProperty); ok {
			return int(p.ImageWidth), int(p.ImageHeight), true
		}
	}
	return
}

// Rotations returns the number of 90 degree rotations counter-clockwise that this
// image should be rendered at, in the range [0,3].
func (it *Item) Rotations() int {
	for _, p := range it.Properties {
		if p, ok := p.(*bmff.ImageRotation); ok {
			return int(p.Angle)
		}
	}
	return 0
}

// VisualDimensions returns the item's width and height after correcting
// for any rotations.
func (it *Item) VisualDimensions() (width, height int, ok bool) {
	width, height, ok = it.SpatialExtents()
	for i := 0; i < it.Rotations(); i++ {
		width, height = height, width
	}
	return
}

// TODO: add HEIF imir (mirroring) accessor, like Image.SpatialExtents.

// Open returns a handle to access a HEIF file.
func Open(f io.ReaderAt) *File {
	return &File{ra: f}
}

// ErrNoEXIF is returned by File.EXIF when a file does not contain an EXIF item.
var ErrNoEXIF = errors.New("heif: no EXIF found")

// ErrUnknownItem is returned by File.ItemByID for unknown items.
var ErrUnknownItem = errors.New("heif: unknown item")

// EXIF returns the raw EXIF data from the file.
// The error is ErrNoEXIF if the file did not contain EXIF.
//
// The raw EXIF data can be parsed by the
// github.com/rwcarlsen/goexif/exif package's Decode function.
func (f *File) EXIF() ([]byte, error) {
	meta, err := f.getMeta()
	if err != nil {
		return nil, err
	}
	exifID := meta.EXIFItemID()
	if exifID == 0 {
		return nil, ErrNoEXIF
	}
	it, err := f.ItemByID(exifID)
	if err != nil {
		return nil, err
	}
	if it.Location == nil {
		return nil, errors.New("heif: file said it contained EXIF, but didn't say where")
	}
	if n := len(it.Location.Extents); n != 1 {
		return nil, fmt.Errorf("heif: expected 1 EXIF section, saw %d", n)
	}
	offLen := it.Location.Extents[0]
	const maxSize = 20 << 10 // 20MB of EXIF seems excessive; cap it for sanity
	if offLen.Length > maxSize {
		return nil, fmt.Errorf("heif: declared EXIF size %d exceeds threshold of %d bytes", offLen.Length, maxSize)
	}
	if offLen.Length < 4 {
		return nil, fmt.Errorf("heif: declared EXIF size %d is too small", offLen.Length)
	}
	buf := make([]byte, offLen.Length-4)
	// The EXIF item starts with a 4 byte offset to the TIFF header,
	// which is skipped here.
	n, err := f.ra.ReadAt(buf, int64(it.Location.BaseOffset+offLen.Offset)+4)
	if err != nil {
		log.Printf("Read %d bytes + %v: %q", n, err, buf)
		return nil, err
	}
	return buf, nil
}

// MaxItemDataSize is the largest item that Item.Data and
// File.StandaloneHEIF read into memory.
const MaxItemDataSize = 64 << 20

// Data returns the item's bytes, such as an image item's coded data.
// It supports only items stored in the file itself, in one or more
// extents (construction method 0 with the default data reference),
// of at most MaxItemDataSize bytes in total.
func (it *Item) Data() ([]byte, error) {
	loc := it.Location
	if loc == nil {
		return nil, fmt.Errorf("heif: item %d has no location", it.ID)
	}
	if loc.ConstructionMethod != 0 || loc.DataReferenceIndex != 0 {
		return nil, fmt.Errorf("heif: item %d uses unsupported construction method %d, data reference %d",
			it.ID, loc.ConstructionMethod, loc.DataReferenceIndex)
	}
	if len(loc.Extents) == 0 {
		return nil, fmt.Errorf("heif: item %d has no extents", it.ID)
	}
	var total uint64
	for _, e := range loc.Extents {
		// A zero length means the rest of the file, which
		// isn't supported.
		if e.Length == 0 || e.Length > MaxItemDataSize-total {
			return nil, fmt.Errorf("heif: item %d has unsupported extent length %d", it.ID, e.Length)
		}
		total += e.Length
	}
	buf := make([]byte, 0, total)
	for _, e := range loc.Extents {
		off := loc.BaseOffset + e.Offset
		if off < loc.BaseOffset || off > math.MaxInt64 {
			return nil, fmt.Errorf("heif: item %d has invalid extent offset", it.ID)
		}
		part := buf[len(buf) : len(buf)+int(e.Length)]
		if _, err := it.f.ra.ReadAt(part, int64(off)); err != nil {
			return nil, fmt.Errorf("heif: reading item %d: %w", it.ID, err)
		}
		buf = buf[:len(buf)+len(part)]
	}
	return buf, nil
}

// Thumbnails returns the thumbnail items of it: the items with a "thmb"
// reference to it, in file order. It returns no items and a nil error
// if there are none.
func (f *File) Thumbnails(it *Item) ([]*Item, error) {
	meta, err := f.getMeta()
	if err != nil {
		return nil, err
	}
	if meta.ItemReference == nil {
		return nil, nil
	}
	var thumbs []*Item
	for _, ref := range meta.ItemReference.References {
		if !ref.Type.EqualString("thmb") || !slices.Contains(ref.ToItemIDs, it.ID) {
			continue
		}
		thumb, err := f.ItemByID(ref.FromItemID)
		if err != nil {
			return nil, err
		}
		thumbs = append(thumbs, thumb)
	}
	return thumbs, nil
}

// StandaloneHEIF returns a minimal HEIF file whose primary and only
// item is a copy of it, including its data and associated properties
// (such as decoder configuration, spatial extents, and rotation).
// This lets HEIF decoders that only decode a file's primary image
// decode another item, such as a thumbnail.
//
// It only supports coded image items stored per Item.Data, not derived
// images (such as "grid") that reference other items.
func (f *File) StandaloneHEIF(it *Item) ([]byte, error) {
	meta, err := f.getMeta()
	if err != nil {
		return nil, err
	}
	switch it.Info.ItemType {
	case "grid", "iden", "iovl", "Exif", "mime", "uri ":
		return nil, fmt.Errorf("heif: can't make standalone HEIF of %q item %d", it.Info.ItemType, it.ID)
	}
	data, err := it.Data()
	if err != nil {
		return nil, err
	}
	var props []Property
	for _, ass := range f.propertyAssociations(it.ID) {
		if ass.Index == 0 || int(ass.Index) > len(meta.Properties.PropertyContainer.Properties) {
			continue
		}
		p := meta.Properties.PropertyContainer.Properties[ass.Index-1]
		body, err := io.ReadAll(p.Body())
		if err != nil {
			return nil, err
		}
		props = append(props, Property{Box: appendBox(nil, p.Type().String(), body), Essential: ass.Essential})
	}
	return EncodeImage(f.majorBrand(), it.Info.ItemType, props, data)
}

// Property is an item property for EncodeImage.
type Property struct {
	// Box is the complete property box, including its header.
	Box []byte

	// Essential is whether decoders must support the property to
	// decode the item, such as for decoder configurations and
	// transformations.
	Essential bool
}

// SpatialExtents returns an "ispe" property of an image's width and
// height in pixels, before any transformations such as rotation.
func SpatialExtents(width, height uint32) Property {
	body := make([]byte, 4) // version & flags
	body = binary.BigEndian.AppendUint32(body, width)
	body = binary.BigEndian.AppendUint32(body, height)
	return Property{Box: appendBox(nil, "ispe", body)}
}

// Rotation returns an essential "irot" property, rotating an image by
// the given number of quarter turns counterclockwise for display,
// modulo 4.
func Rotation(quarterTurns int) Property {
	return Property{Box: appendBox(nil, "irot", []byte{byte(quarterTurns & 3)}), Essential: true}
}

// EncodeImage returns a minimal HEIF file of major brand brand (such as
// "heic") whose primary and only item is a coded image of itemType (such
// as "hvc1"), with the given properties and coded data. The properties
// typically include the item's decoder configuration (such as "hvcC")
// and SpatialExtents.
func EncodeImage(brand, itemType string, props []Property, data []byte) ([]byte, error) {
	if len(brand) != 4 || len(itemType) != 4 {
		return nil, fmt.Errorf("heif: invalid brand %q or item type %q", brand, itemType)
	}
	if len(props) > 0x7f {
		return nil, errors.New("heif: too many item properties")
	}
	var ipco, ipma []byte // ipma: association entries after the count
	for i, p := range props {
		if len(p.Box) < 8 || binary.BigEndian.Uint32(p.Box) != uint32(len(p.Box)) {
			return nil, fmt.Errorf("heif: item property %d isn't a box", i)
		}
		ipco = append(ipco, p.Box...)
		v := byte(i + 1) // 1-based index
		if p.Essential {
			v |= 0x80
		}
		ipma = append(ipma, v)
	}

	const id = 1 // the item's ID
	ftyp := []byte(brand)
	ftyp = append(ftyp, 0, 0, 0, 0) // minor version
	ftyp = append(ftyp, "mif1"...)
	ftyp = append(ftyp, brand...)

	hdlr := make([]byte, 4+4+4+12+1) // version/flags, pre_defined, type, reserved, name
	copy(hdlr[8:], "pict")
	pitm := binary.BigEndian.AppendUint16(make([]byte, 4), id)

	infe := []byte{2, 0, 0, 0} // version 2
	infe = binary.BigEndian.AppendUint16(infe, id)
	infe = binary.BigEndian.AppendUint16(infe, 0) // protection index
	infe = append(infe, itemType...)
	infe = append(infe, 0) // empty name
	iinf := binary.BigEndian.AppendUint16(make([]byte, 4), 1)
	iinf = appendBox(iinf, "infe", infe)

	assoc := binary.BigEndian.AppendUint32(make([]byte, 4), 1) // version 0, 8-bit indexes; 1 entry
	assoc = binary.BigEndian.AppendUint16(assoc, id)
	assoc = append(assoc, byte(len(ipma)))
	assoc = append(assoc, ipma...)
	iprp := appendBox(nil, "ipco", ipco)
	iprp = appendBox(iprp, "ipma", assoc)

	// buildMeta returns the meta box, locating the item's data at off.
	buildMeta := func(off uint32) []byte {
		iloc := []byte{0, 0, 0, 0, 0x44, 0x00} // version 0; 4 byte offsets & lengths; no base offset
		iloc = binary.BigEndian.AppendUint16(iloc, 1)
		iloc = binary.BigEndian.AppendUint16(iloc, id)
		iloc = binary.BigEndian.AppendUint16(iloc, 0) // data reference index
		iloc = binary.BigEndian.AppendUint16(iloc, 1) // extent count
		iloc = binary.BigEndian.AppendUint32(iloc, off)
		iloc = binary.BigEndian.AppendUint32(iloc, uint32(len(data)))

		body := make([]byte, 4) // version & flags
		body = appendBox(body, "hdlr", hdlr)
		body = appendBox(body, "pitm", pitm)
		body = appendBox(body, "iinf", iinf)
		body = appendBox(body, "iloc", iloc)
		body = appendBox(body, "iprp", iprp)
		return appendBox(nil, "meta", body)
	}
	out := appendBox(nil, "ftyp", ftyp)
	dataOff := len(out) + len(buildMeta(0)) + 8 // mdat header
	if uint64(dataOff)+uint64(len(data)) > math.MaxUint32 {
		return nil, errors.New("heif: image data is too large")
	}
	out = append(out, buildMeta(uint32(dataOff))...)
	return appendBox(out, "mdat", data), nil
}

// majorBrand returns the file's major brand, or "heic" if unknown.
func (f *File) majorBrand() string {
	if f.meta != nil && f.meta.FileType != nil && len(f.meta.FileType.MajorBrand) == 4 {
		return f.meta.FileType.MajorBrand
	}
	return "heic"
}

// appendBox appends a box of type typ and the given body to dst.
func appendBox(dst []byte, typ string, body []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(8+len(body)))
	dst = append(dst, typ...)
	return append(dst, body...)
}

func (f *File) setMetaErr(err error) error {
	if f.metaErr != nil {
		f.metaErr = err
	}
	return err
}

func (f *File) getMeta() (*BoxMeta, error) {
	if f.metaErr != nil {
		return nil, f.metaErr
	}
	if f.meta != nil {
		return f.meta, nil
	}
	const assumedMaxSize = 5 << 40 // arbitrary
	sr := io.NewSectionReader(f.ra, 0, assumedMaxSize)
	bmr := bmff.NewReader(sr)

	meta := &BoxMeta{}

	pbox, err := bmr.ReadAndParseBox(bmff.TypeFtyp)
	if err != nil {
		return nil, f.setMetaErr(err)
	}
	meta.FileType = pbox.(*bmff.FileTypeBox)

	pbox, err = bmr.ReadAndParseBox(bmff.TypeMeta)
	if err != nil {
		return nil, f.setMetaErr(err)
	}
	metabox := pbox.(*bmff.MetaBox)

	for _, box := range metabox.Children {
		boxp, err := box.Parse()
		if err == bmff.ErrUnknownBox {
			continue
		}
		if err != nil {
			return nil, f.setMetaErr(err)
		}
		switch v := boxp.(type) {
		case *bmff.HandlerBox:
			meta.Handler = v
		case *bmff.PrimaryItemBox:
			meta.PrimaryItem = v
		case *bmff.ItemInfoBox:
			meta.ItemInfo = v
		case *bmff.ItemPropertiesBox:
			meta.Properties = v
		case *bmff.ItemLocationBox:
			meta.ItemLocation = v
		case *bmff.ItemReferenceBox:
			meta.ItemReference = v
		}
	}

	f.meta = meta
	return f.meta, nil
}

// PrimaryItem returns the HEIF file's primary item.
func (f *File) PrimaryItem() (*Item, error) {
	meta, err := f.getMeta()
	if err != nil {
		return nil, err
	}
	if meta.PrimaryItem == nil {
		return nil, errors.New("heif: HEIF file lacks primary item box")
	}
	return f.ItemByID(uint32(meta.PrimaryItem.ItemID))
}

// ItemByID by returns the file's Item of a given ID.
// If the ID is known, the returned error is ErrUnknownItem.
func (f *File) ItemByID(id uint32) (*Item, error) {
	meta, err := f.getMeta()
	if err != nil {
		return nil, err
	}
	it := &Item{
		f:  f,
		ID: id,
	}
	if meta.ItemLocation != nil {
		for _, ilbe := range meta.ItemLocation.Items {
			if uint32(ilbe.ItemID) == id {
				shallowCopy := ilbe
				it.Location = &shallowCopy
			}
		}
	}
	if meta.ItemInfo != nil {
		for _, iie := range meta.ItemInfo.ItemInfos {
			if uint32(iie.ItemID) == id {
				it.Info = iie
			}
		}
	}
	if it.Info == nil {
		return nil, ErrUnknownItem
	}
	for _, ass := range f.propertyAssociations(id) {
		box := meta.Properties.PropertyContainer.Properties[ass.Index-1]
		boxp, err := box.Parse()
		if err == nil {
			box = boxp
		}
		it.Properties = append(it.Properties, box)
	}
	return it, nil
}

// propertyAssociations returns item id's property associations,
// in order, omitting any with invalid property indexes.
func (f *File) propertyAssociations(id uint32) []bmff.ItemProperty {
	if f.meta == nil || f.meta.Properties == nil {
		return nil
	}
	allProps := f.meta.Properties.PropertyContainer.Properties
	var asses []bmff.ItemProperty
	for _, ipa := range f.meta.Properties.Associations {
		// TODO: I've never seen a file with more than
		// top-level ItemPropertyAssociation box, but
		// apparently they can exist with different
		// versions/flags. For now we just merge them
		// all together, but that's not really right.
		// So for now, just bail once a previous loop
		// found anything.
		if len(asses) > 0 {
			break
		}

		for _, ipai := range ipa.Entries {
			if ipai.ItemID != id {
				continue
			}
			for _, ass := range ipai.Associations {
				if ass.Index != 0 && int(ass.Index) <= len(allProps) {
					asses = append(asses, ass)
				}
			}
		}
	}
	return asses
}
