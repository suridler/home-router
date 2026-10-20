package transaction

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

type ByteEdit struct {
	Start       int
	End         int
	Replacement []byte
}

func ApplyEdits(source []byte, edits []ByteEdit) ([]byte, error) {
	ordered := append([]ByteEdit(nil), edits...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Start == ordered[right].Start {
			return ordered[left].End < ordered[right].End
		}
		return ordered[left].Start < ordered[right].Start
	})
	previousEnd := -1
	previousStart := -1
	for _, edit := range ordered {
		if edit.Start < 0 || edit.End < edit.Start || edit.End > len(source) {
			return nil, errors.New("JSONC edit range is invalid")
		}
		if edit.Start < previousEnd || edit.Start == previousStart {
			return nil, errors.New("JSONC edit ranges overlap")
		}
		previousStart = edit.Start
		previousEnd = edit.End
	}
	var result bytes.Buffer
	result.Grow(len(source))
	position := 0
	for _, edit := range ordered {
		if _, err := result.Write(source[position:edit.Start]); err != nil {
			return nil, fmt.Errorf("apply JSONC edit prefix: %w", err)
		}
		if _, err := result.Write(edit.Replacement); err != nil {
			return nil, fmt.Errorf("apply JSONC replacement: %w", err)
		}
		position = edit.End
	}
	if _, err := result.Write(source[position:]); err != nil {
		return nil, fmt.Errorf("apply JSONC edit suffix: %w", err)
	}
	return result.Bytes(), nil
}
