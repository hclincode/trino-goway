package diffharness

import "os"

func writeFileBytes(path string, content []byte) error {
	return os.WriteFile(path, content, 0o600)
}
