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
	"errors"
	"fmt"
	"io"
)

// MovieHeaderBox is an "mvhd" box.
type MovieHeaderBox struct {
	FullBox

	// CreationTime and ModificationTime are in seconds since
	// 1904-01-01 00:00:00 UTC.
	CreationTime     uint64
	ModificationTime uint64

	TimeScale uint32 // time units per second
	Duration  uint64 // in TimeScale units
}

func parseMovieHeaderBox(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	mh := &MovieHeaderBox{FullBox: fb}
	switch fb.Version {
	case 0:
		ct, _ := br.readUint32()
		mt, _ := br.readUint32()
		mh.TimeScale, _ = br.readUint32()
		d, _ := br.readUint32()
		mh.CreationTime, mh.ModificationTime, mh.Duration = uint64(ct), uint64(mt), uint64(d)
	case 1:
		mh.CreationTime, _ = br.readUintN(64)
		mh.ModificationTime, _ = br.readUintN(64)
		mh.TimeScale, _ = br.readUint32()
		mh.Duration, _ = br.readUintN(64)
	default:
		return nil, fmt.Errorf("unsupported mvhd version %d", fb.Version)
	}
	if !br.ok() {
		return nil, br.err
	}
	return mh, nil
}

// MetadataKey is an entry of a KeysBox.
type MetadataKey struct {
	Namespace string // always 4 bytes; usually "mdta"
	Name      string // e.g. "com.apple.quicktime.content.identifier"
}

// KeysBox is a QuickTime "keys" box, found in a QuickTime MetaBox
// with handler type "mdta".
type KeysBox struct {
	FullBox

	// Keys are the metadata keys. The item with KeyIndex i in the
	// sibling ItemListBox has key Keys[i-1].
	Keys []MetadataKey
}

func parseKeysBox(outer *box, br *bufReader) (Box, error) {
	fb, err := readFullBox(outer, br)
	if err != nil {
		return nil, err
	}
	kb := &KeysBox{FullBox: fb}
	count, _ := br.readUint32()
	for i := uint32(0); i < count && br.ok(); i++ {
		size, _ := br.readUint32()
		if !br.ok() {
			break
		}
		if size < 8 {
			return nil, fmt.Errorf("invalid keys entry size %d", size)
		}
		var ns [4]byte
		if _, err := io.ReadFull(br, ns[:]); err != nil {
			return nil, err
		}
		// Don't trust size for allocation; read only what's present.
		n := int64(size) - 8
		name, err := io.ReadAll(io.LimitReader(br, n))
		if err != nil {
			return nil, err
		}
		if int64(len(name)) != n {
			return nil, io.ErrUnexpectedEOF
		}
		kb.Keys = append(kb.Keys, MetadataKey{Namespace: string(ns[:]), Name: string(name)})
	}
	if !br.ok() {
		return nil, br.err
	}
	return kb, nil
}

// ItemListBox is an "ilst" metadata item list box.
type ItemListBox struct {
	*box
	Items []*MetadataItem
}

// MetadataItem is a child of an ItemListBox.
//
// In QuickTime "mdta" metadata, the item's box type is its big-endian
// KeyIndex. In iTunes-style metadata, the box type is a four-character
// code such as "\xa9nam".
type MetadataItem struct {
	*box
	KeyIndex uint32     // the box type as a big-endian number
	Data     []*DataBox // the item's value(s), usually one
}

func parseItemListBox(outer *box, br *bufReader) (Box, error) {
	ilb := &ItemListBox{box: outer}
	var children []Box
	if err := br.parseAppendBoxes(&children); err != nil {
		return nil, err
	}
	for _, c := range children {
		cb := c.(*box)
		item := &MetadataItem{box: cb, KeyIndex: binary.BigEndian.Uint32(cb.boxType[:])}
		r := NewReader(bytes.NewReader(cb.slurp))
		for {
			b, err := r.ReadBox()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("reading ilst item %q: %v", cb.boxType, err)
			}
			if !b.Type().EqualString("data") {
				continue // e.g. "mean" or "name" in iTunes "----" items
			}
			pb, err := b.Parse()
			if err != nil {
				return nil, fmt.Errorf("parsing ilst item %q data: %v", cb.boxType, err)
			}
			item.Data = append(item.Data, pb.(*DataBox))
		}
		ilb.Items = append(ilb.Items, item)
	}
	return ilb, nil
}

// DataBox is a metadata "data" box, holding a metadata item's value.
type DataBox struct {
	*box

	// DataType is the type indicator. Its high byte is the type set,
	// which is zero for the well-known types in the low 24 bits,
	// e.g. 1 (UTF-8), 21 (big-endian signed integer),
	// 22 (big-endian unsigned integer), 23 (big-endian float32),
	// or 24 (big-endian float64).
	DataType uint32

	Locale uint32
	Value  []byte
}

var errShortDataBox = errors.New("short data box")

func parseDataBox(outer *box, br *bufReader) (Box, error) {
	typ, _ := br.readUint32()
	locale, _ := br.readUint32()
	if !br.ok() {
		return nil, errShortDataBox
	}
	val, err := io.ReadAll(br)
	if err != nil {
		return nil, err
	}
	return &DataBox{box: outer, DataType: typ, Locale: locale, Value: val}, nil
}
