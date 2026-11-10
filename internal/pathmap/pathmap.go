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
	// reversed are the moves of the reverse mapping, kept so that checking
	// reversibility sorts nothing.
	reversed []Move
	// directories are the sources and targets as bytes, and plain reports
	// whether every one is plain ASCII, so that records without a mention are
	// recognized cheaply.
	directories [][]byte
	plain       bool
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
	reversed := make([]Move, len(moves))
	for index, move := range moves {
		reversed[index] = Move{From: move.To, To: move.From}
	}
	byLongestSource := func(a, b Move) int { return len(b.From) - len(a.From) }
	slices.SortStableFunc(sorted, byLongestSource)
	slices.SortStableFunc(reversed, byLongestSource)
	mapping := Mapping{moves: sorted, reversed: reversed, plain: true}
	for _, move := range moves {
		for _, directory := range []string{move.From, move.To} {
			mapping.directories = append(mapping.directories, []byte(directory))
			mapping.plain = mapping.plain && isPlainASCII(directory)
		}
	}
	return mapping, nil
}

// IsEmpty reports whether the mapping moves no directory.
func (m Mapping) IsEmpty() bool {
	return len(m.moves) == 0
}

// mayMention reports whether record can mention a directory of the mapping or
// of its reverse, which a reversible rewrite must not meet. It skips the
// decoding of records that cannot, which is most of a large session: a
// directory of plain ASCII with no character that JSON escapes appears in any
// encoding of a string as the same bytes, unless the encoder escapes such a
// character, as \/ or \u0041.
func (m Mapping) mayMention(record []byte) bool {
	if !m.plain || escapesPrintableASCII(record) {
		return true
	}
	for _, directory := range m.directories {
		if bytes.Contains(record, directory) {
			return true
		}
	}
	return false
}

// escapesPrintableASCII reports whether record may hold an escaped printable
// ASCII character. Control characters, such as terminal escapes, are written
// as \u0000 to \u001f and do not count.
func escapesPrintableASCII(record []byte) bool {
	if bytes.Contains(record, []byte(`\/`)) {
		return true
	}
	for offset := 0; ; {
		found := bytes.Index(record[offset:], []byte(`\u00`))
		if found < 0 || offset+found+4 >= len(record) {
			return false
		}
		if digit := record[offset+found+4]; digit >= '2' && digit <= '7' {
			return true
		}
		offset += found + 4
	}
}

// isPlainASCII reports whether value holds only printable ASCII that JSON
// never escapes.
func isPlainASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e || value[index] == '"' || value[index] == '\\' {
			return false
		}
	}
	return true
}

// Reverse returns the mapping that moves every target back to its source.
func (m Mapping) Reverse() Mapping {
	return Mapping{moves: m.reversed, reversed: m.moves, directories: m.directories, plain: m.plain}
}

// Rewrite moves every mention in the JSON strings of record and fails with
// ErrIrreversible unless moving back would restore record exactly: every
// moved string must move back and re-encode to its original bytes, and no
// other string may mention a target directory. A string, or a whole record,
// that cannot mention a directory of the mapping is not decoded, so callers
// validate records themselves.
func (m Mapping) Rewrite(record []byte) ([]byte, error) {
	if !m.mayMention(record) {
		return record, nil
	}
	reverse := m.Reverse()
	var output []byte
	copied := 0
	for index := 0; index < len(record); index++ {
		if record[index] != '"' {
			continue
		}
		start := index
		end := stringTokenEnd(record, start)
		if end < 0 {
			return nil, errors.New("unterminated JSON string")
		}
		index = end
		token := record[start : end+1]
		if !m.mayMention(token) {
			continue
		}
		var value string
		if err := json.Unmarshal(token, &value); err != nil {
			return nil, err
		}
		moved := m.Path(value)
		if moved == value {
			if reverse.Path(value) != value {
				return nil, ErrIrreversible
			}
			continue
		}
		original, err := encodeString(value)
		if err != nil {
			return nil, err
		}
		if reverse.Path(moved) != value || !bytes.Equal(original, token) {
			return nil, ErrIrreversible
		}
		encoded, err := encodeString(moved)
		if err != nil {
			return nil, err
		}
		if output == nil {
			output = make([]byte, 0, len(record)+len(record)/8)
		}
		output = append(output, record[copied:start]...)
		output = append(output, encoded...)
		copied = end + 1
	}
	if output == nil {
		return record, nil
	}
	return append(output, record[copied:]...), nil
}

// RewriteLines rewrites every record of JSON Lines data, as Rewrite does, into
// one buffer. Errors name the line.
func (m Mapping) RewriteLines(data []byte) ([]byte, error) {
	output := make([]byte, 0, len(data)+len(data)/16)
	number := 0
	for line := range bytes.Lines(data) {
		number++
		content := bytes.TrimSuffix(line, []byte{'\n'})
		rewritten, err := m.Rewrite(content)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", number, err)
		}
		output = append(output, rewritten...)
		output = append(output, line[len(content):]...)
	}
	return output, nil
}

// RewriteText moves every mention in plain text, such as a saved tool output,
// and fails with ErrIrreversible unless the reverse mapping restores text
// exactly.
func (m Mapping) RewriteText(text []byte) ([]byte, error) {
	rewritten := []byte(m.Path(string(text)))
	if !bytes.Equal([]byte(m.Reverse().Path(string(rewritten))), text) {
		return nil, ErrIrreversible
	}
	return rewritten, nil
}

// Path replaces each mention of a source directory in value that starts and
// ends at a path boundary.
func (m Mapping) Path(value string) string {
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

// Within returns the path that path has once the directory from, which must
// contain it, moves to to.
func Within(path, from, to string) (string, error) {
	relative, err := filepath.Rel(from, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !filepath.IsAbs(from) {
		return "", fmt.Errorf("%s is not inside %s", path, from)
	}
	return filepath.Join(to, relative), nil
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
