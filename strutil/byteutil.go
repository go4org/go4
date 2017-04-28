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

import "unicode/utf8"

// BytesContainsFold is like bytes.Contains but uses Unicode case-folding.
func BytesContainsFold(s, substr []byte) bool {
	return BytesIndexFold(s, substr) >= 0
}

// ByteIndexFold is like bytes.Contains but uses Unicode case-folding.
func BytesIndexFold(s, substr []byte) int {
	if len(substr) == 0 {
		return 0
	}
	if len(s) == 0 {
		return -1
	}
	firstRune := rune(substr[0])
	if firstRune >= utf8.RuneSelf {
		firstRune, _ = utf8.DecodeRune(substr)
	}
	pos := 0
	for {
		rune, size := utf8.DecodeRune(s)
		if EqualFoldRune(rune, firstRune) && BytesHasPrefixFold(s, substr) {
			return pos
		}
		pos += size
		s = s[size:]
		if len(s) == 0 {
			break
		}
	}
	return -1
}

// HasPrefixFold is like bytes.HasPrefix but uses Unicode case-folding.
func BytesHasPrefixFold(s, prefix []byte) bool {
	if len(prefix) == 0 {
		return true
	}
	for {
		pr, prSize := utf8.DecodeRune(prefix)
		prefix = prefix[prSize:]
		if len(s) == 0 {
			return false
		}
		// step with s, too
		sr, size := utf8.DecodeRune(s)
		if sr == utf8.RuneError {
			return false
		}
		s = s[size:]
		if !EqualFoldRune(sr, pr) {
			return false
		}
		if len(prefix) == 0 {
			break
		}
	}
	return true
}

// BytesHasSuffixFold is like bytes.HasSuffix but uses Unicode case-folding.
func BytesHasSuffixFold(s, suffix []byte) bool {
	if len(suffix) == 0 {
		return true
	}
	// count the runes and bytes in s, but only till rune count of suffix
	bo, so := len(s), len(suffix)
	for bo > 0 && so > 0 {
		r, size := utf8.DecodeLastRune(s[:bo])
		if r == utf8.RuneError {
			return false
		}
		bo -= size

		sr, size := utf8.DecodeLastRune(suffix[:so])
		if sr == utf8.RuneError {
			return false
		}
		so -= size

		if !EqualFoldRune(r, sr) {
			return false
		}
	}
	return so == 0
}
