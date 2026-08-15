package sse

import (
	"bufio"
	"bytes"
	"io"
)

// Event represents a single SSE event with its type and data.
type Event struct {
	Type string // event type (empty means "message")
	Data []byte // event data (may span multiple data: lines)
}

// Reader reads SSE events from an io.Reader.
type Reader struct {
	scanner *bufio.Scanner
	err     error
}

// NewReader creates a new SSE event reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		scanner: bufio.NewScanner(r),
	}
}

// Next reads the next SSE event. Returns false when done or on error.
func (r *Reader) Next() bool {
	if r.err != nil {
		return false
	}

	var dataLines [][]byte

	for r.scanner.Scan() {
		line := r.scanner.Bytes()

		// Empty line signals end of event
		if len(line) == 0 {
			if len(dataLines) > 0 {
				return true
			}
			continue
		}

		// Parse field: value
		idx := bytes.IndexByte(line, ':')
		if idx == -1 {
			// Malformed line, skip
			continue
		}

		field := string(line[:idx])
		value := line[idx+1:]

		// Trim leading space from value (SSE spec)
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch field {
		case "data":
			// Accumulate data lines
			dataLines = append(dataLines, append([]byte(nil), value...))
		case "event", "id", "retry":
			// Ignore for now (can extend Event struct if needed)
		}
	}

	// Check for scanner error
	if err := r.scanner.Err(); err != nil {
		r.err = err
		return false
	}

	// End of stream - if we have accumulated data, return it
	if len(dataLines) > 0 {
		return true
	}

	return false
}

// Event returns the current event. Only valid after Next() returns true.
func (r *Reader) Event() Event {
	return Event{
		Type: "",    // Will be populated during Next()
		Data: nil,   // Will be populated during Next()
	}
}

// Err returns the error that stopped iteration, if any.
func (r *Reader) Err() error {
	return r.err
}

// ReadAll reads all SSE events from r and returns them as a slice.
func ReadAll(r io.Reader) ([]Event, error) {
	reader := NewReader(r)
	var events []Event

	var currentType string
	var currentData [][]byte

	for reader.scanner.Scan() {
		line := reader.scanner.Bytes()

		// Empty line signals end of event
		if len(line) == 0 {
			if len(currentData) > 0 {
				// Join data lines with newline
				data := bytes.Join(currentData, []byte("\n"))
				events = append(events, Event{
					Type: currentType,
					Data: data,
				})
				currentType = ""
				currentData = nil
			}
			continue
		}

		// Parse field: value
		idx := bytes.IndexByte(line, ':')
		if idx == -1 {
			continue
		}

		field := string(line[:idx])
		value := line[idx+1:]

		// Trim leading space from value (SSE spec)
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch field {
		case "event":
			currentType = string(value)
		case "data":
			currentData = append(currentData, append([]byte(nil), value...))
		}
	}

	// Flush any remaining event
	if len(currentData) > 0 {
		data := bytes.Join(currentData, []byte("\n"))
		events = append(events, Event{
			Type: currentType,
			Data: data,
		})
	}

	if err := reader.scanner.Err(); err != nil {
		return events, err
	}

	return events, nil
}
