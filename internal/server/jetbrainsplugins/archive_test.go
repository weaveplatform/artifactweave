package jetbrainsplugins

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
)

func pluginJar(t *testing.T, id string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("META-INF/plugin.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte(`<idea-plugin><id>` + id + `</id><name>Weave Fixture</name>` +
		`<version>1.0.0</version><idea-version since-build="252" until-build="252.*"/>` +
		`</idea-plugin>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnitArchiveValidationBindsDescriptorToRequestedPlugin(t *testing.T) {
	valid := pluginJar(t, "com.weave.fixture")
	wrong := pluginJar(t, "com.other")
	for _, tc := range []struct {
		body []byte
		ok   bool
	}{
		{valid, true},
		{wrong, false},
		{[]byte("not a plugin archive"), false},
	} {
		err := validateArchive("com.weave.fixture")(
			context.Background(), bytes.NewReader(tc.body), int64(len(tc.body)),
		)
		if (err == nil) != tc.ok {
			t.Errorf("archive err %v, want valid %v", err, tc.ok)
		}
	}
}

func TestUnitArchiveRejectsTraversingEntryEvenWithValidDescriptor(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	_, err := z.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	f, err := z.Create("META-INF/plugin.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(
		[]byte(
			`<idea-plugin><id>com.weave.fixture</id><name>Weave Fixture</name><version>1.0.0</version><idea-version since-build="252"/></idea-plugin>`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateArchive("com.weave.fixture")(
		context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()),
	); err == nil {
		t.Fatal("archive with traversal entry passed validation")
	}
}

func TestUnitArchiveRejectsUnsafeEntryAfterValidDescriptor(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("META-INF/plugin.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(
		[]byte(
			`<idea-plugin><id>com.weave.fixture</id><name>Weave Fixture</name><version>1.0.0</version><idea-version since-build="252"/></idea-plugin>`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := z.Create("../outside.txt"); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateArchive("com.weave.fixture")(
		context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()),
	); err == nil {
		t.Fatal("later traversal entry escaped archive validation")
	}
}

func TestUnitArchiveRejectsCorruptPayloadAfterValidDescriptor(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("META-INF/plugin.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(
		[]byte(
			`<idea-plugin><id>com.weave.fixture</id><name>Weave Fixture</name><version>1.0.0</version><idea-version since-build="252"/></idea-plugin>`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := z.CreateHeader(&zip.FileHeader{Name: "lib/payload.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payload.Write([]byte("payload123")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(b.Bytes(), []byte("payload123"), []byte("payload124"), 1)
	if bytes.Equal(corrupt, b.Bytes()) {
		t.Fatal("fixture payload was not corrupted")
	}
	if err := validateArchive("com.weave.fixture")(
		context.Background(), bytes.NewReader(corrupt), int64(len(corrupt)),
	); err == nil {
		t.Fatal("non-descriptor CRC mismatch escaped archive validation")
	}
}

func TestUnitArchiveRejectsDotAndDotDotEntries(t *testing.T) {
	for _, name := range []string{".", "..", "lib/../escape.jar"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			f, err := z.Create("META-INF/plugin.xml")
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Write(
				[]byte(
					`<idea-plugin><id>com.weave.fixture</id><name>Weave Fixture</name><version>1.0.0</version><idea-version since-build="252"/></idea-plugin>`,
				),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := z.Create(name); err != nil {
				t.Fatal(err)
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			if err := validateArchive("com.weave.fixture")(
				context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()),
			); err == nil {
				t.Fatalf("unsafe ZIP entry %q passed", name)
			}
		})
	}
}

func TestUnitNestedJarRejectsCorruptTrailingEntry(t *testing.T) {
	var nested bytes.Buffer
	nestedZip := zip.NewWriter(&nested)
	descriptor, err := nestedZip.Create("META-INF/plugin.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = descriptor.Write(
		[]byte(
			`<idea-plugin><id>com.weave.fixture</id><name>Weave Fixture</name><version>1.0.0</version><idea-version since-build="252"/></idea-plugin>`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := nestedZip.CreateHeader(&zip.FileHeader{Name: "payload.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payload.Write([]byte("nested-payload123")); err != nil {
		t.Fatal(err)
	}
	if err := nestedZip.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(nested.Bytes(), []byte("nested-payload123"),
		[]byte("nested-payload124"), 1)
	if bytes.Equal(corrupt, nested.Bytes()) {
		t.Fatal("nested fixture payload was not corrupted")
	}
	var outer bytes.Buffer
	outerZip := zip.NewWriter(&outer)
	jar, err := outerZip.Create("plugin/lib/plugin.jar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jar.Write(corrupt); err != nil {
		t.Fatal(err)
	}
	if err := outerZip.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateArchive("com.weave.fixture")(
		context.Background(), bytes.NewReader(outer.Bytes()), int64(outer.Len()),
	); err == nil {
		t.Fatal("corrupt trailing nested JAR entry passed validation")
	}
}

func TestUnitArchiveStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := pluginJar(t, "com.weave.fixture")
	if err := validateArchive(
		"com.weave.fixture",
	)(
		ctx,
		bytes.NewReader(body),
		int64(len(body)),
	); err == nil {
		t.Fatal("cancelled archive validation succeeded")
	}
}
