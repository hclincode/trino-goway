package script

import "os"

// readFile reads a file's contents as a string.
// Only called from LoadConfig (cold path) — never from Evaluate (hot path).
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
