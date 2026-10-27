package npm

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// maxFields bounds the JSON of a publish body held in memory: everything
// but the attachments' data, which streams. npm sends a few kilobytes of
// fields for one version; without a bound, a body within
// npm.max_publish_size could still make the server hold a gigabyte of
// description.
var maxFields = 64 << 20

// publishBody is a publish request's body as read: its fields as sent,
// with the tarball attachment's data stored rather than held.
type publishBody struct {
	// fields are the top-level fields but _attachments.
	fields map[string]json.RawMessage
	// hasAttachments is whether the body has _attachments. A PUT without them is
	// a packument update, as npm deprecate sends (decision 0034).
	hasAttachments bool
	// tarballs counts the attachments whose names end in .tgz.
	tarballs int
	// hasStoredTarball is whether the tarball attachment had data, and info what
	// storing it gave.
	hasStoredTarball bool
	info             blob.Info
	// length is the tarball attachment's declared length, if it has one.
	length json.RawMessage
}

// readPublish reads a publish body from r, whatever order its fields come
// in. The data of the attachment whose name ends in .tgz is decoded from
// base64 into upload as it arrives; the data of any other attachment, such
// as a provenance bundle, is read past and dropped. upload reads its
// reader to the end. Every error is an *apierror.Error or a read error of
// r, so a caller answers it as it is.
func readPublish(r io.Reader, upload func(io.Reader) (blob.Info, error)) (publishBody, error) {
	s := &scanner{r: bufio.NewReaderSize(r, 64<<10)}
	b := publishBody{fields: map[string]json.RawMessage{}}
	err := s.object(func(key string) error {
		if key != "_attachments" {
			v, err := s.value()
			b.fields[key] = v
			return err
		}
		b.hasAttachments = true
		return s.object(func(name string) error {
			tarball := strings.HasSuffix(name, ".tgz")
			if tarball {
				b.tarballs++
				// Refused before a second tarball is read in vain.
				if b.tarballs > 1 {
					return badRequest("exactly one attachment must be a .tgz tarball")
				}
			}
			return s.object(func(field string) error {
				switch {
				case field == "data" && tarball:
					return b.store(s, upload)
				case field == "data":
					return s.skipString()
				}
				v, err := s.value()
				if tarball && field == "length" {
					b.length = v
				}
				return err
			})
		})
	})
	if err != nil {
		return publishBody{}, err
	}
	return b, s.end()
}

// store uploads the tarball attachment's data, at the scanner's reader.
func (b *publishBody) store(s *scanner, upload func(io.Reader) (blob.Info, error)) error {
	if b.hasStoredTarball {
		return badRequest("the tarball attachment has two data fields")
	}
	data, err := s.string()
	if err != nil {
		return err
	}
	b.info, err = upload(base64Data{base64.NewDecoder(base64.StdEncoding, data)})
	if err != nil {
		return err
	}
	b.hasStoredTarball = true
	// A decoder may stop at the padding with the closing quote unread.
	_, err = io.Copy(io.Discard, data)
	return err //nolint:wrapcheck // the scanner's own errors
}

// base64Data answers data that is not base64 as the client's fault, and
// fills each read. The decoder returns at most 768 bytes a call and the
// blob stores write what each read returns, so a gigabyte tarball would
// otherwise take over a million writes.
type base64Data struct{ r io.Reader }

func (d base64Data) Read(p []byte) (int, error) {
	n, err := io.ReadFull(d.r, p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	var corrupt base64.CorruptInputError
	if errors.As(err, &corrupt) {
		return n, badRequest("the tarball attachment's data is not base64")
	}
	return n, err //nolint:wrapcheck // io.Reader contract
}

// limited answers a body over http.MaxBytesReader's limit with 413.
type limited struct{ r io.Reader }

func (l limited) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	var over *http.MaxBytesError
	if errors.As(err, &over) {
		return n, tooLarge(over.Limit)
	}
	return n, err //nolint:wrapcheck // io.Reader contract
}

func tooLarge(limit int64) error {
	return apierror.New(http.StatusRequestEntityTooLarge,
		fmt.Sprintf("the publish request is larger than the %d bytes this registry accepts", limit))
}

func badRequest(msg string) error { return apierror.New(http.StatusBadRequest, msg) }

func malformed(msg string) error { return badRequest("malformed publish body: " + msg) }

// scanner reads the JSON of a publish body. It holds the values it is
// asked for, up to maxFields bytes in all, and streams the strings it is
// asked to.
type scanner struct {
	r    *bufio.Reader
	held int
}

// eof answers the body's end inside its JSON as a malformed body; other
// read errors, such as the size limit's, pass as they are.
func (s *scanner) eof(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return malformed("it ends early")
	}
	return err
}

func space(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// peek skips white space and returns the next byte, unread.
func (s *scanner) peek() (byte, error) {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return 0, s.eof(err)
		}
		if !space(c) {
			_ = s.r.UnreadByte()
			return c, nil
		}
	}
}

func (s *scanner) expect(want byte) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c != want {
		return malformed(fmt.Sprintf("found %q where %q belongs", c, want))
	}
	_, _ = s.r.ReadByte()
	return nil
}

// object reads an object, calling field with each key once the reader is
// at the key's value, which field reads.
func (s *scanner) object(field func(key string) error) error {
	if err := s.expect('{'); err != nil {
		return err
	}
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c == '}' {
		_, _ = s.r.ReadByte()
		return nil
	}
	for {
		raw, err := s.value()
		if err != nil {
			return err
		}
		var key string
		if json.Unmarshal(raw, &key) != nil {
			return malformed("an object key is not a string")
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := field(key); err != nil {
			return err
		}
		c, err := s.peek()
		if err != nil {
			return err
		}
		_, _ = s.r.ReadByte()
		switch c {
		case ',':
		case '}':
			return nil
		default:
			return malformed(fmt.Sprintf("found %q after an object's value", c))
		}
	}
}

// value reads one JSON value and returns it as sent.
func (s *scanner) value() (json.RawMessage, error) {
	if _, err := s.peek(); err != nil {
		return nil, err
	}
	var buf []byte
	depth := 0
	for {
		c, err := s.hold(&buf)
		if err != nil {
			return nil, err
		}
		switch c {
		case '"':
			if err := s.restOfString(&buf); err != nil {
				return nil, err
			}
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
		if depth < 0 {
			return nil, malformed(fmt.Sprintf("found %q where a value belongs", c))
		}
		if depth == 0 && (c == '"' || c == '}' || c == ']' || s.scalarEnds()) {
			break
		}
	}
	if !json.Valid(buf) {
		return nil, malformed("a value is not JSON")
	}
	return buf, nil
}

// hold reads a byte into buf, counting it against maxFields.
func (s *scanner) hold(buf *[]byte) (byte, error) {
	c, err := s.r.ReadByte()
	if err != nil {
		return 0, s.eof(err)
	}
	if s.held++; s.held > maxFields {
		return 0, apierror.New(http.StatusRequestEntityTooLarge, fmt.Sprintf(
			"the publish body holds more than %d bytes besides its attachments' data", maxFields))
	}
	*buf = append(*buf, c)
	return c, nil
}

// restOfString holds a string's bytes up to its closing quote.
func (s *scanner) restOfString(buf *[]byte) error {
	for {
		c, err := s.hold(buf)
		if err != nil {
			return err
		}
		switch c {
		case '\\':
			if _, err := s.hold(buf); err != nil {
				return err
			}
		case '"':
			return nil
		}
	}
}

// scalarEnds reports whether the byte after a number or literal ends it.
func (s *scanner) scalarEnds() bool {
	b, err := s.r.Peek(1)
	return err != nil || space(b[0]) || strings.IndexByte(",:}]", b[0]) >= 0
}

// string opens the string value at the reader, which streams its content.
func (s *scanner) string() (*streamed, error) {
	if err := s.expect('"'); err != nil {
		return nil, err
	}
	return &streamed{s: s}, nil
}

// skipString reads past the string value at the reader.
func (s *scanner) skipString() error {
	str, err := s.string()
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, str)
	return err //nolint:wrapcheck // the scanner's own errors
}

// end reads past the white space after the body's object, which must end
// the body.
func (s *scanner) end() error {
	for {
		c, err := s.r.ReadByte()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err //nolint:wrapcheck // the body's read error, such as the size limit's
		case !space(c):
			return malformed("it continues after its object")
		}
	}
}

// streamed reads a JSON string's content, unescaped, from after its
// opening quote to its closing one, without holding it: the base64 of a
// tarball is most of a publish body.
type streamed struct {
	s *scanner
	// pend is an escape's bytes not yet returned.
	pend   []byte
	isDone bool
}

func (j *streamed) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(j.pend) > 0 {
			c := copy(p[n:], j.pend)
			n, j.pend = n+c, j.pend[c:]
			continue
		}
		if j.isDone {
			break
		}
		if _, err := j.s.r.Peek(1); err != nil {
			return n, j.s.eof(err)
		}
		b, _ := j.s.r.Peek(j.s.r.Buffered())
		run := 0
		for run < len(b) && run < len(p)-n && b[run] != '"' && b[run] != '\\' && b[run] >= 0x20 {
			run++
		}
		if run > 0 {
			copy(p[n:], b[:run])
			n += run
			_, _ = j.s.r.Discard(run)
			continue
		}
		switch b[0] {
		case '"':
			_, _ = j.s.r.Discard(1)
			j.isDone = true
		case '\\':
			if err := j.escape(); err != nil {
				return n, err
			}
		default:
			return n, malformed("a string holds a control character")
		}
	}
	if n == 0 && j.isDone {
		return 0, io.EOF
	}
	return n, nil
}

// escapes are JSON's one-letter escapes.
var escapes = map[byte]byte{
	'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// escape reads the escape at the reader into pend.
func (j *streamed) escape() error {
	var e [2]byte
	if _, err := io.ReadFull(j.s.r, e[:]); err != nil {
		return j.s.eof(err)
	}
	if c, ok := escapes[e[1]]; ok {
		j.pend = append(j.pend[:0], c)
		return nil
	}
	if e[1] != 'u' {
		return malformed(fmt.Sprintf("a string holds the unknown escape \\%c", e[1]))
	}
	var hex [4]byte
	if _, err := io.ReadFull(j.s.r, hex[:]); err != nil {
		return j.s.eof(err)
	}
	v, err := strconv.ParseUint(string(hex[:]), 16, 16)
	if err != nil {
		return malformed("a string holds a malformed \\u escape")
	}
	j.pend = utf8.AppendRune(j.pend[:0], rune(v))
	return nil
}
