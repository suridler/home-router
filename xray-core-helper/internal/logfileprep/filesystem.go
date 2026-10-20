package logfileprep

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

type FileSystem interface {
	OpenRoot() (Directory, error)
}

type Directory interface {
	Info() (os.FileInfo, error)
	OpenDirectory(string) (Directory, error)
	Mkdir(string, os.FileMode) error
	OpenFile(string, int, os.FileMode) (File, error)
	Remove(string) error
	RemoveDirectory(string) error
	Chown(int, int) error
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

type File interface {
	Info() (os.FileInfo, error)
	Chown(int, int) error
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

type osFileSystem struct{}

type osDirectory struct {
	file *os.File
}

type osFile struct {
	file *os.File
}

func (osFileSystem) OpenRoot() (Directory, error) {
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &osDirectory{file: os.NewFile(uintptr(fd), "/")}, nil
}

func (directory *osDirectory) Info() (os.FileInfo, error) {
	return directory.file.Stat()
}

func (directory *osDirectory) OpenDirectory(name string) (Directory, error) {
	if !validComponent(name) {
		return nil, errors.New("invalid directory component")
	}
	fd, err := syscall.Openat(int(directory.file.Fd()), name,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &osDirectory{file: os.NewFile(uintptr(fd), name)}, nil
}

func (directory *osDirectory) Mkdir(name string, mode os.FileMode) error {
	if !validComponent(name) {
		return errors.New("invalid directory component")
	}
	return syscall.Mkdirat(int(directory.file.Fd()), name, uint32(mode.Perm()))
}

func (directory *osDirectory) OpenFile(name string, flags int, mode os.FileMode) (File, error) {
	if !validComponent(name) {
		return nil, errors.New("invalid file component")
	}
	fd, err := syscall.Openat(int(directory.file.Fd()), name,
		flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return &osFile{file: os.NewFile(uintptr(fd), name)}, nil
}

func (directory *osDirectory) Remove(name string) error {
	if !validComponent(name) {
		return errors.New("invalid file component")
	}
	return syscall.Unlinkat(int(directory.file.Fd()), name)
}

func (directory *osDirectory) RemoveDirectory(name string) error {
	if !validComponent(name) {
		return errors.New("invalid directory component")
	}
	value, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, directory.file.Fd(),
		uintptr(unsafe.Pointer(value)), uintptr(0x200))
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}

func (directory *osDirectory) Chown(uid, gid int) error {
	return directory.file.Chown(uid, gid)
}

func (directory *osDirectory) Chmod(mode os.FileMode) error {
	return directory.file.Chmod(mode)
}

func (directory *osDirectory) Sync() error {
	return directory.file.Sync()
}

func (directory *osDirectory) Close() error {
	return directory.file.Close()
}

func (file *osFile) Info() (os.FileInfo, error) {
	return file.file.Stat()
}

func (file *osFile) Chown(uid, gid int) error {
	return file.file.Chown(uid, gid)
}

func (file *osFile) Chmod(mode os.FileMode) error {
	return file.file.Chmod(mode)
}

func (file *osFile) Sync() error {
	return file.file.Sync()
}

func (file *osFile) Close() error {
	return file.file.Close()
}

func validComponent(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}
