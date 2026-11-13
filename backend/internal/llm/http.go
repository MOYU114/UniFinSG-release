package llm

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var (
	errStreamDone       = errors.New("llm stream complete")
	errResponseTooLarge = errors.New("llm response exceeded 8MB")
)

type limitedBody struct {
	reader    io.ReadCloser
	remaining int64
}

func newLimitedBody(r io.ReadCloser) io.ReadCloser {
	// Read one byte past the limit so an exactly 8MB body still ends in EOF
	// instead of a spurious "too large" error.
	return &limitedBody{reader: r, remaining: MaxResponseBytes + 1}
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, errResponseTooLarge
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= int64(n)
	if b.remaining == 0 {
		return n, errResponseTooLarge
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.reader.Close() }

// scanSSE dispatches Server-Sent Events. handle receives the event name (which
// may be empty) and the joined data payload.
func scanSSE(r io.Reader, handle func(event, data string) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var event string
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		payload := strings.Join(data, "\n")
		name := event
		event, data = "", nil
		return handle(name, payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func imageDataURL(mime string, data []byte) string {
	if mime == "" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64String(data)
}

func base64String(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func rawOrEmptyObject(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}
