package geodatainstall

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const oPath = 0x200000

type File interface {
	io.Reader
	io.Writer
	Stat() (os.FileInfo, error)
	Chmod(os.FileMode) error
	Chown(int, int) error
	Sync() error
	Close() error
}

type Directory interface {
	Stat() (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	ReadDir() ([]string, error)
	Mkdir(string, os.FileMode) error
	Chmod(os.FileMode) error
	OpenDir(string) (Directory, error)
	OpenFile(string, int, os.FileMode) (File, error)
	Rename(string, string) error
	Remove(string) error
	RemoveDir(string) error
	Sync() error
	Close() error
}

type FileSystem interface {
	OpenDir(string) (Directory, error)
	OpenSource(string) (File, error)
}

type osFileSystem struct{}

type osDirectory struct {
	file *os.File
}

func (osFileSystem) OpenDir(path string) (Directory, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidInput
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return &osDirectory{file: os.NewFile(uintptr(fd), path)}, nil
}

// OpenDirectory exposes the installer's no-follow directory opener to other
// fixed-purpose geodata transaction packages.
func OpenDirectory(path string) (Directory, error) {
	return (osFileSystem{}).OpenDir(path)
}

// OpenSourceFile opens one absolute source path without following its final
// path component.
func OpenSourceFile(path string) (File, error) {
	return (osFileSystem{}).OpenSource(path)
}

func (osFileSystem) OpenSource(path string) (File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return nil, ErrInvalidInput
	}
	parent, err := (osFileSystem{}).OpenDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	directory := parent.(*osDirectory)
	fd, openErr := syscall.Openat(int(directory.file.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	closeErr := parent.Close()
	if openErr != nil {
		return nil, openErr
	}
	if closeErr != nil {
		_ = syscall.Close(fd)
		return nil, closeErr
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (directory *osDirectory) Stat() (os.FileInfo, error) {
	return directory.file.Stat()
}

func (directory *osDirectory) Lstat(name string) (os.FileInfo, error) {
	if !validComponent(name) {
		return nil, ErrInvalidInput
	}
	fd, err := syscall.Openat(int(directory.file.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	return file.Stat()
}

func (directory *osDirectory) ReadDir() ([]string, error) {
	fd, err := syscall.Openat(int(directory.file.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), directory.file.Name())
	defer file.Close()
	names, err := file.Readdirnames(0)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return names, err
}

func (directory *osDirectory) Mkdir(name string, mode os.FileMode) error {
	if !validComponent(name) {
		return ErrInvalidInput
	}
	return syscall.Mkdirat(int(directory.file.Fd()), name, uint32(mode.Perm()))
}

func (directory *osDirectory) Chmod(mode os.FileMode) error {
	return directory.file.Chmod(mode)
}

func (directory *osDirectory) OpenDir(name string) (Directory, error) {
	if !validComponent(name) {
		return nil, ErrInvalidInput
	}
	fd, err := syscall.Openat(int(directory.file.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &osDirectory{file: os.NewFile(uintptr(fd), name)}, nil
}

func (directory *osDirectory) OpenFile(name string, flags int, mode os.FileMode) (File, error) {
	if !validComponent(name) {
		return nil, ErrInvalidInput
	}
	fd, err := syscall.Openat(int(directory.file.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (directory *osDirectory) Rename(oldName, newName string) error {
	if !validComponent(oldName) || !validComponent(newName) {
		return ErrInvalidInput
	}
	return syscall.Renameat(int(directory.file.Fd()), oldName, int(directory.file.Fd()), newName)
}

func (directory *osDirectory) Remove(name string) error {
	if !validComponent(name) {
		return ErrInvalidInput
	}
	return syscall.Unlinkat(int(directory.file.Fd()), name)
}

func (directory *osDirectory) RemoveDir(name string) error {
	if !validComponent(name) {
		return ErrInvalidInput
	}
	value, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, directory.file.Fd(), uintptr(unsafe.Pointer(value)), uintptr(0x200))
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}

func (directory *osDirectory) Sync() error {
	return directory.file.Sync()
}

func (directory *osDirectory) Close() error {
	return directory.file.Close()
}

func validComponent(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}

func fileIdentity(first, second os.FileInfo) bool {
	return first != nil && second != nil && os.SameFile(first, second)
}

func owner(info os.FileInfo) (int, int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("file ownership is unavailable")
	}
	return int(stat.Uid), int(stat.Gid), nil
}

func mode(info os.FileInfo) uint32 {
	return uint32(info.Mode().Perm() | info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}

func modeValue(value uint32) os.FileMode {
	return os.FileMode(value)
}

func within(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
