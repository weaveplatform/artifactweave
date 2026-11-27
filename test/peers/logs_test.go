package peers

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitSanitizedLogPublication(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "writable", mode: 0o600},
		{name: "readonly", mode: 0o400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			private, exports := t.TempDir(), t.TempDir()
			t.Setenv(ProcessLogDirEnv, exports)
			canary := rand.Text()
			raw := []byte("before\n" + canary + "\nafter\n")
			clean := bytes.ReplaceAll(raw, []byte(canary), []byte("[REDACTED]"))
			sanitize := func(data []byte) ([]byte, error) {
				return bytes.ReplaceAll(data, []byte(canary), []byte("[REDACTED]")), nil
			}
			c := &Cluster{t: t, sanitizeLogs: sanitize}
			in := &Instance{Name: "peer1", work: private, cluster: c}
			f, err := in.openLog()
			if err != nil {
				t.Fatal("could not open private fixture log")
			}
			if _, err := f.Write(raw); err != nil {
				t.Fatal("could not write private fixture log")
			}
			if err := f.Close(); err != nil {
				t.Fatal("could not close private fixture log")
			}
			if filepath.Dir(in.logPath) != private {
				t.Fatal("raw fixture log entered the publication directory")
			}
			t.Cleanup(func() {
				if err := os.Chmod(in.logPath, 0o600); err != nil {
					t.Error("could not restore private fixture log permissions")
				}
			})
			if err := os.Chmod(in.logPath, tt.mode); err != nil {
				t.Fatal("could not set fixture log permissions")
			}
			var printed []byte
			if err := publishLog(in.logPath, exports, sanitize, func(data []byte) { printed = bytes.Clone(data) }); err != nil {
				t.Fatal("sanitized publication failed")
			}
			unchanged, err := os.ReadFile(in.logPath)
			if err != nil || !bytes.Equal(unchanged, raw) {
				t.Fatal("publication mutated its private input")
			}
			exported, err := os.ReadFile(filepath.Join(exports, filepath.Base(in.logPath)))
			if err != nil || !bytes.Equal(exported, clean) || !bytes.Equal(printed, clean) {
				t.Fatal("failure output and exported log must contain the same sanitized bytes")
			}
			entries, err := os.ReadDir(exports)
			if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".log") {
				t.Fatal("publication retained an incomplete export")
			}
		})
	}
}

func TestUnitSanitizedLogPublicationFailures(t *testing.T) {
	for _, name := range []string{"read", "sanitize", "export_directory", "export_rename"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			canary := rand.Text()
			path := filepath.Join(root, "private.log")
			dir := filepath.Join(root, "exports")
			if err := os.WriteFile(path, []byte(canary), 0o600); err != nil {
				t.Fatal("could not prepare private fixture log")
			}
			sanitize := func([]byte) ([]byte, error) { return []byte("[REDACTED]"), nil }
			switch name {
			case "read":
				path = filepath.Join(root, canary)
			case "sanitize":
				sanitize = func(data []byte) ([]byte, error) { return data, errors.New(canary) }
			case "export_directory":
				if err := os.WriteFile(dir, []byte("blocked"), 0o600); err != nil {
					t.Fatal("could not block the export directory")
				}
			case "export_rename":
				if err := os.MkdirAll(filepath.Join(dir, filepath.Base(path)), 0o700); err != nil {
					t.Fatal("could not block the export destination")
				}
			}
			printed := false
			err := publishLog(path, dir, sanitize, func([]byte) { printed = true })
			if err == nil || strings.Contains(err.Error(), canary) || printed {
				t.Fatal("failed publication exposed output or sensitive error details")
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil && name != "export_directory" && !os.IsNotExist(readErr) {
				t.Fatal("could not inspect the export directory")
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					t.Fatal("failed publication retained an export or temporary file")
				}
			}
		})
	}
}

func TestUnitSanitizedLogWithoutExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.log")
	if err := os.WriteFile(path, []byte("private input"), 0o600); err != nil {
		t.Fatal(err)
	}
	var printed []byte
	if err := publishLog(path, "", func([]byte) ([]byte, error) { return []byte("safe output"), nil },
		func(data []byte) { printed = bytes.Clone(data) }); err != nil {
		t.Fatal(err)
	}
	if string(printed) != "safe output" {
		t.Fatal("unconfigured export must still sanitize failure output")
	}
}

func TestUnitDefaultLogPublication(t *testing.T) {
	exports := t.TempDir()
	t.Setenv(ProcessLogDirEnv, exports)
	c := &Cluster{t: t}
	in := &Instance{Name: "peer1", work: t.TempDir(), cluster: c}
	f, err := in.openLog()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("default fixture log"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(in.logPath) != exports {
		t.Fatal("default fixture changed its retained log directory")
	}
	data, err := readTail(in.logPath, logTail)
	if err != nil || data != "default fixture log" {
		t.Fatal("default fixture changed its failure output")
	}
}

func TestUnitSanitizedLogTail(t *testing.T) {
	if got := logTailBytes([]byte("123456"), 3); got != "[first 3 bytes omitted]\n456" {
		t.Fatal("sanitized failure output did not retain the bounded tail")
	}
	if got := logTailBytes([]byte("123"), 3); got != "123" {
		t.Fatal("sanitized failure output truncated a short log")
	}
}
