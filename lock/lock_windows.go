/*
Copyright 2013 The Go Authors

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

package lock

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

func init() {
	lockFn = lockWindows
}

type winUnlocker struct {
	h   windows.Handle
	abs string
	// once guards the close method call.
	once sync.Once
	// err holds the error returned by Close.
	err error
}

func (u *winUnlocker) Close() error {
	u.once.Do(u.close)
	return u.err
}

func (u *winUnlocker) close() {
	lockmu.Lock()
	defer lockmu.Unlock()
	delete(locked, u.abs)

	u.err = windows.CloseHandle(u.h)
}

func lockWindows(name string) (io.Closer, error) {
	fi, err := os.Stat(name)
	if err == nil && fi.Size() > 0 {
		return nil, fmt.Errorf("can't lock file %q: %s", name, "has non-zero size")
	}

	handle, err := winCreateEphemeral(name)
	if err != nil {
		return nil, fmt.Errorf("creation of lock %s failed: %v", name, err)
	}

	absPath, err := filepath.Abs(name)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("can't lock file %q, unable to determine full path: %s", name, err)
	}

	return &winUnlocker{h: handle, abs: absPath}, nil
}
