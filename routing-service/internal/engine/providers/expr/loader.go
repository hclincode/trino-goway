package expr

import "os"

// readFile reads a file's contents as a string. Isolated here so that
// CONVENTIONS.md's "no I/O in hot path" rule is easy to audit:
// readFile is only called from LoadConfig (the cold path), never from Evaluate.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
