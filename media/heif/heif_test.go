package heif

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
)

func TestAll(t *testing.T) {
	f, err := os.Open("testdata/park.heic")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := Open(f)

	// meta
	_, err = h.getMeta()
	if err != nil {
		t.Fatalf("getMeta: %v", err)
	}

	it, err := h.PrimaryItem()
	if err != nil {
		t.Fatalf("PrimaryItem: %v", err)
	}
	if want := uint32(49); it.ID != want {
		t.Errorf("PrimaryIem ID = %v; want %v", it.ID, want)
	}
	if it.Location == nil {
		t.Errorf("Item.Location is nil")
	}
	if it.Info == nil {
		t.Errorf("Item.Info is nil")
	}
	if len(it.Properties) == 0 {
		t.Errorf("Item.Properties is empty")
	}
	for _, prop := range it.Properties {
		t.Logf("  property: %q, %#v", prop.Type(), prop)
	}
	if w, h, ok := it.SpatialExtents(); !ok || w == 0 || h == 0 {
		t.Errorf("no spatial extents found")
	} else {
		t.Logf("dimensions: %v x %v", w, h)
	}

	// exif
	exbuf, err := h.EXIF()
	if err != nil {
		t.Errorf("EXIF: %v", err)
	} else {
		const magic = "Exif\x00\x00"
		if !bytes.HasPrefix(exbuf, []byte(magic)) {
			t.Errorf("Exif buffer doesn't start with %q: got %q", magic, exbuf)
		}
		x, err := exif.Decode(bytes.NewReader(exbuf))
		if err != nil {
			t.Fatalf("EXIF decode: %v", err)
		}
		got := map[string]string{}
		if err := x.Walk(walkFunc(func(name exif.FieldName, tag *tiff.Tag) error {
			got[fmt.Sprint(name)] = fmt.Sprint(tag)
			return nil
		})); err != nil {
			t.Fatalf("EXIF walk: %v", err)
		}
		if g, w := len(got), 56; g < w {
			t.Errorf("saw %v EXIF tags; want at least %v", g, w)
		}
		if g, w := got["GPSLongitude"], `["122/1","21/1","3776/100"]`; g != w {
			t.Errorf("GPSLongitude = %#q; want %#q", g, w)
		}

	}
}

func TestRotations(t *testing.T) {
	f, err := os.Open("testdata/rotate.heic")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := Open(f)
	it, err := h.PrimaryItem()
	if err != nil {
		t.Fatalf("PrimaryItem: %v", err)
	}
	if r := it.Rotations(); r != 3 {
		t.Errorf("Rotations = %v; want %v", r, 3)
	}
	sw, sh, ok := it.SpatialExtents()
	if !ok {
		t.Fatalf("expected spatial extents")
	}
	vw, vh, ok := it.VisualDimensions()
	if !ok {
		t.Fatalf("expected visual dimensions")
	}
	if vw != sh || vh != sw {
		t.Errorf("visual dimensions = %v, %v; want %v, %v", vw, vh, sh, sw)
	}
}

type walkFunc func(exif.FieldName, *tiff.Tag) error

func (f walkFunc) Walk(name exif.FieldName, tag *tiff.Tag) error {
	return f(name, tag)
}

func TestThumbnails(t *testing.T) {
	src, err := os.ReadFile("testdata/park.heic")
	if err != nil {
		t.Fatal(err)
	}
	h := Open(bytes.NewReader(src))
	primary, err := h.PrimaryItem()
	if err != nil {
		t.Fatalf("PrimaryItem: %v", err)
	}
	thumbs, err := h.Thumbnails(primary)
	if err != nil {
		t.Fatalf("Thumbnails: %v", err)
	}
	if len(thumbs) != 1 {
		t.Fatalf("got %d thumbnails; want 1", len(thumbs))
	}
	thumb := thumbs[0]
	if thumb.ID != 50 || thumb.Info.ItemType != "hvc1" {
		t.Errorf("thumbnail = item %d, type %q; want item 50, hvc1", thumb.ID, thumb.Info.ItemType)
	}
	if w, h, _ := thumb.SpatialExtents(); w != 320 || h != 240 {
		t.Errorf("thumbnail extents = %dx%d; want 320x240", w, h)
	}
	data, err := thumb.Data()
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	if ext := thumb.Location.Extents[0]; !bytes.Equal(data, src[ext.Offset:][:ext.Length]) {
		t.Errorf("Data returned %d bytes not matching its extent %+v", len(data), ext)
	}

	if tt, err := h.Thumbnails(thumb); err != nil || len(tt) != 0 {
		t.Errorf("thumbnails of thumbnail = %v, %v; want none", tt, err)
	}
	if _, err := h.StandaloneHEIF(primary); err == nil {
		t.Error("StandaloneHEIF of grid item succeeded; want error")
	}

	standalone, err := h.StandaloneHEIF(thumb)
	if err != nil {
		t.Fatalf("StandaloneHEIF: %v", err)
	}
	sh := Open(bytes.NewReader(standalone))
	sp, err := sh.PrimaryItem()
	if err != nil {
		t.Fatalf("standalone PrimaryItem: %v", err)
	}
	if sp.Info.ItemType != "hvc1" {
		t.Errorf("standalone primary type = %q; want hvc1", sp.Info.ItemType)
	}
	if len(sp.Properties) != len(thumb.Properties) {
		t.Fatalf("standalone has %d properties; want %d", len(sp.Properties), len(thumb.Properties))
	}
	for i, p := range sp.Properties {
		if p.Type() != thumb.Properties[i].Type() {
			t.Errorf("standalone property %d = %q; want %q", i, p.Type(), thumb.Properties[i].Type())
		}
	}
	tw, th, _ := thumb.VisualDimensions()
	if w, h, _ := sp.VisualDimensions(); w != tw || h != th {
		t.Errorf("standalone visual dimensions = %dx%d; want %dx%d", w, h, tw, th)
	}
	if sp.Rotations() != thumb.Rotations() {
		t.Errorf("standalone Rotations = %d; want %d", sp.Rotations(), thumb.Rotations())
	}
	sdata, err := sp.Data()
	if err != nil {
		t.Fatalf("standalone Data: %v", err)
	}
	if !bytes.Equal(sdata, data) {
		t.Error("standalone item data differs from original")
	}
	if _, err := sh.EXIF(); err != ErrNoEXIF {
		t.Errorf("standalone EXIF error = %v; want ErrNoEXIF", err)
	}
}

// BenchmarkThumbnail measures finding and reading a primary image's
// thumbnail as a standalone HEIF file.
func BenchmarkThumbnail(b *testing.B) {
	name := "testdata/park.heic"
	if v := os.Getenv("HEIF_BENCH_FILE"); v != "" {
		name = v
	}
	src, err := os.ReadFile(name)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		h := Open(bytes.NewReader(src))
		primary, err := h.PrimaryItem()
		if err != nil {
			b.Fatal(err)
		}
		thumbs, err := h.Thumbnails(primary)
		if err != nil || len(thumbs) == 0 {
			b.Fatalf("Thumbnails = %v, %v", thumbs, err)
		}
		if _, err := h.StandaloneHEIF(thumbs[0]); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDataTruncated(t *testing.T) {
	f, err := os.Open("testdata/rotate.heic") // truncated within its thumbnail
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := Open(f)
	thumb, err := h.ItemByID(50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := thumb.Data(); err == nil {
		t.Error("Data succeeded; want error")
	}
	if _, err := h.StandaloneHEIF(thumb); err == nil {
		t.Error("StandaloneHEIF succeeded; want error")
	}
}

func TestEncodeImage(t *testing.T) {
	hvcC := Property{Box: appendBox(nil, "hvcC", []byte{1, 2, 3}), Essential: true}
	data := []byte("coded image data")
	b, err := EncodeImage("heic", "hvc1", []Property{hvcC, SpatialExtents(1920, 1440), Rotation(3)}, data)
	if err != nil {
		t.Fatal(err)
	}
	h := Open(bytes.NewReader(b))
	it, err := h.PrimaryItem()
	if err != nil {
		t.Fatal(err)
	}
	if it.Info.ItemType != "hvc1" {
		t.Errorf("item type = %q; want hvc1", it.Info.ItemType)
	}
	var types []string
	for _, p := range it.Properties {
		types = append(types, p.Type().String())
	}
	if got, want := strings.Join(types, ","), "hvcC,ispe,irot"; got != want {
		t.Fatalf("properties = %s; want %s", got, want)
	}
	// Each property's box comes back byte for byte.
	for i, want := range [][]byte{hvcC.Box, SpatialExtents(1920, 1440).Box, Rotation(3).Box} {
		body, err := io.ReadAll(it.Properties[i].Body())
		if err != nil {
			t.Fatal(err)
		}
		if got := appendBox(nil, it.Properties[i].Type().String(), body); !bytes.Equal(got, want) {
			t.Errorf("property %d = %x; want %x", i, got, want)
		}
	}
	if w, h, ok := it.SpatialExtents(); !ok || w != 1920 || h != 1440 {
		t.Errorf("SpatialExtents = %d, %d, %v; want 1920, 1440, true", w, h, ok)
	}
	if r := it.Rotations(); r != 3 {
		t.Errorf("Rotations = %d; want 3", r)
	}
	if w, h, _ := it.VisualDimensions(); w != 1440 || h != 1920 {
		t.Errorf("VisualDimensions = %dx%d; want 1440x1920", w, h)
	}
	got, err := it.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Data = %q; want %q", got, data)
	}
	meta, err := h.getMeta()
	if err != nil {
		t.Fatal(err)
	}
	if meta.FileType.MajorBrand != "heic" {
		t.Errorf("major brand = %q; want heic", meta.FileType.MajorBrand)
	}
	for i, ass := range h.propertyAssociations(it.ID) {
		if want := i != 1; ass.Essential != want { // all but ispe
			t.Errorf("property %d essential = %v; want %v", i, ass.Essential, want)
		}
	}

	for _, tt := range []struct {
		name            string
		brand, itemType string
		props           []Property
	}{
		{"brand", "hei", "hvc1", nil},
		{"item type", "heic", "hvc", nil},
		{"short box", "heic", "hvc1", []Property{{Box: []byte{0, 0, 0, 4}}}},
		{"box size", "heic", "hvc1", []Property{{Box: appendBox(nil, "hvcC", nil)[:7]}}},
		{"too many", "heic", "hvc1", make([]Property, 128)},
	} {
		if _, err := EncodeImage(tt.brand, tt.itemType, tt.props, data); err == nil {
			t.Errorf("EncodeImage with invalid %s succeeded; want error", tt.name)
		}
	}
}
