package item

import (
	"bytes"
	"errors"
	"testing"
)

// TestUnitMimeTypeBox: a .box is recorded as application/x-vagrant-box, the
// type Artifactory's default MIME table gives the extension, whatever its
// case (#1266).
func TestUnitMimeTypeBox(t *testing.T) {
	for name, want := range map[string]string{
		"focal.box":        "application/x-vagrant-box",
		"dir/FOCAL.BOX":    "application/x-vagrant-box",
		"focal.box.gz":     "application/x-gzip",
		"app.jar":          "application/java-archive",
		"index.yaml":       "text/plain",
		"acme-1.0.0.tgz":   "application/x-gzip",
		"layout.tar":       "application/x-tar",
		"plain.bin":        "application/octet-stream",
		"box":              "application/octet-stream",
		"focal.boxed":      "application/octet-stream",
		"vagrant/metadata": "application/octet-stream",
	} {
		if got := MimeType(name); got != want {
			t.Errorf("MimeType(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestUnitRemoteBoxKeepsItsMIMECheck: a remote repository caches a .box
// under whatever archive label its upstream sends and records it with the
// box type it downloads with. Artifactory refuses only the labels on its
// block list, so an HTML page is still refused.
func TestUnitRemoteBoxKeepsItsMIMECheck(t *testing.T) {
	data := []byte("box archive bytes")
	for label, refused := range map[string]bool{
		"application/x-vagrant-box": false,
		"application/octet-stream":  false,
		"binary/octet-stream":       false,
		"application/x-tar":         false,
		"application/gzip":          false,
		"application/x-gzip":        false,
		"text/html":                 true,
	} {
		t.Run(label, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Token.Key.Path = "boxes/focal.box"
			u.ContentType, u.Body, u.Length = label, bytes.NewReader(data), int64(len(data))
			var err error
			u.Token, err = w.db.RemoteCache().Begin(t.Context(), u.Token.Key)
			if err != nil {
				t.Fatal(err)
			}
			result, err := w.svc.Cache(t.Context(), u)
			if refused {
				if !errors.Is(err, ErrRemoteMIME) {
					t.Fatalf("label %s: %v, want %v", label, err, ErrRemoteMIME)
				}
				return
			}
			if err != nil {
				t.Fatalf("label %s refused: %v", label, err)
			}
			if result.Body != nil {
				_ = result.Body.Close()
			}
			if result.Item.MimeType != "application/x-vagrant-box" {
				t.Fatalf("recorded as %q", result.Item.MimeType)
			}
		})
	}
}
