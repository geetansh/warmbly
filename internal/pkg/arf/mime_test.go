package arf

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// rawReport builds a whole RFC 5965 message as a mailbox's raw MIME endpoint returns it.
func rawReport(feedbackType, returnedEncoding, returned string) []byte {
	if returnedEncoding == "base64" {
		returned = base64.StdEncoding.EncodeToString([]byte(returned))
	}
	return []byte(strings.ReplaceAll(`From: staff@hotmail.com
To: box@outlook.test
Subject: complaint about message from 203.0.113.9
Message-ID: <report-envelope@hotmail.com>
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="b1"

--b1
Content-Type: text/plain; charset=us-ascii

This is an email abuse report for an email message received from IP 203.0.113.9.

--b1
Content-Type: message/feedback-report

Feedback-Type: `+feedbackType+`
User-Agent: Hotmail FBL
Version: 1
Original-Rcpt-To: <recipient@hotmail.com>

--b1
Content-Type: message/rfc822
Content-Transfer-Encoding: `+returnedEncoding+`

`+returned+`
--b1--
`, "\n", "\r\n"))
}

const returnedSend = "From: sender@ourdomain.com\r\nTo: recipient@hotmail.com\r\nSubject: Quick question\r\nMessage-ID: <the-original-send@ourdomain.com>\r\n\r\nHi there\r\n"

func TestParseMIMEReadsTheReportParts(t *testing.T) {
	for _, enc := range []string{"7bit", "base64"} {
		t.Run(enc, func(t *testing.T) {
			r := ParseMIME(rawReport("abuse", enc, returnedSend))
			if !r.IsComplaint {
				t.Fatal("Feedback-Type: abuse did not read as a complaint")
			}
			// The report's own Message-ID sits in the top-level headers, which are not a report part.
			if r.OriginalMessageID != "the-original-send@ourdomain.com" {
				t.Errorf("OriginalMessageID = %q, want the reported send", r.OriginalMessageID)
			}
			if r.ComplainedRecipient != "recipient@hotmail.com" || r.UserAgent != "Hotmail FBL" {
				t.Errorf("recipient %q, user agent %q", r.ComplainedRecipient, r.UserAgent)
			}
		})
	}
}

func TestParseMIMEOnlyCountsAbuseReports(t *testing.T) {
	if r := ParseMIME(rawReport("not-spam", "7bit", returnedSend)); r.IsComplaint {
		t.Error("a not-spam report read as a complaint")
	}
	plain := []byte("From: ada@acme.com\r\nMessage-ID: <x@acme.com>\r\nContent-Type: text/plain\r\n\r\nFeedback-Type: abuse\r\n")
	if r := ParseMIME(plain); r.IsComplaint || r.OriginalMessageID != "" {
		t.Errorf("a plain message parsed as a report: %+v", r)
	}
	if r := ParseMIME([]byte("not a message")); r.IsComplaint {
		t.Error("garbage parsed as a report")
	}
}

func TestParseMIMEBoundsTraversal(t *testing.T) {
	wrap := func(raw []byte, boundary string) []byte {
		return []byte("Content-Type: multipart/mixed; boundary=" + boundary + "\r\n\r\n--" + boundary + "\r\n" + string(raw) + "\r\n--" + boundary + "--\r\n")
	}
	valid := rawReport("abuse", "7bit", returnedSend)
	if got := ParseMIME(wrap(valid, "outer")); !got.IsComplaint {
		t.Fatal("ordinary nested report rejected")
	}
	t.Run("depth", func(t *testing.T) {
		raw := valid
		for depth := range maxMIMEDepth {
			raw = wrap(raw, fmt.Sprintf("outer%d", depth))
		}
		if got := ParseMIME(raw); got != (Report{}) {
			t.Fatalf("deep report accepted: %+v", got)
		}
	})
	t.Run("part count", func(t *testing.T) {
		var raw strings.Builder
		raw.WriteString("Content-Type: multipart/mixed; boundary=outer\r\n\r\n")
		for range maxMIMEParts {
			raw.WriteString("--outer\r\nContent-Type: text/plain\r\n\r\nnotice\r\n")
		}
		raw.WriteString("--outer\r\n" + string(valid) + "\r\n--outer--\r\n")
		if got := ParseMIME([]byte(raw.String())); got != (Report{}) {
			t.Fatalf("over-parted report accepted: %+v", got)
		}
	})
	t.Run("raw bytes", func(t *testing.T) {
		raw := append(append([]byte{}, valid...), []byte(strings.Repeat("x", maxMIMEBytes))...)
		if got := ParseMIME(raw); got != (Report{}) {
			t.Fatalf("oversized report accepted: %+v", got)
		}
	})
	t.Run("no partial report on malformed trailing part", func(t *testing.T) {
		raw := strings.Replace(string(valid), "--b1--", "--b1\r\nnot a header\r\n\r\n--b1--", 1)
		if got := ParseMIME([]byte(raw)); got != (Report{}) {
			t.Fatalf("partial malformed report accepted: %+v", got)
		}
	})
}
