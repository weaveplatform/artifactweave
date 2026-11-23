package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// The mail steps deliver through Mailpit, a pinned real SMTP server with
// AUTH, so a passing scenario shows the configured sender, prefix, URL and
// credentials reached an independent implementation. The scenario's own
// password differs from Mailpit's only where a step says so.
func (w *world) registerMail(sc *godog.ScenarioContext) {
	sc.Step(`^an SMTP server receives mail$`, w.startSMTP)
	sc.Step(`^I PATCH the SMTP server's mail settings as admin with password "([^"]*)"$`, w.patchSMTPSettings)
	sc.Step(`^I send a test mail to "([^"]*)" from the server store$`, w.sendTestMail)
	sc.Step(`^sending a test mail to "([^"]*)" from the server store fails with "([^"]*)"$`, w.testMailFails)
	sc.Step(`^the SMTP server receives a message to "([^"]*)" with subject "([^"]*)" containing "([^"]*)"$`, w.smtpReceived)
	sc.Step(`^the SMTP server has received (\d+) messages?$`, w.smtpCount)
	sc.Step(`^the registry restarts on the same data$`, w.restart)
}

// restart stops the server and starts the same command, so the data
// directory, port, master key and environment are unchanged and only
// in-memory state is lost.
func (w *world) restart() error {
	if w.server == nil {
		return errors.New("the registry is not running")
	}
	args, env := w.server.Args, w.server.Env
	w.stop()
	w.server = exec.Command(args[0], args[1:]...)
	w.server.Env, w.server.Stderr = env, &w.stderr
	if err := w.server.Start(); err != nil {
		return err
	}
	return w.waitReady()
}

func (w *world) startSMTP() error {
	ctx := context.Background()
	if err := containertest.CheckLinux(ctx); err != nil {
		w.t.Logf("skipping: %v", err)
		return godog.ErrSkip
	}
	ep, stop, err := containertest.RunMailpit(ctx)
	if err != nil {
		return err
	}
	w.closers = append(w.closers, stop)
	w.smtp = &ep
	return nil
}

// patchSMTPSettings configures every documented mailServer field; the
// password is the fixture's when it is "fixture".
func (w *world) patchSMTPSettings(password string) error {
	if w.smtp == nil {
		return errors.New("no SMTP server was started")
	}
	if password == "fixture" {
		password = w.smtp.Password
	}
	yaml := fmt.Sprintf(`mailServer:
  enabled: true
  host: %s
  port: %d
  username: %s
  password: %s
  from: registry-sender@example.invalid
  subjectPrefix: '[CFG-07]'
  artifactoryUrl: https://registry.invalid/artifactory/
  tls: false
  ssl: false
`, w.smtp.Host, w.smtp.SMTPPort, w.smtp.Username, strconv.Quote(password))
	return w.patchYAMLAsAdmin("/artifactory/api/system/configuration", &godog.DocString{Content: yaml})
}

func (w *world) runMailTest(recipient string) error {
	cmd := exec.Command(binary, "mail", "test", recipient, "--data-dir", w.dataDir)
	cmd.Env = append(os.Environ(), "ARTIFACTWEAVE_MASTER_KEY="+testMasterKey)
	if coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
	}
	body, err := cmd.CombinedOutput()
	w.body = body
	if err != nil {
		return fmt.Errorf("mail test: %w", err)
	}
	return nil
}

func (w *world) sendTestMail(recipient string) error {
	if err := w.runMailTest(recipient); err != nil {
		return fmt.Errorf("%w: %s", err, w.body)
	}
	return w.outputContains("A test message has been sent successfully to '" + recipient + "'")
}

func (w *world) testMailFails(recipient, want string) error {
	if err := w.runMailTest(recipient); err == nil {
		return fmt.Errorf("mail test succeeded: %s", w.body)
	}
	if w.smtp != nil && strings.Contains(string(w.body), w.smtp.Password) {
		return errors.New("mail test output exposed the mail password")
	}
	return w.outputContains(want)
}

func (w *world) smtpReceived(recipient, subject, text string) error {
	if w.smtp == nil {
		return errors.New("no SMTP server was started")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		messages, err := w.smtp.Messages(context.Background())
		if err != nil {
			return err
		}
		for _, m := range messages {
			if m.Subject == subject && slices.Contains(m.Recipients, recipient) {
				if m.From != "registry-sender@example.invalid" || !strings.Contains(m.Text, text) {
					return fmt.Errorf("message %q from %q lacks %q:\n%s", subject, m.From, text, m.Text)
				}
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no message %q to %s arrived; %d other messages", subject, recipient, len(messages))
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (w *world) smtpCount(want int) error {
	messages, err := w.smtp.Messages(context.Background())
	if err != nil {
		return err
	}
	if len(messages) != want {
		return fmt.Errorf("SMTP server received %d messages, want %d", len(messages), want)
	}
	return nil
}
