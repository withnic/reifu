package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func sharingViolation(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(32))
}

func canFallback(err error) bool {
	return sharingViolation(err) || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EXDEV) ||
		errors.Is(err, syscall.Errno(17)) || // ERROR_NOT_SAME_DEVICE
		errors.Is(err, syscall.Errno(50)) || // ERROR_NOT_SUPPORTED
		errors.Is(err, syscall.Errno(1314)) // ERROR_PRIVILEGE_NOT_HELD
}

func publishNew(staged, target string) error {
	from, err := syscall.UTF16PtrFromString(extendedPath(staged))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(extendedPath(target))
	if err != nil {
		return err
	}
	return retrySharing(func() error { return syscall.MoveFile(from, to) })
}

func extendedPath(path string) string {
	volume := filepath.VolumeName(path)
	if len(volume) == 2 && volume[1] == ':' {
		return "\\\\?\\" + path
	}
	if len(volume) >= 3 && volume[:2] == "\\\\" && volume[2] != '?' && volume[2] != '.' {
		return "\\\\?\\UNC\\" + path[2:]
	}
	return path
}

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func openLock(path string, create bool) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return nil, err
	}
	disposition := uint32(syscall.OPEN_EXISTING)
	if create {
		// CREATE_NEW is the Windows equivalent of O_CREATE|O_EXCL.
		disposition = syscall.CREATE_NEW
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, disposition, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open lock", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func tryLock(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	// Lock one byte past the metadata so file contents remain readable.
	overlapped.Offset = 4096
	const exclusiveFailImmediately = 3
	result, _, err := lockFileEx.Call(file.Fd(), exclusiveFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return true, nil
	}
	if errors.Is(err, syscall.Errno(33)) { // ERROR_LOCK_VIOLATION
		return false, nil
	}
	return false, err
}
