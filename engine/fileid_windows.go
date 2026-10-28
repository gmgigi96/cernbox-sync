//go:build windows

package engine

import (
	"io/fs"
	"syscall"
)

// fileIdentity returns the volume serial number and the NTFS file index of
// path, the Windows equivalent of device and inode, or zeros on failure.
func fileIdentity(path string, _ fs.FileInfo) (dev, ino uint64) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	// FILE_FLAG_BACKUP_SEMANTICS is required to open directories; zero access
	// rights are enough to read the file information.
	h, err := syscall.CreateFile(p, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, 0
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &d); err != nil {
		return 0, 0
	}
	return uint64(d.VolumeSerialNumber), uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)
}
