// Copyright 2015 The go4 Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build freebsd darwin

package osutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var cacheWD, cacheWDErr = os.Getwd()

func exec_darwin() (string, error) {
	mib := [4]int32{1 /* CTL_KERN */, 38 /* KERN_PROCARGS */, int32(os.Getpid()), -1}
	n := uintptr(0)
	// get length
	_, _, err := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4, 0, uintptr(unsafe.Pointer(&n)), 0, 0)
	if err != 0 {
		return "", err
	}
	if n == 0 { // shouldn't happen
		return "", nil
	}
	buf := make([]byte, n)
	_, _, err = syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if err != 0 {
		return "", err
	}
	if n == 0 { // shouldn't happen
		return "", nil
	}
	// Because KERN_PROC_ARGS returns a list of NULL separated items,
	// and we want the first one.
	parts := bytes.Split(buf[:n-1], []byte{0})
	if len(parts) < 2 {
		return "", nil
	}
	p := string(parts[0])
	if !filepath.IsAbs(p) {
		if cacheWDErr != nil {
			return p, cacheWDErr
		}
		p = filepath.Join(cacheWD, filepath.Clean(p))
	}
	return filepath.EvalSymlinks(p)
}

func exec_freebsd() (string, error) {
	mib := [4]int32{1 /* CTL_KERN */, 14 /* KERN_PROC */, 12 /* KERN_PROC_PATHNAME */, -1}

	n := uintptr(0)
	// get length
	_, _, err := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4, 0, uintptr(unsafe.Pointer(&n)), 0, 0)
	if err != 0 {
		return "", err
	}
	if n == 0 { // shouldn't happen
		return "", nil
	}
	buf := make([]byte, n)
	_, _, err = syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if err != 0 {
		return "", err
	}
	if n == 0 { // shouldn't happen
		return "", nil
	}
	p := string(buf[:n-1])
	if !filepath.IsAbs(p) {
		if cacheWDErr != nil {
			return p, cacheWDErr
		}
		p = filepath.Join(cacheWD, filepath.Clean(p))
	}
	return filepath.EvalSymlinks(p)
}

func executable() (string, error) {
	switch runtime.GOOS {
	case "freebsd":
		return exec_freebsd()
	case "darwin":
		return exec_darwin()
	}
	return "", errors.New("unsupported OS")
}
