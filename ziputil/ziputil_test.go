package ziputil

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"go4.org/readerutil"
)

type testZip struct {
	name      string
	data      []byte
	fileNames []string
}

func (tz *testZip) Len() int    { return len(tz.data) }
func (tz *testZip) Size() int64 { return int64(len(tz.data)) }

func mkZip(t testing.TB, name string, numFiles int) *testZip {
	tz := &testZip{name: name}

	// Create a zip file in memory with numFiles files.
	// Each file is named "fileN.txt" and contains "This is file N".
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < numFiles; i++ {
		fileName := fmt.Sprintf("file%d.txt", i)
		tz.fileNames = append(tz.fileNames, fileName)
		fw, _ := zw.Create(fileName)
		fw.Write([]byte(fmt.Sprintf("This is file %d", i)))
	}
	zw.Close()
	t.Logf("created zip with %d files; size=%d bytes", numFiles, buf.Len())

	tz.data = buf.Bytes()
	return tz
}

func TestZipTOCSize(t *testing.T) {
	zipSmall := mkZip(t, "small", 1)
	zipMed := mkZip(t, "med", 100)
	zipLarge := mkZip(t, "large", 10000)

	tests := []struct {
		name   string
		zip    *testZip
		footer int   // footer length of zip to pass
		want   int64 // or -1 for wanting ok=false
	}{
		{
			name:   "smallzip-21",
			zip:    zipSmall,
			footer: 21,
			want:   -1,
		},
		{
			name:   "smallzip-22",
			zip:    zipSmall,
			footer: 22,
			want:   77,
		},
		{
			name:   "smallzip-all",
			zip:    zipSmall,
			footer: len(zipSmall.data),
			want:   77,
		},
		{
			name:   "medzip-1024",
			zip:    zipMed,
			footer: 1024,
			want:   5612,
		},
		{
			name:   "largezip-21",
			zip:    zipLarge,
			footer: 21,
			want:   -1,
		},
		{
			name:   "largezip-22",
			zip:    zipLarge,
			footer: 22,
			want:   578912,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.footer > tt.zip.Len() {
				panic("bad test")
			}
			footer := tt.zip.data[len(tt.zip.data)-tt.footer:]
			tocSize, ok := ZipTOCSize(int64(len(tt.zip.data)), footer)
			got := tocSize
			if !ok {
				got = -1
			} else {
				if got >= tt.zip.Size() {
					t.Errorf("unexpected tocSize %d is >= file size %d", got, tt.zip.Size())
				}
			}
			if got != tt.want {
				if tt.want == -1 {
					t.Fatalf("got tocSize = %d; want ok=false", tocSize)
				}
				if !ok {
					t.Fatalf("got ok=false; want tocSize=%d", tt.want)
				}
				t.Fatalf("got tocSize=%d; want %d", tocSize, tt.want)
			}
			if !ok {
				return
			}
			// Verify that archive/zip will read the TOC, with fake zero bytes
			// for the rest of the file.
			dataLen := tt.zip.Size() - tocSize
			ra := readerutil.NewMultiReaderAt(
				readerutil.ZeroSizeReaderAt(dataLen),
				bytes.NewReader(tt.zip.data[tt.zip.Size()-tocSize:]),
			)
			t.Logf("total size = %d (tocSize = %d; dataLen = %d)", tt.zip.Size(), tocSize, dataLen)
			zr, err := zip.NewReader(ra, tt.zip.Size())
			if err != nil {
				t.Fatalf("zip.NewReader error: %v", err)
			}
			var gotNames []string
			for _, f := range zr.File {
				gotNames = append(gotNames, f.Name)
			}
			if !reflect.DeepEqual(gotNames, tt.zip.fileNames) {
				t.Fatalf("got file names = %q; want %q", gotNames, tt.zip.fileNames)
			}
		})
	}
}

func goTestZips(t testing.TB) (baseNames []string) {
	fe, err := os.ReadDir("testdata") // from Go's archive/zip testdata
	if err != nil {
		t.Fatalf("os.ReadDir testdata: %v", err)
	}
	for _, de := range fe {
		if !strings.HasSuffix(de.Name(), ".zip") {
			continue
		}
		baseNames = append(baseNames, de.Name())
	}
	return
}

func TestGoTestZips(t *testing.T) {
	for _, zipBase := range goTestZips(t) {
		zipData, err := os.ReadFile(filepath.Join("testdata", zipBase))
		if err != nil {
			t.Fatal(err)
		}

		wantZF, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
		if err != nil {
			t.Fatalf("zip.NewReader on %s: %v", zipBase, err)
		}

		t.Run(zipBase, func(t *testing.T) {
			tocSize, ok := ZipTOCSize(int64(len(zipData)), zipData)
			if !ok {
				t.Fatalf("ZipTOCSize failed")
			}
			dataLen := int64(len(zipData)) - int64(tocSize)
			ra := readerutil.NewMultiReaderAt(
				readerutil.ZeroSizeReaderAt(dataLen),
				bytes.NewReader(zipData[len(zipData)-int(tocSize):]),
			)
			gotZF, err := zip.NewReader(ra, int64(len(zipData)))
			if err != nil {
				t.Fatalf("zip.NewReader error: %v", err)
			}

			if len(gotZF.File) != len(wantZF.File) {
				t.Fatalf("got %d files; want %d files", len(gotZF.File), len(wantZF.File))
			}
			for i := range gotZF.File {
				got, want := gotZF.File[i].FileHeader, wantZF.File[i].FileHeader
				if !reflect.DeepEqual(got, want) {
					t.Errorf("file %d: got header %+v; want %+v", i, got, want)
				}
			}
		})
	}
}

func TestOpenWithReader(t *testing.T) {
	for _, zipBase := range goTestZips(t) {
		t.Run(zipBase, func(t *testing.T) {

			zipData, err := os.ReadFile(filepath.Join("testdata", zipBase))
			if err != nil {
				t.Fatal(err)
			}
			zipSize := int64(len(zipData))

			zf, err := zip.NewReader(bytes.NewReader(zipData), zipSize)
			if err != nil {
				t.Fatalf("zip.OpenReader on %s: %v", zipBase, err)
			}

			ur, err := ParseTOC(zipSize, zipData)
			if err != nil {
				t.Fatalf("ParseTOC: %v", err)
			}
			if len(ur.File) != len(zf.File) {
				t.Fatalf("ParseTOC got %d files; want %d files", len(ur.File), len(zf.File))
			}

			for i, zf := range zf.File {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					rc, err := zf.Open()
					if err != nil {
						t.Fatal(err)
					}
					defer rc.Close()
					want, err := io.ReadAll(rc)
					if err != nil {
						t.Fatalf("ReadAll: %v", err)
					}

					fh := ur.File[i]
					var sawClose atomic.Bool // was our closeTracker's Close called?
					rc, err = OpenWithReader(fh, func(off, size int64) (io.ReadCloser, error) {
						return &closeTracker{
							sawClose: &sawClose,
							Reader:   io.NewSectionReader(bytes.NewReader(zipData), off, size),
						}, nil
					})
					if err != nil {
						t.Fatal(err)
					}
					got, err := io.ReadAll(rc)
					rc.Close()
					if err != nil {
						t.Fatalf("ReadAll: %v", err)
					}
					if !bytes.Equal(got, want) {
						t.Errorf("file %d: contents mismatch", i)
					}
					if !sawClose.Load() {
						t.Errorf("didn't see Close")
					}
					if !t.Failed() {
						t.Logf("pass; %d bytes, desc=%v", len(got), fh.hasDataDesc())
					}
				})
			}
		})
	}
}

type closeTracker struct {
	io.Reader
	sawClose *atomic.Bool
}

func (c *closeTracker) Close() error {
	c.sawClose.Store(true)
	return nil
}
