package acceptance

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/textproto"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cucumber/godog"
)

// This receiver is owned by one native scenario and accepts only loopback
// connections. It deliberately loses the first DATA acknowledgement to prove
// the actual runtime worker retries the durable occurrence rather than minting
// a second warning. It never forwards mail or contacts another server.
type quotaMailReceiver struct {
	listener net.Listener
	messages chan quotaMailEnvelope
	done     chan struct{}
	accepted atomic.Int64
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]bool
}

type quotaMailEnvelope struct {
	body       []byte
	recipients []string
}

func (w *world) registerProjectQuota(sc *godog.ScenarioContext) {
	sc.Step(`^a loopback quota mail receiver loses the first acknowledgement$`, w.startQuotaMail)
	sc.Step(`^I upload (\d+) quota bytes to "([^"]*)"$`, w.uploadQuotaBytes)
	sc.Step(`^the quota warning is retried with the same identity for "([^"]*)"$`, w.quotaMailRetried)
	sc.Step(`^the quota warning survives a process restart with the same identity for "([^"]*)"$`, w.quotaMailRestarted)
	sc.Step(`^a new quota warning arrives for "([^"]*)"$`, w.quotaMailRearmed)
	sc.Step(`^no additional quota mail arrives$`, w.noQuotaMail)
}

func (w *world) startQuotaMail() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	r := &quotaMailReceiver{listener: listener, messages: make(chan quotaMailEnvelope, 16), done: make(chan struct{}), conns: map[net.Conn]bool{}}
	r.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns[conn] = true
			r.mu.Unlock()
			r.wg.Go(func() {
				defer conn.Close()
				defer func() { r.mu.Lock(); delete(r.conns, conn); r.mu.Unlock() }()
				r.serve(conn)
			})
		}
	})
	w.quotaMail = r
	w.closers = append(w.closers, func() {
		close(r.done)
		_ = listener.Close()
		r.mu.Lock()
		for conn := range r.conns {
			_ = conn.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	yaml := "mailServer:\n  enabled: true\n  host: 127.0.0.1\n  port: " + port + "\n  from: quota-sender@example.invalid\n  subjectPrefix: '[Native quota]'\n"
	if err := w.patchYAMLAsAdmin("/artifactory/api/system/configuration", &godog.DocString{Content: yaml}); err != nil {
		return err
	}
	return w.statusIs(http.StatusOK)
}

func (r *quotaMailReceiver) serve(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	tp := textproto.NewConn(conn)
	var recipients []string
	if tp.PrintfLine("220 loopback quota fixture") != nil {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		switch strings.SplitN(line, " ", 2)[0] {
		case "EHLO", "HELO", "MAIL":
			err = tp.PrintfLine("250 accepted")
		case "RCPT":
			recipients = append(recipients, strings.Trim(strings.TrimPrefix(line, "RCPT TO:"), "<>"))
			err = tp.PrintfLine("250 accepted")
		case "DATA":
			if tp.PrintfLine("354 send message") != nil {
				return
			}
			body, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			select {
			case r.messages <- quotaMailEnvelope{body: body, recipients: recipients}:
			case <-r.done:
				return
			}
			if r.accepted.Add(1) == 1 {
				return
			}
			err = tp.PrintfLine("250 accepted")
			if err != nil {
				return
			}
		case "QUIT":
			_ = tp.PrintfLine("221 goodbye")
			return
		default:
			err = tp.PrintfLine("500 unsupported")
		}
		if err != nil {
			return
		}
	}
}

func (w *world) uploadQuotaBytes(size int, path string) error {
	if err := w.send(http.MethodPut, path, strings.Repeat("x", size), func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
		r.Header.Set("Content-Type", "application/octet-stream")
	}); err != nil {
		return err
	}
	return w.statusIs(http.StatusCreated)
}

func (w *world) readQuotaMail(project string) (mail.Header, error) {
	if w.quotaMail == nil {
		return nil, fmt.Errorf("quota receiver is not configured")
	}
	select {
	case envelope := <-w.quotaMail.messages:
		if len(envelope.recipients) != 1 || envelope.recipients[0] != "quota-owner@example.invalid" {
			return nil, fmt.Errorf("quota message was addressed outside the current project administrator")
		}
		message, err := mail.ReadMessage(strings.NewReader(string(envelope.body)))
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
		if err != nil {
			return nil, err
		}
		if message.Header.Get("Message-ID") == "" || message.Header.Get("Date") == "" ||
			!strings.Contains(message.Header.Get("Subject"), project) || !strings.Contains(string(content), "Project "+project+" reached") ||
			!strings.Contains(string(content), "75 bytes") || !strings.Contains(string(content), "100 bytes") {
			return nil, fmt.Errorf("quota message lacks its durable identity or transition facts")
		}
		return message.Header, nil
	case <-time.After(12 * time.Second):
		return nil, fmt.Errorf("quota notification did not reach the loopback receiver")
	}
}

func (w *world) quotaMailRetried(project string) error {
	first, err := w.readQuotaMail(project)
	if err != nil {
		return err
	}
	second, err := w.readQuotaMail(project)
	if err != nil {
		return err
	}
	if first.Get("Message-ID") != second.Get("Message-ID") || first.Get("Date") != second.Get("Date") {
		return fmt.Errorf("quota retry changed its occurrence identity or date")
	}
	w.quotaMailID = second.Get("Message-ID")
	return nil
}

func (w *world) quotaMailRestarted(project string) error {
	first, err := w.readQuotaMail(project)
	if err != nil {
		return err
	}
	if w.server == nil {
		return fmt.Errorf("quota application is not running")
	}
	// Restart exactly this scenario's binary over its existing data directory
	// and sealed key. The receiver survives; no shared database is reopened or
	// reset by the fixture. The first DATA acknowledgement was already lost.
	args, environment := slices.Clone(w.server.Args[1:]), slices.Clone(w.server.Env)
	w.stop()
	w.server = exec.Command(binary, args...)
	w.server.Env, w.server.Stderr = environment, &w.stderr
	if err := w.server.Start(); err != nil {
		return err
	}
	if err := w.waitReady(); err != nil {
		return err
	}
	second, err := w.readQuotaMail(project)
	if err != nil {
		return err
	}
	if first.Get("Message-ID") != second.Get("Message-ID") || first.Get("Date") != second.Get("Date") {
		return fmt.Errorf("process restart changed the durable quota occurrence")
	}
	w.quotaMailID = second.Get("Message-ID")
	return nil
}

func (w *world) quotaMailRearmed(project string) error {
	header, err := w.readQuotaMail(project)
	if err != nil {
		return err
	}
	if header.Get("Message-ID") == w.quotaMailID {
		return fmt.Errorf("rearmed quota warning reused the old occurrence")
	}
	w.quotaMailID = header.Get("Message-ID")
	return nil
}

func (w *world) noQuotaMail() error {
	if w.quotaMail == nil {
		return fmt.Errorf("quota receiver is not configured")
	}
	select {
	case <-w.quotaMail.messages:
		return fmt.Errorf("unexpected additional quota notification")
	case <-time.After(1200 * time.Millisecond):
		return nil
	}
}
