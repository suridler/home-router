package nativebinding

import (
	"io"
	"os"

	"home-router/xray-core-helper/internal/nativeblock"
)

// ParseFile checks concrete helper annotations without rendering or modifying
// any values. It bounds reads before handing content to the shared parser.
func ParseFile(path string) (*Document, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, nativeblock.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}
