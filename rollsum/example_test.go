/*
Copyright 2019 The Go4 Authors

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

package rollsum_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/bits"
	"math/rand"

	"go4.org/rollsum"
)

func ExampleSplitFunc() {
	const size = 50000

	// Fill a buffer with random data.
	buf := new(bytes.Buffer)
	io.CopyN(buf, rand.New(rand.NewSource(14)), size)

	// Make a scanner than splits that data up using rollsum.
	scan := bufio.NewScanner(buf)
	scan.Split(rollsum.SplitFunc())

	tot := 0
	for scan.Scan() {
		chunksize := len(scan.Bytes())
		fmt.Println("chunk size", chunksize)
		tot += chunksize
	}

	// Sanity check the result.
	if err := scan.Err(); err != nil {
		panic(err)
	}
	if tot != size {
		panic("lost or invented data!")
	}

	// Output:
	// chunk size 29793
	// chunk size 411
	// chunk size 111
	// chunk size 5955
	// chunk size 805
	// chunk size 4523
	// chunk size 7765
	// chunk size 637
}

func ExampleSplitFuncWithBits() {
	const size = 50000

	// Fill a buffer with random data.
	buf := new(bytes.Buffer)
	io.CopyN(buf, rand.New(rand.NewSource(14)), size)

	// Make a scanner than splits that data up using rollsum, using only 4 bits to split.
	// Track the size distribution, on a logarithmic scale.
	scan := bufio.NewScanner(buf)
	scan.Split(rollsum.SplitFuncWithBits(4))
	var loghist [8]int
	for scan.Scan() {
		chunksize := len(scan.Bytes())
		loglen := bits.Len(uint(chunksize))
		loghist[loglen]++
	}

	fmt.Println("Log₂ histogram of chunk sizes:", loghist)

	// Sanity check the result.
	if err := scan.Err(); err != nil {
		panic(err)
	}

	// Output:
	// Log₂ histogram of chunk sizes: [0 197 344 603 804 728 366 61]
}
