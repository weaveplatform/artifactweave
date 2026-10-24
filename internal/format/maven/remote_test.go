package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitSourceMetadataValidation(t *testing.T) {
	for _, body := range []string{"<html>error</html>", "<metadata>", "<metadata/><metadata/>", "<metadata/>garbage", "<metadata/>\x00", strings.Repeat(" ", maxPOM+1)} {
		if _, err := ReadSourceMetadata(
			strings.NewReader(body),
		); !errors.Is(
			err,
			ErrRemoteContent,
		) {
			t.Fatalf("invalid metadata admitted (%d bytes): %v", len(body), err)
		}
	}
	if _, err := ReadSourceMetadata(
		iotest.ErrReader(io.ErrUnexpectedEOF),
	); !errors.Is(
		err,
		io.ErrUnexpectedEOF,
	) {
		t.Fatal(err)
	}
	got, err := ReadSourceMetadata(
		strings.NewReader(
			`<metadata><versioning><latest>2</latest><release>1</release><versions><version>1</version><version>2</version></versions></versioning></metadata> `,
		),
	)
	if err != nil || len(got.Versioning.Versions) != 2 || got.Versioning.Latest != "2" {
		t.Fatal(got, err)
	}
}

func TestUnitRemotePOMAndJARAdmission(t *testing.T) {
	l, err := layout.Lookup(repo.LayoutMaven2)
	if err != nil {
		t.Fatal(err)
	}
	c := repo.Config{RejectInvalidJars: new(true)}
	for _, tc := range []struct {
		path, body string
		allowed    bool
	}{
		{"org/acme/app/1.0/app-1.0.pom", `<project><parent><groupId>org.acme</groupId><version>1.0</version></parent><artifactId>app</artifactId></project>`, true},
		{"org/acme/app/1.0/app-1.0.pom", `<project><groupId>wrong</groupId><artifactId>app</artifactId><version>1.0</version></project>`, false},
		{"org/acme/app/1.0/app-1.0.pom", `<html>not a pom</html>`, false},
		{"org/acme/app/maven-metadata.xml", `<metadata/>`, true},
		{"org/acme/app/1.0/app-1.0.jar", `not a jar`, false},
		{"org/acme/app/1.0/app-1.0.txt", `plain artifact`, true},
	} {
		err := ValidateRemote(
			t.Context(),
			c,
			l,
			tc.path,
			strings.NewReader(tc.body),
			int64(len(tc.body)),
		)
		if (err == nil) != tc.allowed {
			t.Fatal(tc.path, tc.allowed, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := ValidateRemote(
		ctx,
		c,
		l,
		"a.jar",
		strings.NewReader(""),
		0,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.CreateHeader(&zip.FileHeader{Name: "data", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("unique payload inside ZIP"))
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = ValidateRemote(
		t.Context(),
		c,
		l,
		"a.jar",
		bytes.NewReader(b.Bytes()),
		int64(b.Len()),
	); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(b.Bytes())
	corrupt[bytes.Index(corrupt, []byte("unique payload"))] = 'X'
	if err = ValidateRemote(
		t.Context(),
		c,
		l,
		"a.jar",
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
	); !errors.Is(
		err,
		ErrRemoteContent,
	) {
		t.Fatal("CRC error not rejected", err)
	}
	c.SuppressPomConsistencyChecks, c.RejectInvalidJars = new(true), new(false)
	if err = ValidateRemote(
		t.Context(),
		c,
		l,
		"a.pom",
		strings.NewReader("invalid"),
		7,
	); err != nil {
		t.Fatal(err)
	}
	if err = ValidateRemote(
		t.Context(),
		c,
		l,
		"a.jar",
		strings.NewReader("invalid"),
		7,
	); err != nil {
		t.Fatal(err)
	}
}
