/*
Copyright 2013 The Camlistore Authors

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

package strutil

import (
	"bytes"
	"testing"
)

func TestBytesContainsFold(t *testing.T) {
	for _, tt := range containsFoldTests {
		r := BytesContainsFold([]byte(tt.s), []byte(tt.substr))
		if r != tt.result {
			t.Errorf("BytesContainsFold(%q, %q) returned %v", tt.s, tt.substr, r)
		}
	}
}

func TestBytesHasPrefixFold(t *testing.T) {
	for _, tt := range hasPrefixFoldTests {
		r := BytesHasPrefixFold([]byte(tt.s), []byte(tt.prefix))
		if r != tt.result {
			t.Errorf("BytesHasPrefixFold(%q, %q) returned %v", tt.s, tt.prefix, r)
		}
	}
}

func BenchmarkBytesHasSuffixFoldToLower(tb *testing.B) {
	a, b := "camlik", "AMLI\u212A"
	aB, bB := bytes.ToLower([]byte(a)), bytes.ToLower([]byte(b))
	for i := 0; i < tb.N; i++ {
		if !bytes.HasSuffix(aB, bB) {
			tb.Fatalf("%q should have the same suffix as %q", a, b)
		}
	}
}
func BenchmarkBytesHasSuffixFold(tb *testing.B) {
	a, b := []byte("camlik"), []byte("AMLI\u212A")
	for i := 0; i < tb.N; i++ {
		if !BytesHasSuffixFold(a, b) {
			tb.Fatalf("%q should have the same suffix as %q", a, b)
		}
	}
}

func BenchmarkBytesHasPrefixFoldToLower(tb *testing.B) {
	a, b := "kamlistore", "\u212AAMLI"
	aB, bB := bytes.ToLower([]byte(a)), bytes.ToLower([]byte(b))
	for i := 0; i < tb.N; i++ {
		if !bytes.HasPrefix(aB, bB) {
			tb.Fatalf("%q should have the same suffix as %q", a, b)
		}
	}
}
func BenchmarkBytesHasPrefixFold(tb *testing.B) {
	a, b := []byte("kamlistore"), []byte("\u212AAMLI")
	for i := 0; i < tb.N; i++ {
		if !BytesHasPrefixFold(a, b) {
			tb.Fatalf("%q should have the same suffix as %q", a, b)
		}
	}
}
