package xrayconfig

import "home-router/xray-core-helper/internal/transaction"

// session applies narrow edit steps to successive snapshots of one document.
type session struct {
	original []byte
	current  []byte
}

func newSession(source []byte) *session {
	return &session{original: source, current: source}
}

func (s *session) apply(step func(*editor) ([]transaction.ByteEdit, error)) error {
	e, err := newEditor(s.current)
	if err != nil {
		return err
	}
	edits, err := step(e)
	if err != nil {
		return err
	}
	if len(edits) == 0 {
		return nil
	}
	next, err := transaction.ApplyEdits(s.current, edits)
	if err != nil {
		return err
	}
	s.current = next
	return nil
}

// edits folds all steps into one edit spanning only the changed region.
func (s *session) edits() []transaction.ByteEdit {
	before, after := s.original, s.current
	shortest := min(len(before), len(after))
	prefix := 0
	for prefix < shortest && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < shortest-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	if prefix == len(before) && prefix == len(after) {
		return nil
	}
	replacement := append([]byte(nil), after[prefix:len(after)-suffix]...)
	return []transaction.ByteEdit{{Start: prefix, End: len(before) - suffix, Replacement: replacement}}
}
