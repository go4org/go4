/*
Copyright 2016 The Go4 Authors.

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

package readerutil

import (
	"io"
	"io/ioutil"
	"strings"
	"testing"
)

func TestMultiReaderAt(t *testing.T) {
	sra := NewMultiReaderAt(
		io.NewSectionReader(strings.NewReader("xaaax"), 1, 3),
		io.NewSectionReader(strings.NewReader("xxbbbbxx"), 2, 3),
		io.NewSectionReader(strings.NewReader("cccx"), 0, 3),
	)
	if sra.Size() != 9 {
		t.Fatalf("Size = %d; want 9", sra.Size())
	}
	const full = "aaabbbccc"
	for start := 0; start < len(full); start++ {
		for end := start; end < len(full); end++ {
			want := full[start:end]
			got, err := ioutil.ReadAll(io.NewSectionReader(sra, int64(start), int64(end-start)))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("for start=%d, end=%d: ReadAll = %q; want %q", start, end, got, want)
			}
		}
	}
}

func TestMultiReaderAtEmptyParts(t *testing.T) {
	tests := []struct {
		name    string
		parts   []string
		off     int64
		length  int
		want    string
		wantErr error
	}{
		{name: "nonempty", parts: []string{"ab", "cd"}, length: 4, want: "abcd"},
		{name: "middle", parts: []string{"ab", "", "cd"}, length: 4, want: "abcd"},
		{name: "consecutive", parts: []string{"ab", "", "", "cd"}, length: 4, want: "abcd"},
		{name: "leading_and_trailing", parts: []string{"", "ab", "cd", ""}, length: 4, want: "abcd"},
		{name: "offset_across_empty", parts: []string{"ab", "", "", "cde"}, off: 1, length: 3, want: "bcd"},
		{name: "offset_at_boundary", parts: []string{"ab", "", "", "cd"}, off: 2, length: 2, want: "cd"},
		{name: "stop_before_empty", parts: []string{"ab", "", "cd"}, length: 2, want: "ab"},
		{name: "past_end", parts: []string{"ab", "", "cd", ""}, off: 1, length: 4, want: "bcd", wantErr: io.ErrUnexpectedEOF},
		{name: "all_empty", parts: []string{"", ""}, length: 1, wantErr: io.ErrUnexpectedEOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parts []SizeReaderAt
			for _, part := range tt.parts {
				parts = append(parts, strings.NewReader(part))
			}
			sra := NewMultiReaderAt(parts...)
			buf := make([]byte, tt.length)
			n, err := sra.ReadAt(buf, tt.off)
			if n != len(tt.want) || err != tt.wantErr || string(buf[:n]) != tt.want {
				t.Errorf("ReadAt(%d bytes, %d) = %d, %v, %q; want %d, %v, %q",
					tt.length, tt.off, n, err, buf[:n], len(tt.want), tt.wantErr, tt.want)
			}
		})
	}
}
