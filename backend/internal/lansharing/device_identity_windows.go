package lansharing

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func LoadDeviceIdentity(dataDir string) (*Identity, error) {
	documents, err := windows.KnownFolderPath(windows.FOLDERID_Documents, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return nil, fmt.Errorf("locate Documents directory: %w", err)
	}
	systemUUID, err := readSystemUUID()
	if err != nil {
		return nil, err
	}
	return loadDeviceIdentityAt(dataDir, filepath.Join(documents, "MoyuCanvas"), systemUUID)
}

func readSystemUUID() (string, error) {
	getTable := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemFirmwareTable")
	if err := getTable.Find(); err != nil {
		return "", err
	}
	const rsmb = 0x52534d42
	size, _, err := getTable.Call(rsmb, 0, 0, 0)
	if size == 0 {
		return "", fmt.Errorf("read SMBIOS table size: %w", err)
	}
	if size > 16<<20 {
		return "", errors.New("SMBIOS table exceeds size limit")
	}
	buffer := make([]byte, int(size))
	written, _, err := getTable.Call(rsmb, 0, uintptr(unsafe.Pointer(&buffer[0])), size)
	if written == 0 {
		return "", fmt.Errorf("read SMBIOS table: %w", err)
	}
	if written > size {
		return "", errors.New("SMBIOS table size changed during read")
	}
	return systemUUIDFromSMBIOS(buffer[:written])
}
