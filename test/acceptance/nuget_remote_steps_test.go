package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerRemoteNuGet(sc *godog.ScenarioContext) {
	sc.Step(`^the signed NuGet package "([^"]*)" is tampered into "([^"]*)"$`, w.tamperSignedNuGet)
	sc.Step(
		`^a private NuGet v3 upstream serves signed packages with dependencies$`,
		w.signedNuGetUpstream,
	)
	sc.Step(`^I run verified dotnet "([^"]*)"$`, w.runVerifiedDotnet)
}

func (w *world) runVerifiedDotnet(args string) error {
	flags := []string{
		"--entrypoint",
		"dotnet",
		"-e",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"-e",
		"DOTNET_NOLOGO=1",
		"-e",
		"DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"-e",
		"DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"-e",
		"NUGET_XMLDOC_MODE=skip",
		"-e",
		"DOTNET_NUGET_SIGNATURE_VERIFICATION=true",
		"-v",
		filepath.Join(
			w.dir,
			"nuget-signer.pem",
		) + ":/etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem:ro",
	}
	argv := w.clientArgs(args)
	// The .NET 11 RC2 NativeAOT CLI also hangs in signing. Its documented
	// managed fallback runs the same NuGet commands and cryptographic verifier.
	if len(argv) >= 2 && argv[0] == "nuget" && (argv[1] == "sign" || argv[1] == "verify") {
		flags = append(flags, "-e", "DOTNET_CLI_ENABLEAOT=false")
	}
	return w.runContainer(w.dotnetImage, flags, argv)
}

func (w *world) signedNuGetUpstream() error {
	if _, err := w.filesDir(); err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber:          big.NewInt(628),
		Subject:               pkix.Name{CommonName: "artifactweave NuGet acceptance signer"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if err = os.WriteFile(
		filepath.Join(w.dir, "nuget-signer.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		0o600,
	); err != nil {
		return err
	}
	if err = os.WriteFile(
		filepath.Join(w.dir, "nuget-signer-key.pem"),
		pem.EncodeToMemory(
			&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)},
		),
		0o600,
	); err != nil {
		return err
	}
	if err = w.runContainer(
		w.dotnetImage,
		[]string{"--entrypoint", "openssl"},
		[]string{
			"pkcs12",
			"-export",
			"-out",
			"/work/nuget-signer.pfx",
			"-inkey",
			"/work/nuget-signer-key.pem",
			"-in",
			"/work/nuget-signer.pem",
			"-passout",
			"pass:fixture",
		},
	); err != nil {
		return err
	}
	if w.lastRC != 0 {
		return fmt.Errorf("create fixture PFX: %s", w.body)
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	sum := sha256.Sum256(der)
	w.vars["nugetSigner"] = hex.EncodeToString(sum[:])
	w.remote = fakeupstream.New(
		w.t,
		fakeupstream.Options{
			Auth: fakeupstream.Auth{Username: "upstream", Password: "upstream-fixture"},
		},
	)
	w.closers = append(w.closers, w.remote.Close)
	w.vars["upstream"] = w.remote.URL
	set := func(path string, v any) {
		b, _ := json.Marshal(v)
		w.remote.Set(
			path,
			fakeupstream.Resource{
				Body:   b,
				Header: http.Header{"Content-Type": []string{"application/json"}},
			},
		)
	}
	set("/v3/index.json", map[string]any{"version": "3.0.0", "resources": []any{
		map[string]string{"@id": w.remote.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"},
		map[string]string{
			"@id":   w.remote.URL + "/registration/",
			"@type": "RegistrationsBaseUrl/3.6.0",
		},
	}})
	for _, id := range []string{"Acme.Dependency", "Acme.Signed"} {
		dependency := ""
		if id == "Acme.Signed" {
			dependency = `<dependencies><dependency id="Acme.Dependency" version="[1.0.0]" /></dependencies>`
		}
		name := "signed/" + id + ".1.0.0.nupkg"
		if err = w.writeArchive(
			name,
			[]archiveFile{
				{
					id + ".nuspec",
					`<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>` + id + `</id><version>1.0.0</version><authors>artifactweave</authors><description>Signed remote acceptance package</description>` + dependency + `</metadata></package>`,
				},
				{
					"[Content_Types].xml",
					`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="nuspec" ContentType="application/octet" /></Types>`,
				},
			},
		); err != nil {
			return err
		}
		// The lock hash excludes the signature; preserve the unsigned reference.
		unsigned, readErr := os.ReadFile(filepath.Join(w.dir, name))
		if readErr != nil {
			return readErr
		}
		if err = os.MkdirAll(filepath.Join(w.dir, "unsigned"), 0o700); err != nil {
			return err
		}
		if err = os.WriteFile(
			filepath.Join(w.dir, "unsigned", id+".1.0.0.nupkg"),
			unsigned,
			0o600,
		); err != nil {
			return err
		}
		if err = w.runVerifiedDotnet(
			"nuget sign " + name + " --certificate-path nuget-signer.pfx --certificate-password fixture",
		); err != nil {
			return err
		}
		if w.lastRC != 0 {
			return fmt.Errorf("sign controlled package: %s", w.body)
		}
		body, e := os.ReadFile(filepath.Join(w.dir, name))
		if e != nil {
			return e
		}
		digest := sha512.Sum512(body)
		lower := strings.ToLower(id)
		target := "/content/" + lower + ".signed.nupkg"
		w.remote.Set(
			target,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
			},
		)
		set("/flat/"+lower+"/index.json", map[string]any{"versions": []string{"1.0.0"}})
		set(
			"/registration/"+lower+"/index.json",
			map[string]any{
				"count": 1,
				"items": []any{
					map[string]any{
						"@id":   w.remote.URL + "/opaque-pages/" + lower,
						"count": 1,
						"lower": "1.0.0",
						"upper": "1.0.0",
					},
				},
			},
		)
		groups := []any{}
		if dependency != "" {
			groups = append(
				groups,
				map[string]any{
					"dependencies": []any{
						map[string]string{"id": "Acme.Dependency", "range": "[1.0.0]"},
					},
				},
			)
		}
		set(
			"/opaque-pages/"+lower,
			map[string]any{
				"count": 1,
				"items": []any{
					map[string]any{
						"@id":            w.remote.URL + "/opaque-leaves/" + lower,
						"packageContent": w.remote.URL + target,
						"catalogEntry": map[string]any{
							"@id":                  w.remote.URL + "/catalog/" + lower,
							"id":                   id,
							"version":              "1.0.0",
							"listed":               true,
							"dependencyGroups":     groups,
							"packageHashAlgorithm": "SHA512",
							"packageHash":          base64.StdEncoding.EncodeToString(digest[:]),
						},
					},
				},
			},
		)
	}
	return nil
}

// Retain the real signature and valid ZIP CRCs while changing signed content;
// native verification must reject this despite a structurally valid archive.
func (w *world) tamperSignedNuGet(src, dst string) error {
	raw, err := os.ReadFile(filepath.Join(w.dir, src))
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".nuspec") {
			// The signature entry forbids ZIP data descriptors. Copy its raw header
			// and compressed bytes so this fixture fails content verification only.
			if err = zw.Copy(f); err != nil {
				return err
			}
			continue
		}
		rc, e := f.Open()
		if e != nil {
			return e
		}
		b, e := io.ReadAll(rc)
		_ = rc.Close()
		if e != nil {
			return e
		}
		b = bytes.Replace(
			b,
			[]byte("Signed remote acceptance package"),
			[]byte("Altered signed package content"),
			1,
		)
		entry, e := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Store})
		if e != nil {
			return e
		}
		if _, e = entry.Write(b); e != nil {
			return e
		}
	}
	if err = zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, dst), out.Bytes(), 0o600)
}
