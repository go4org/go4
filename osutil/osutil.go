/*
Copyright 2015 The go4 Authors

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

// Package osutil contains os level functions.
package osutil // import "go4.org/osutil"

import "os" // capture executable on package init to work around various os issues if

// Executable returns [os.Executable]. This function predates the Go standard
// library's os.Executable and is retained here for compatibility.
//
// Deprecated: use os.Executable directly instead.
func Executable() (string, error) {
	return os.Executable()
}
