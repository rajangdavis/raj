package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The wire format is JSON-RPC 2.0 in HTTP-style frames: headers, a blank line,
// then exactly Content-Length bytes of body.
//
// Content-Length is in bytes, not characters, which is worth stating because it
// is the one place in this protocol where the answer is bytes and getting it
// wrong desynchronises the stream permanently — every subsequent frame is read
// from the middle of the previous one. So the body is read with ReadFull rather
// than by scanning for a delimiter: JSON contains newlines and braces, and
// there is no delimiter to scan for.

// MaxMessage bounds a single frame. A server that announces a gigabyte is
// broken or hostile, and allocating for it before reading a byte is how a
// broken server takes the editor down with it.
const MaxMessage = 32 << 20

// Message is one JSON-RPC message. Requests carry an ID and expect a response;
// notifications have no ID and expect none. A response carries the ID it
// answers and exactly one of Result or Error.
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *ResponseError   `json:"error,omitempty"`
}

// IsResponse reports whether m answers a request rather than making one. A
// message with an ID and no method is a response; one with both is a request
// from the server, which happens for things like configuration queries.
func (m *Message) IsResponse() bool { return m.ID != nil && m.Method == "" }

// ResponseError is a server-side failure. It is data rather than a Go error
// because it belongs to one request and must not abort the connection: a server
// that cannot answer a hover is still able to answer the next completion.
type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("lsp: %s (code %d)", e.Message, e.Code)
}

// ErrTooLarge is returned when a frame announces more than MaxMessage.
var ErrTooLarge = errors.New("lsp: message exceeds the size limit")

// WriteMessage frames and writes one message.
func WriteMessage(w io.Writer, m *Message) error {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ReadMessage reads one framed message.
//
// Headers other than Content-Length are skipped rather than rejected. Content-
// Type appears in the specification and servers have historically sent others;
// refusing an unknown header would make raj fail against a server that is
// merely chattier than expected, which is not a failure worth having.
func ReadMessage(r *bufio.Reader) (*Message, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // the blank line ends the headers
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed header %q", line)
		}
		if !strings.EqualFold(strings.TrimSpace(name), "content-length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("lsp: bad Content-Length %q", value)
		}
		length = n
	}
	if length < 0 {
		return nil, errors.New("lsp: frame has no Content-Length")
	}
	if length > MaxMessage {
		return nil, ErrTooLarge
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("lsp: %w", err)
	}
	return &m, nil
}

// NewRequest builds a request with an integer ID.
func NewRequest(id int, method string, params any) (*Message, error) {
	m := &Message{JSONRPC: "2.0", Method: method}
	raw := json.RawMessage(strconv.Itoa(id))
	m.ID = &raw
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		m.Params = p
	}
	return m, nil
}

// NewNotification builds a notification, which has no ID and no reply.
func NewNotification(method string, params any) (*Message, error) {
	m := &Message{JSONRPC: "2.0", Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		m.Params = p
	}
	return m, nil
}

// RequestID is the integer ID of a message, and whether it had one.
//
// IDs may be strings as well as numbers in JSON-RPC. raj only ever sends
// integers, so a non-integer ID belongs to a request the server originated and
// is matched by its raw form rather than parsed.
func (m *Message) RequestID() (int, bool) {
	if m.ID == nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(*m.ID)))
	if err != nil {
		return 0, false
	}
	return n, true
}
