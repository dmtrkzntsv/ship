package host

import "os"

// SetWriteUnitFile replaces how Update writes the new unit file and returns
// a function restoring the original.
func SetWriteUnitFile(f func(path string, data []byte, mode os.FileMode) error) (restore func()) {
	old := writeUnitFile
	writeUnitFile = f
	return func() { writeUnitFile = old }
}
