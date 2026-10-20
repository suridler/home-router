package xrayconfig

import (
	"bytes"
	"encoding/json"
	"errors"

	"home-router/xray-core-helper/internal/transaction"
)

// editor builds narrow byte edits against one parsed snapshot of the config.
type editor struct {
	source []byte
	syntax *parsedSource
}

func newEditor(source []byte) (*editor, error) {
	syntax, err := parseSource(source)
	if err != nil {
		return nil, err
	}
	return &editor{source: source, syntax: syntax}, nil
}

func renderJSON(value any, indent string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent(indent, "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

func (e *editor) lineIndent(position int) string {
	start := bytes.LastIndexByte(e.source[:position], '\n') + 1
	end := start
	for end < len(e.source) && (e.source[end] == ' ' || e.source[end] == '\t') {
		end++
	}
	return string(e.source[start:end])
}

func onlySpace(value []byte) bool {
	for _, character := range value {
		if !isJSONSpace(character) {
			return false
		}
	}
	return true
}

func (e *editor) replaceNode(node *jsonNode, value any) (transaction.ByteEdit, error) {
	rendered, err := renderJSON(value, e.lineIndent(node.start))
	if err != nil {
		return transaction.ByteEdit{}, err
	}
	return transaction.ByteEdit{Start: node.start, End: node.end, Replacement: rendered}, nil
}

func (e *editor) appendArrayItem(array *jsonNode, value any) (transaction.ByteEdit, error) {
	if array == nil || array.array == nil {
		return transaction.ByteEdit{}, errors.New("target is not an array")
	}
	if count := len(array.array); count > 0 {
		last := array.array[count-1]
		indent := e.lineIndent(last.start)
		rendered, err := renderJSON(value, indent)
		if err != nil {
			return transaction.ByteEdit{}, err
		}
		return transaction.ByteEdit{Start: last.end, End: last.end, Replacement: []byte(",\n" + indent + string(rendered))}, nil
	}
	return e.insertIntoEmpty(array, value, "")
}

func (e *editor) appendObjectMember(object *jsonNode, key string, value any) (transaction.ByteEdit, error) {
	if object == nil || object.object == nil {
		return transaction.ByteEdit{}, errors.New("target is not an object")
	}
	quotedKey, err := renderJSON(key, "")
	if err != nil {
		return transaction.ByteEdit{}, err
	}
	prefix := string(quotedKey) + ": "
	if count := len(object.members); count > 0 {
		last := object.members[count-1]
		indent := e.lineIndent(last.keyStart)
		rendered, err := renderJSON(value, indent)
		if err != nil {
			return transaction.ByteEdit{}, err
		}
		return transaction.ByteEdit{
			Start: last.value.end, End: last.value.end,
			Replacement: []byte(",\n" + indent + prefix + string(rendered)),
		}, nil
	}
	return e.insertIntoEmpty(object, value, prefix)
}

func (e *editor) insertIntoEmpty(container *jsonNode, value any, prefix string) (transaction.ByteEdit, error) {
	closing := e.lineIndent(container.start)
	indent := closing + "  "
	rendered, err := renderJSON(value, indent)
	if err != nil {
		return transaction.ByteEdit{}, err
	}
	text := []byte("\n" + indent + prefix + string(rendered) + "\n" + closing)
	inner := e.source[container.start+1 : container.end-1]
	if onlySpace(inner) {
		return transaction.ByteEdit{Start: container.start + 1, End: container.end - 1, Replacement: text}, nil
	}
	// Keep comments that live inside an otherwise empty container.
	return transaction.ByteEdit{Start: container.end - 1, End: container.end - 1, Replacement: text}, nil
}

type span struct{ start, end int }

func (e *editor) lineStart(position int) (int, bool) {
	for ; position > 0; position-- {
		switch e.source[position-1] {
		case '\n':
			return position, true
		case ' ', '\t':
		default:
			return position, false
		}
	}
	return position, false
}

// afterComma returns the offset past an optional comma following position.
func (e *editor) afterComma(position int) int {
	clean := e.syntax.clean
	next := position
	for next < len(clean) && (clean[next] == ' ' || clean[next] == '\t') {
		next++
	}
	if next < len(clean) && clean[next] == ',' {
		return next + 1
	}
	return position
}

// deleteSpans removes the flagged siblings while keeping separators valid.
func (e *editor) deleteSpans(spans []span, remove []bool) []transaction.ByteEdit {
	clean := e.syntax.clean
	edits := make([]transaction.ByteEdit, 0)
	for i := 0; i < len(spans); {
		if !remove[i] {
			i++
			continue
		}
		j := i
		for j+1 < len(spans) && remove[j+1] {
			j++
		}
		first, last := spans[i], spans[j]
		switch {
		case j+1 < len(spans):
			start, atLine := e.lineStart(first.start)
			end := e.afterComma(last.end)
			if atLine {
				tail := end
				for tail < len(clean) && (clean[tail] == ' ' || clean[tail] == '\t' || clean[tail] == '\r') {
					tail++
				}
				if tail < len(clean) && clean[tail] == '\n' {
					end = tail + 1
				}
			}
			edits = append(edits, transaction.ByteEdit{Start: start, End: end})
		case i > 0:
			from := spans[i-1].end
			for position := spans[i-1].end; position < first.start; position++ {
				if clean[position] == ',' {
					from = position
					break
				}
			}
			edits = append(edits, transaction.ByteEdit{Start: from, End: e.afterComma(last.end)})
		default:
			edits = append(edits, transaction.ByteEdit{Start: first.start, End: e.afterComma(last.end)})
		}
		i = j + 1
	}
	return edits
}

func (e *editor) deleteArrayItem(array *jsonNode, index int) ([]transaction.ByteEdit, error) {
	if array == nil || array.array == nil || index < 0 || index >= len(array.array) {
		return nil, errors.New("array item does not exist")
	}
	spans := make([]span, len(array.array))
	for i, item := range array.array {
		spans[i] = span{item.start, item.end}
	}
	remove := make([]bool, len(spans))
	remove[index] = true
	return e.deleteSpans(spans, remove), nil
}

// deleteMembers removes the named members of an object in one non-overlapping pass.
func (e *editor) deleteMembers(object *jsonNode, names map[string]bool) []transaction.ByteEdit {
	spans := make([]span, len(object.members))
	remove := make([]bool, len(spans))
	for i, member := range object.members {
		spans[i] = span{member.keyStart, member.value.end}
		remove[i] = names[member.key]
	}
	return e.deleteSpans(spans, remove)
}

// insertArrayItemAt inserts before the element at index; index == len appends.
func (e *editor) insertArrayItemAt(array *jsonNode, index int, value any) (transaction.ByteEdit, error) {
	if array == nil || array.array == nil || index < 0 || index > len(array.array) {
		return transaction.ByteEdit{}, errors.New("array position is invalid")
	}
	if index == len(array.array) {
		return e.appendArrayItem(array, value)
	}
	target := array.array[index]
	indent := e.lineIndent(target.start)
	rendered, err := renderJSON(value, indent)
	if err != nil {
		return transaction.ByteEdit{}, err
	}
	if start, atLine := e.lineStart(target.start); atLine {
		return transaction.ByteEdit{Start: start, End: start, Replacement: []byte(indent + string(rendered) + ",\n")}, nil
	}
	return transaction.ByteEdit{Start: target.start, End: target.start, Replacement: []byte(string(rendered) + ", ")}, nil
}
