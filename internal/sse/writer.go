package sse

import (
	"bytes"
	"io"
)

// Writer writes SSE events to an io.Writer.
type Writer struct {
	w io.Writer
}

// NewWriter creates a new SSE event writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteEvent writes a single SSE event.
// If eventType is empty, no "event:" line is written (defaults to "message").
func (w *Writer) WriteEvent(eventType string, data []byte) error {
	var buf bytes.Buffer

	// Write event type if specified
	if eventType != "" {
		buf.WriteString("event: ")
		buf.WriteString(eventType)
		buf.WriteByte('\n')
	}

	// Write data (split multi-line data into multiple data: lines per SSE spec)
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		buf.WriteString("data: ")
		buf.Write(line)
		buf.WriteByte('\n')
	}

	// Empty line to signal end of event
	buf.WriteByte('\n')

	_, err := w.w.Write(buf.Bytes())
	return err
}

// WriteRaw writes raw bytes directly to the underlying writer (no SSE formatting).
func (w *Writer) WriteRaw(data []byte) (int, error) {
	return w.w.Write(data)
}
