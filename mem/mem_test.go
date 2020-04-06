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

package mem

import "testing"

func TestRO(t *testing.T) {
	b := []byte("some memory.")
	s := "some memory."
	rb := B(b)
	rs := S(s)
	if !rb.Equal(rs) {
		t.Fatal("rb != rs")
	}
	if !rb.EqualString(s) {
		t.Errorf("not equal string")
	}
	if !rs.EqualBytes(b) {
		t.Errorf("not equal byte")
	}
	if !rb.EqualBytes(b) {
		t.Errorf("not equal bytes")
	}
	if !rs.EqualString(s) {
		t.Errorf("not equal string")
	}

	if rb.At(0) != 's' {
		t.Fatalf("[0] = %q; want 's'", rb.At(0))
	}
	b[0] = 'z'
	if rb.At(0) != 'z' {
		t.Fatalf("[0] = %q; want 'z'", rb.At(0))
	}

	var got []byte
	got = rb.Append(got)
	got = rs.Append(got)
	want := "zome memory.some memory."
	if string(got) != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestAllocs(t *testing.T) {
	b := []byte("some memory.")
	n := uint(testing.AllocsPerRun(5000, func() {
		ro := B(b)
		if ro.Len() != len(b) {
			t.Fatal("wrong length")
		}
	}))
	if n != 0 {
		t.Errorf("unexpected allocs (%d)", n)
	}
}

func TestStrconv(t *testing.T) {
	b := []byte("1234")
	i, err := B(b).ParseInt(10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if i != 1234 {
		t.Errorf("got %d; want 1234", i)
	}
}
