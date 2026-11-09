// Package pathmap moves directory paths inside JSON records, the way session
// adapters move a recorded workspace to another machine.
//
// A rewrite touches only JSON strings that mention a mapped directory at path
// boundaries, so sibling paths such as /work/project-other stay unchanged when
// /work/project moves. Every other byte of a record is preserved, and every
// rewrite must be reversible.
package pathmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// Move maps the directory From, and every path inside it, to To.
type Move struct {
	From string
	To   string
}

// Mapping is a set of directory moves applied together. Where two source
// directories nest, the longer one wins.
type Mapping struct {
	moves []Move
}

// ErrIrreversible reports a record that already mentions a target directory,
// so moving it back would not restore the original bytes.
var ErrIrreversible = errors.New("the path rewrite would not be reversible")

// New validates moves and returns their mapping. Every directory must be
// absolute, and no source may be a filesystem root.
func New(moves ...Move) (Mapping, error) {
	for _, move := range moves {
		if !filepath.IsAbs(move.From) || !filepath.IsAbs(move.To) {
			return Mapping{}, fmt.Errorf("cannot move %q to %q: paths must be absolute", move.From, move.To)
		}
		if filepath.Dir(move.From) == move.From {
			return Mapping{}, errors.New("a filesystem root cannot be moved")
		}
	}
	sorted := slices.Clone(moves)
	slices.SortStableFunc(sorted, func(a, b Move) int { return len(b.From) - len(a.From) })
	return Mapping{moves: sorted}, nil
}

// Reverse returns the mapping that moves every target back to its source.
func (m Mapping) Reverse() Mapping {
	reversed := make([]Move, len(m.moves))
	for index, move := range m.moves {
		reversed[index] = Move{From: move.To, To: move.From}
	}
	slices.SortStableFunc(reversed, func(a, b Move) int { return len(b.From) - len(a.From) })
	return Mapping{moves: reversed}
}

// Rewrite rewrites the JSON strings in record and returns it with the number of
// strings changed. With no member names it rewrites the whole record;
// otherwise record must be a JSON object, and only the values of the named
// top-level members change. It fails with ErrIrreversible unless the reverse
// mapping restores record exactly.
func (m Mapping) Rewrite(record []byte, members ...string) ([]byte, int, error) {
	rewritten, changed, err := m.rewrite(record, members)
	if err != nil || changed == 0 {
		return record, 0, err
	}
	restored, _, err := m.Reverse().rewrite(rewritten, members)
	if err != nil {
		return nil, 0, err
	}
	if !bytes.Equal(restored, record) {
		return nil, 0, ErrIrreversible
	}
	return rewritten, changed, nil
}

func (m Mapping) rewrite(record []byte, members []string) ([]byte, int, error) {
	if len(members) == 0 {
		return m.rewriteStrings(record)
	}
	spans, err := memberValues(record, members)
	if err != nil {
		return nil, 0, err
	}
	var output []byte
	copied := 0
	changed := 0
	for _, span := range spans {
		value, count, err := m.rewriteStrings(record[span.start:span.end])
		if err != nil {
			return nil, 0, err
		}
		if count == 0 {
			continue
		}
		output = append(output, record[copied:span.start]...)
		output = append(output, value...)
		copied = span.end
		changed += count
	}
	if changed == 0 {
		return record, 0, nil
	}
	return append(output, record[copied:]...), changed, nil
}

// rewriteStrings rewrites every JSON string token in data.
func (m Mapping) rewriteStrings(data []byte) ([]byte, int, error) {
	var output []byte
	copied := 0
	changed := 0
	for index := 0; index < len(data); index++ {
		if data[index] != '"' {
			continue
		}
		start := index
		end := stringTokenEnd(data, start)
		if end < 0 {
			return nil, 0, errors.New("unterminated JSON string")
		}
		index = end
		var value string
		if err := json.Unmarshal(data[start:end+1], &value); err != nil {
			return nil, 0, err
		}
		replaced := m.String(value)
		if replaced == value {
			continue
		}
		encoded, err := encodeString(replaced)
		if err != nil {
			return nil, 0, err
		}
		output = append(output, data[copied:start]...)
		output = append(output, encoded...)
		copied = end + 1
		changed++
	}
	if changed == 0 {
		return data, 0, nil
	}
	return append(output, data[copied:]...), changed, nil
}

// String replaces each mention of a source directory in value that starts and
// ends at a path boundary.
func (m Mapping) String(value string) string {
	var output strings.Builder
	copied := 0
	for offset := 0; offset < len(value); {
		start, move := m.nextMention(value, offset)
		if start < 0 {
			break
		}
		output.WriteString(value[copied:start])
		output.WriteString(move.To)
		copied = start + len(move.From)
		offset = copied
	}
	if copied == 0 {
		return value
	}
	output.WriteString(value[copied:])
	return output.String()
}

// nextMention returns the earliest boundary-delimited mention of a source
// directory at or after offset, preferring the longest source at one position.
func (m Mapping) nextMention(value string, offset int) (int, Move) {
	earliest := -1
	var found Move
	for _, move := range m.moves {
		start := mention(value, move.From, offset)
		if start >= 0 && (earliest < 0 || start < earliest) {
			earliest, found = start, move
		}
	}
	return earliest, found
}

func mention(value, path string, offset int) int {
	for offset < len(value) {
		found := strings.Index(value[offset:], path)
		if found < 0 {
			return -1
		}
		start := offset + found
		end := start + len(path)
		startsAtBoundary := start == 0 || !isPathNameByte(value[start-1])
		endsAtBoundary := end == len(value) || !isPathNameByte(value[end])
		if startsAtBoundary && endsAtBoundary {
			return start
		}
		offset = start + 1
	}
	return -1
}

// span is the byte range of one JSON value inside a record.
type span struct {
	start int
	end   int
}

// memberValues returns the byte ranges of the values of the named top-level
// members of a JSON object, in record order.
func memberValues(record []byte, names []string) ([]span, error) {
	decoder := json.NewDecoder(bytes.NewReader(record))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("record is not a JSON object")
	}
	var spans []span
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if slices.Contains(names, name) {
			end := int(decoder.InputOffset())
			spans = append(spans, span{start: end - len(value), end: end})
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("record has trailing data")
	}
	return spans, nil
}

// stringTokenEnd returns the index of the quote closing the JSON string that
// starts at start, or -1 when the string is unterminated.
func stringTokenEnd(data []byte, start int) int {
	for index := start + 1; index < len(data); index++ {
		switch data[index] {
		case '\\':
			index++
		case '"':
			return index
		}
	}
	return -1
}

func isPathNameByte(character byte) bool {
	return character == '.' || character == '_' || character == '-' ||
		(character >= '0' && character <= '9') ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z')
}

func encodeString(value string) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}
