//go:build windows

package fsx

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const (
	fsctlSetReparsePoint = 0x000900a4
	fsctlGetReparsePoint = 0x000900a8
	mountPointTag        = 0xa0000003
	maxReparseBuffer     = 16 * 1024
)

func createJunction(linkPath, targetAbs string) (err error) {
	targetAbs = filepath.Clean(targetAbs)
	volume := filepath.VolumeName(targetAbs)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(targetAbs) {
		return fmt.Errorf("junction target %q must be on a local drive: %w", targetAbs, syscall.Errno(50))
	}
	substitute, err := syscall.UTF16FromString("\\??\\" + targetAbs)
	if err != nil {
		return err
	}
	printName, err := syscall.UTF16FromString(targetAbs)
	if err != nil {
		return err
	}
	// Mount-point REPARSE_DATA_BUFFER offsets are byte offsets from PathBuffer.
	data := make([]byte, 16+2*(len(substitute)+len(printName)))
	if len(data) > maxReparseBuffer {
		return fmt.Errorf("junction target %q exceeds the reparse buffer: %w", targetAbs, syscall.Errno(206))
	}
	binary.LittleEndian.PutUint32(data[0:4], mountPointTag)
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(data)-8))
	binary.LittleEndian.PutUint16(data[10:12], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(data[12:14], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[14:16], uint16(2*(len(printName)-1)))
	for i, unit := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(data[16+2*i:], unit)
	}

	if err := os.Mkdir(linkPath, 0755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeTemp(linkPath))
		}
	}()
	return retrySharing(func() error {
		handle, err := openReparsePoint(linkPath, syscall.GENERIC_WRITE)
		if err != nil {
			return err
		}
		var returned uint32
		err = syscall.DeviceIoControl(handle, fsctlSetReparsePoint, &data[0], uint32(len(data)), nil, 0, &returned, nil)
		return errors.Join(err, syscall.CloseHandle(handle))
	})
}

func openReparsePoint(path string, access uint32) (syscall.Handle, error) {
	name, err := syscall.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return syscall.InvalidHandle, err
	}
	return syscall.CreateFile(name, access, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

func isJunction(path string) bool {
	handle, err := openReparsePoint(path, 0)
	if err != nil {
		return false
	}
	var data [maxReparseBuffer]byte
	var returned uint32
	err = syscall.DeviceIoControl(handle, fsctlGetReparsePoint, nil, 0, &data[0], uint32(len(data)), &returned, nil)
	syscall.CloseHandle(handle)
	return err == nil && returned >= 8 && binary.LittleEndian.Uint32(data[:4]) == mountPointTag
}

func isDirReparsePoint(info os.FileInfo, _ string) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func junctionUnsupported(err error) bool {
	return errors.Is(err, syscall.Errno(1)) || // ERROR_INVALID_FUNCTION
		errors.Is(err, syscall.Errno(206)) // ERROR_FILENAME_EXCED_RANGE
}

func removeOrdinaryDir(path string) error {
	// Inspect every child before recursing, including junctions that an
	// older Go runtime may report as directories.
	children, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := removeDirEntry(filepath.Join(path, child.Name())); err != nil {
			return err
		}
	}
	return removeTemp(path)
}

func moveNewDirEntry(src, dst string) error {
	if err := publishNew(src, dst); err != nil {
		return fmt.Errorf("move entry %q to %q: %w", src, dst, err)
	}
	return nil
}
