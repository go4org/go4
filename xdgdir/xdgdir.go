// Package xdgdir implements the Free Desktop Base Directory
// specification for locating directories.
//
// http://standards.freedesktop.org/basedir-spec/basedir-spec-latest.html
package xdgdir // import "go4.org/xdgdir"

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Directories defined by the specification.
var (
	Data = Dir{
		env:       "XDG_DATA_HOME",
		envdirs:   "XDG_DATA_DIRS",
		primary:   "$HOME/.local/share",
		secondary: []string{"/usr/local/share", "/usr/share"},
	}
	Config = Dir{
		env:       "XDG_CONFIG_HOME",
		envdirs:   "XDG_CONFIG_DIRS",
		primary:   "$HOME/.config",
		secondary: []string{"/etc/xdg"},
	}
	Cache = Dir{
		env:     "XDG_CACHE_HOME",
		primary: "$HOME/.cache",
	}
	Runtime = Dir{
		env:       "XDG_RUNTIME_DIR",
		userOwned: true,
	}
)

// A Dir is a logical base directory along with additional search
// directories.
type Dir struct {
	env       string
	envdirs   string
	primary   string
	secondary []string
	userOwned bool
}

// String returns the name of the primary environment variable for the
// directory.
func (d Dir) String() string {
	return d.env
}

// Path returns the absolute path of the primary directory, or an empty
// string if there's no suitable directory present.  This is the path
// that should be used for writing files.
func (d Dir) Path() string {
	p := d.path()
	if p != "" && d.userOwned {
		info, err := os.Stat(p)
		if err != nil {
			return ""
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return ""
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != geteuid() {
			return ""
		}
	}
	return p
}

func (d Dir) path() string {
	if e := getenv(d.env); isValidPath(e) {
		return e
	}
	badenv := false
	primary := os.Expand(d.primary, func(key string) string {
		e := getenv(key)
		if e == "" {
			badenv = true
		}
		return e
	})
	if isValidPath(primary) && !badenv {
		return primary
	}
	return ""
}

// SearchPaths returns the list of paths (in descending order of
// preference) to search for files.
func (d Dir) SearchPaths() []string {
	paths := make([]string, 0, 10)
	if p := d.Path(); p != "" {
		paths = append(paths, p)
	}
	if d.envdirs == "" {
		return paths
	}
	e := getenv(d.envdirs)
	if e == "" {
		paths = append(paths, d.secondary...)
		return paths
	}
	epaths := strings.Split(e, string(filepath.ListSeparator))
	n := 0
	for _, p := range epaths {
		if isValidPath(p) {
			epaths[n] = p
			n++
		}
	}
	paths = append(paths, epaths[:n]...)
	return paths
}

// Open opens the named file inside the directory for reading.  If the
// directory has multiple search paths, each path is checked in order
// for the file and the first one found is opened.
func (d Dir) Open(name string) (*os.File, error) {
	paths := d.SearchPaths()
	if len(paths) == 0 {
		return nil, &invalidDirError{"open", d.env, name}
	}
	for _, p := range paths {
		f, err := os.Open(filepath.Join(p, name))
		if err == nil {
			return f, nil
		}
	}
	return nil, &os.PathError{
		Op:   "Open",
		Path: filepath.Join("$"+d.env, name),
		Err:  os.ErrNotExist,
	}
}

// Create creates the named file inside the directory mode 0666 (before
// umask), truncating it if it already exists.  Parent directories of
// the file will be created with mode 0700.
func (d Dir) Create(name string) (*os.File, error) {
	p := d.Path()
	if p == "" {
		return nil, &invalidDirError{"create", d.env, name}
	}
	fp := filepath.Join(p, name)
	if err := os.MkdirAll(filepath.Dir(fp), 0700); err != nil {
		return nil, err
	}
	return os.Create(fp)
}

type invalidDirError struct {
	op   string
	env  string
	name string
}

func (e *invalidDirError) Error() string {
	return "xdgdir: " + e.op + " " + e.name + ": " + e.env + " is not set or invalid"
}

func isValidPath(path string) bool {
	return path != "" && filepath.IsAbs(path)
}

// getenv retrieves an environment variable.  It can be faked for testing.
var getenv = os.Getenv

// geteuid retrieves the effective user ID of the process.  It can be faked for testing.
var geteuid = os.Geteuid
