package acceptance

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// The global storage quota measures the volume of the scenario's own data
// directory, so its scenarios use only the percentages no host can miss: 1%
// is reached on any volume holding an operating system, and 100% is never
// reached by a volume with room to run the registry.
func (w *world) registerStorageQuota(sc *godog.ScenarioContext) {
	sc.Step(`^I upload (\d+) bytes to "([^"]*)" and the disk quota refuses it$`, w.uploadRefusedByDiskQuota)
	sc.Step(`^a storage quota (warning|limit) mail arrives with a new identity$`, w.storageQuotaMail)
	sc.Step(`^the storage quota (warning|limit) is retried with the same identity$`, w.storageQuotaRetried)
	sc.Step(`^the registry process restarts over its data$`, func() error { return w.restartReplicationProcess() })
}

func (w *world) uploadRefusedByDiskQuota(size int, path string) error {
	if err := w.send(http.MethodPut, path, strings.Repeat("x", size), func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
		r.Header.Set("Content-Type", "application/octet-stream")
	}); err != nil {
		return err
	}
	if err := w.statusIs(http.StatusRequestEntityTooLarge); err != nil {
		return err
	}
	return w.bodyContains("Datastore disk usage is too high. Contact your Artifactory administrator to add additional storage space or change the disk quota limits.")
}

func (w *world) readStorageQuotaMail(level string) (mail.Header, error) {
	if w.quotaMail == nil {
		return nil, fmt.Errorf("quota receiver is not configured")
	}
	subject, text := "Storage quota warning", "1% storage space reached. Your maximum limit is 100%. It is recommended to add disk space."
	if level == "limit" {
		subject, text = "Storage quota limit reached", "1% Storage Space Reached: Your maximum storage limit has been reached. New artifact deployments are blocked until more disk space is added."
	}
	select {
	case envelope := <-w.quotaMail.messages:
		if !slices.Contains(envelope.recipients, "storage-admin@example.invalid") || slices.Contains(envelope.recipients, "storage-reader@example.invalid") {
			return nil, fmt.Errorf("storage quota mail went to %v, not the administrators", envelope.recipients)
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
			message.Header.Get("Subject") != "[Native quota] "+subject || !strings.Contains(string(content), text) {
			return nil, fmt.Errorf("storage quota mail lacks its identity or 7.x text: %s\n%s", message.Header.Get("Subject"), content)
		}
		return message.Header, nil
	case <-time.After(12 * time.Second):
		return nil, fmt.Errorf("storage quota notification did not reach the loopback receiver")
	}
}

func (w *world) storageQuotaMail(level string) error {
	header, err := w.readStorageQuotaMail(level)
	if err != nil {
		return err
	}
	if header.Get("Message-ID") == w.quotaMailID {
		return fmt.Errorf("a new storage quota occurrence reused the old identity")
	}
	w.quotaMailID = header.Get("Message-ID")
	return nil
}

func (w *world) storageQuotaRetried(level string) error {
	first, err := w.readStorageQuotaMail(level)
	if err != nil {
		return err
	}
	second, err := w.readStorageQuotaMail(level)
	if err != nil {
		return err
	}
	if first.Get("Message-ID") != second.Get("Message-ID") || first.Get("Date") != second.Get("Date") {
		return fmt.Errorf("storage quota retry changed its occurrence identity or date")
	}
	w.quotaMailID = second.Get("Message-ID")
	return nil
}
