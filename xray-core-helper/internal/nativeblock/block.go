// Package nativeblock builds bounded native-file candidates by replacing text
// between explicit markers. It does not interpret or apply nftables rules.
package nativeblock

import (
	"bytes"
	"errors"
)

const MaxBytes = 1 << 20

const (
	Begin = "# helper:begin gateway"
	End   = "# helper:end gateway"
)

var (
	ErrMarkers    = errors.New("native_block_invalid_markers")
	ErrInitialize = errors.New("native_block_initialization_required")
	ErrSize       = errors.New("native_block_content_too_large")
)

// Candidate is internal secret-bearing transaction data, not an API projection.
// ReviewRequired flags initialization of nonempty content; it is not evidence
// of operator approval. Callers must obtain that approval before committing.
type Candidate struct {
	Content        []byte
	Initialized    bool
	ReviewRequired bool
}

type span struct {
	start, content, end, after int
	found                      bool
}

// Content returns only managed content, not the marker lines or surrounding
// file. It is internal secret-bearing data, never an ordinary API projection.
func Content(raw []byte) ([]byte, error) {
	if len(raw) > MaxBytes {
		return nil, ErrSize
	}
	block, err := locate(raw)
	if err != nil {
		return nil, err
	}
	if !block.found {
		return nil, ErrInitialize
	}
	return bytes.Clone(raw[block.content:block.end]), nil
}

// Build preserves all system bytes outside the marker contents. Both markers
// absent requires explicit initialization; malformed markers never initialize.
// A missing system file is represented by nil; its existence and metadata must
// be journaled separately by the caller for rollback. No input is modified.
func Build(system, template []byte, initialize bool) (Candidate, error) {
	if len(system) > MaxBytes || len(template) > MaxBytes {
		return Candidate{}, ErrSize
	}
	source, err := locate(template)
	if err != nil || !source.found {
		return Candidate{}, ErrMarkers
	}
	target, err := locate(system)
	if err != nil {
		return Candidate{}, err
	}
	var parts [][]byte
	result := Candidate{}
	if target.found {
		parts = [][]byte{system[:target.content], template[source.content:source.end], system[target.end:]}
	} else {
		if !initialize {
			return Candidate{}, ErrInitialize
		}
		result.Initialized = true
		result.ReviewRequired = len(system) != 0
		parts = append(parts, system)
		if len(system) != 0 && system[len(system)-1] != '\n' {
			parts = append(parts, []byte("\n"))
		}
		parts = append(parts, template[source.start:source.after])
	}
	total := 0
	for _, part := range parts {
		if len(part) > MaxBytes-total {
			return Candidate{}, ErrSize
		}
		total += len(part)
	}
	result.Content = make([]byte, 0, total)
	for _, part := range parts {
		result.Content = append(result.Content, part...)
	}
	return result, nil
}

func locate(raw []byte) (span, error) {
	var result span
	opened, closed := false, false
	for offset := 0; offset < len(raw); {
		next := bytes.IndexByte(raw[offset:], '\n')
		after := len(raw)
		if next >= 0 {
			after = offset + next + 1
		}
		line := bytes.TrimSpace(raw[offset:after])
		switch {
		case bytes.Equal(line, []byte(Begin)):
			if opened || closed || next < 0 {
				return span{}, ErrMarkers
			}
			opened = true
			result.start, result.content = offset, after
		case bytes.Equal(line, []byte(End)):
			if !opened || closed {
				return span{}, ErrMarkers
			}
			closed = true
			result.end, result.after = offset, after
		case bytes.HasPrefix(line, []byte("# helper:begin")), bytes.HasPrefix(line, []byte("# helper:end")):
			return span{}, ErrMarkers
		}
		offset = after
	}
	if opened != closed {
		return span{}, ErrMarkers
	}
	result.found = closed
	return result, nil
}
