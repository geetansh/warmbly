package arf

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/emersion/go-message"
)

const (
	maxMIMEBytes = 1 << 20
	maxMIMEDepth = 16
	maxMIMEParts = 64
)

// reportParts carry the Feedback-Type and the reported Message-ID; the human notice carries neither.
var reportParts = map[string]bool{
	"message/feedback-report": true,
	"message/rfc822":          true,
	"message/rfc822-headers":  true,
	"text/rfc822-headers":     true,
	"message/global":          true,
	"message/global-headers":  true,
}

// ParseMIME parses a whole raw message by its report parts, for a provider that
// syncs only the rendered body (Microsoft Graph).
func ParseMIME(raw []byte) Report {
	if len(raw) > maxMIMEBytes {
		return Report{}
	}
	ent, err := message.Read(bytes.NewReader(raw))
	if ent == nil || err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return Report{}
	}
	var parts []string
	count, remaining := 0, maxMIMEBytes
	err = ent.Walk(func(path []int, part *message.Entity, _ error) error {
		count++
		if len(path) >= maxMIMEDepth || count > maxMIMEParts {
			return errors.New("feedback MIME traversal limit")
		}
		if part == nil {
			return nil
		}
		mediaType, _, _ := part.Header.ContentType()
		if !reportParts[strings.ToLower(mediaType)] {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(part.Body, int64(remaining+1)))
		if err != nil {
			return err
		}
		if len(body) > remaining {
			return errors.New("feedback MIME decoded size limit")
		}
		remaining -= len(body)
		parts = append(parts, string(body))
		return nil
	})
	if err != nil {
		return Report{}
	}
	return Parse(strings.Join(parts, "\n"))
}
