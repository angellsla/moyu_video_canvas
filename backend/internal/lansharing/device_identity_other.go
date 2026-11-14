//go:build !windows

package lansharing

// Other desktop platforms retain their workspace identity; Windows obtains
// its device ID from SMBIOS and stores credentials in the Documents folder.
func LoadDeviceIdentity(dataDir string) (*Identity, error) {
	return LoadIdentity(dataDir)
}
