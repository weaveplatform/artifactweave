package hex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/pem"
	"io"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

type hexBrokenArchiveReader struct{}

func (hexBrokenArchiveReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// A valid signature proves who supplied a registry resource, not that its
// contents are unambiguous. These resources must fail before they can be
// re-signed under an ArtifactWeave repository key.
func TestUnitHexSignedRegistryRejectsHostileReleaseFields(t *testing.T) {
	key := testKey()
	var digest [32]byte
	digest[0] = 1
	valid := appendString(nil, 1, "1.0.0")
	valid = appendBytes(valid, 2, digest[:])
	valid = appendBytes(valid, 5, digest[:])
	validDep := appendString(nil, 1, "decimal")
	validDep = appendString(validDep, 2, "~> 2.0")
	wrongType := func(field protowire.Number) []byte {
		b := protowire.AppendTag(nil, field, protowire.VarintType)
		return protowire.AppendVarint(b, 1)
	}
	var invalidIndex []byte
	invalidIndex = protowire.AppendTag(invalidIndex, 6, protowire.VarintType)
	invalidIndex = protowire.AppendVarint(invalidIndex, 10001)
	var duplicateRetirement []byte
	duplicateRetirement = appendBytes(duplicateRetirement, 4, appendString(nil, 1, "x"))
	duplicateRetirement = appendBytes(duplicateRetirement, 4, appendString(nil, 1, "y"))
	var duplicatePublished []byte
	duplicatePublished = appendBytes(duplicatePublished, 7, appendString(nil, 1, "x"))
	duplicatePublished = appendBytes(duplicatePublished, 7, appendString(nil, 1, "y"))
	cases := map[string][]byte{
		"duplicate version":        appendString(bytes.Clone(valid), 1, "2.0.0"),
		"duplicate inner checksum": appendBytes(bytes.Clone(valid), 2, digest[:]),
		"duplicate outer checksum": appendBytes(bytes.Clone(valid), 5, digest[:]),
		"short outer checksum": appendBytes(
			appendString(appendBytes(nil, 2, digest[:]), 1, "1.0.0"),
			5,
			[]byte{1},
		),
		"retirement wire type":     append(bytes.Clone(valid), wrongType(4)...),
		"duplicate retirement":     append(bytes.Clone(valid), duplicateRetirement...),
		"published time wire type": append(bytes.Clone(valid), wrongType(7)...),
		"duplicate published time": append(bytes.Clone(valid), duplicatePublished...),
		"advisory index wire type": appendBytes(bytes.Clone(valid), 6, []byte{1}),
		"advisory index too large": append(bytes.Clone(valid), invalidIndex...),
		"dependency wire type":     append(bytes.Clone(valid), wrongType(3)...),
		"empty dependency":         appendBytes(bytes.Clone(valid), 3, nil),
		"duplicate dependency name": appendBytes(bytes.Clone(valid), 3,
			appendString(bytes.Clone(validDep), 1, "other")),
		"duplicate requirement": appendBytes(bytes.Clone(valid), 3,
			appendString(bytes.Clone(validDep), 2, "~> 3.0")),
		"dependency optional wire type": appendBytes(bytes.Clone(valid), 3,
			appendBytes(bytes.Clone(validDep), 3, []byte{1})),
	}
	for name, release := range cases {
		t.Run(name, func(t *testing.T) {
			payload := appendBytes(nil, 1, release)
			payload = appendString(payload, 2, "example")
			payload = appendString(payload, 3, "hexpm")
			signed, err := sign(payload, key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePackage(signed, &key.PublicKey); err == nil {
				t.Fatal("accepted ambiguous or malformed signed release")
			}
		})
	}
	for name, dep := range map[string][]byte{
		"app has wrong wire type":        append(validDep, wrongType(4)...),
		"repository has wrong wire type": append(validDep, wrongType(5)...),
	} {
		t.Run(name, func(t *testing.T) {
			release := appendBytes(bytes.Clone(valid), 3, dep)
			payload := appendBytes(nil, 1, release)
			payload = appendString(payload, 2, "example")
			payload = appendString(payload, 3, "hexpm")
			signed, err := sign(payload, key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePackage(signed, &key.PublicKey); err == nil {
				t.Fatal("accepted malformed dependency identity")
			}
		})
	}
}

func TestUnitHexSignedRegistryRejectsHostileTopLevelFields(t *testing.T) {
	key := testKey()
	var digest [32]byte
	digest[0] = 1
	release := appendString(nil, 1, "1.0.0")
	release = appendBytes(release, 2, digest[:])
	badVarint := func(field protowire.Number) []byte {
		b := protowire.AppendTag(nil, field, protowire.VarintType)
		return protowire.AppendVarint(b, 1)
	}
	valid := appendBytes(nil, 1, release)
	valid = appendString(valid, 2, "example")
	valid = appendString(valid, 3, "hexpm")
	for name, payload := range map[string][]byte{
		"release wrong wire type":    append(badVarint(1), appendString(appendString(nil, 2, "example"), 3, "hexpm")...),
		"repository wrong wire type": append(appendBytes(appendString(nil, 2, "example"), 1, release), badVarint(3)...),
		"advisory wrong wire type":   append(bytes.Clone(valid), badVarint(4)...),
		"malformed advisory":         appendBytes(bytes.Clone(valid), 4, []byte{0xff}),
		"duplicate repository":       appendString(bytes.Clone(valid), 3, "other"),
	} {
		t.Run(name, func(t *testing.T) {
			signed, err := sign(payload, key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePackage(signed, &key.PublicKey); err == nil {
				t.Fatal("accepted ambiguous signed package metadata")
			}
		})
	}
	if _, err := EncodeVersions("", nil, key); err == nil {
		t.Fatal("signed versions without a source repository")
	}
	if _, err := EncodeVersions("hexpm", []RegistryPackage{{Name: "../bad"}}, key); err == nil {
		t.Fatal("signed unsafe package name in versions")
	}
	badVersionItem := badVarint(1)
	badVersionIndex := appendBytes(nil, 1, badVersionItem)
	badVersionIndex = appendString(badVersionIndex, 2, "hexpm")
	signed, err := sign(badVersionIndex, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeVersions(signed, &key.PublicKey); err == nil {
		t.Fatal("accepted non-string package identity in signed versions")
	}
	if _, _, err := DecodeVersions([]byte("not gzip"), &key.PublicKey); err == nil {
		t.Fatal("accepted unsigned versions index")
	}
}

func TestUnitHexRegistryRejectsTruncatedProtobufFields(t *testing.T) {
	for name, data := range map[string][]byte{
		"truncated tag":         {0xff},
		"truncated byte field":  {0x0a, 0x02, 0x01},
		"truncated varint":      {0x08, 0x80},
		"truncated fixed field": {0x09, 0x01},
	} {
		t.Run(name, func(t *testing.T) {
			if err := fields(data, func(protowire.Number, protowire.Type, []byte) error {
				return nil
			}); err == nil {
				t.Fatal("accepted truncated wire field")
			}
		})
	}
}

func TestUnitHexMetadataEscapesRemainData(t *testing.T) {
	fields, err := parseMetadata([]byte(`{<<"name">>,<<"line\ncarriage\rtab\tliteral\\slash">>}.`))
	if err != nil || fields["name"] != "line\ncarriage\rtab\tliteral\\slash" {
		t.Fatalf("escaped metadata was evaluated incorrectly: %#v %v", fields, err)
	}
	for name, body := range map[string][]byte{
		"bad binary prefix":  []byte(`{<<"name">>,<"decimal">>}.`),
		"trailing backslash": []byte(`{<<"name">>,<<"decimal\`),
		"missing binary end": []byte(`{<<"name">>,<<"decimal"}.`),
		"atom boundary":      []byte(`{<<"active">>,trueevil}.`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMetadata(body); err == nil {
				t.Fatal("accepted malformed Erlang metadata")
			}
		})
	}
}

func TestUnitHexRegistryPublicKeyAndIndexIdentityBoundaries(t *testing.T) {
	key := testKey()
	public := pem.EncodeToMemory(&pem.Block{
		Type: "PUBLIC KEY", Bytes: mustPublicDER(&key.PublicKey),
	})
	if _, err := ParsePublicKey(string(public) + "forged trailing key"); err == nil {
		t.Fatal("accepted trailing key material")
	}
	otherDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePublicKey(string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: otherDER,
	}))); err == nil {
		t.Fatal("accepted a certificate as a registry key")
	}
	if _, err := EncodeVersions("hexpm", nil, nil); err == nil {
		t.Fatal("signed a versions index with no private key")
	}
	var item []byte
	item = appendString(item, 1, "decimal")
	item = appendString(item, 2, "1.0.0")
	for name, payload := range map[string][]byte{
		"version wrong wire type": appendBytes(appendString(nil, 2, "hexpm"), 1,
			append(item, protowire.AppendVarint(protowire.AppendTag(nil, 2, protowire.VarintType), 1)...)),
		"duplicate repository": appendString(appendBytes(appendString(nil, 2, "hexpm"), 1, item), 2, "other"),
	} {
		t.Run(name, func(t *testing.T) {
			signed, err := sign(payload, key)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := DecodeVersions(signed, &key.PublicKey); err == nil {
				t.Fatal("accepted ambiguous versions index")
			}
		})
	}
}

func TestUnitHexArchiveRejectsSyntheticOuterAttacks(t *testing.T) {
	if _, err := InspectTarball(hexBrokenArchiveReader{}); err == nil {
		t.Fatal("accepted archive after a truncated source read")
	}
	for name, headers := range map[string][]tar.Header{
		"unexpected outer file": {{Name: "../../secret", Typeflag: tar.TypeReg}},
		"outer symlink":         {{Name: "metadata.config", Typeflag: tar.TypeSymlink, Linkname: "../secret"}},
		"duplicate outer file":  {{Name: "VERSION", Typeflag: tar.TypeReg}, {Name: "VERSION", Typeflag: tar.TypeReg}},
	} {
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			writer := tar.NewWriter(&body)
			for _, header := range headers {
				if err := writer.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectTarball(bytes.NewReader(body.Bytes())); err == nil {
				t.Fatal("accepted unsafe Hex outer tar structure")
			}
		})
	}
}

func TestUnitHexRequirementsRejectMalformedLegacyTuples(t *testing.T) {
	for name, requirement := range map[string]any{
		"nonstring legacy name": tuple{42, []any{}},
		"nonlist legacy values": tuple{"decimal", "not-a-list"},
		"nonstring attribute":   []any{tuple{42, "decimal"}},
		"empty attribute key":   []any{tuple{"", "decimal"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRequirements([]any{requirement}); err == nil {
				t.Fatal("accepted malformed Hex dependency identity")
			}
		})
	}
}

func TestUnitHexRegistryPreservesDistinctAppName(t *testing.T) {
	key := testKey()
	var digest [32]byte
	digest[0] = 1
	pkg := Package{
		Name: "example", Version: "1.0.0",
		InnerChecksum: digest, OuterChecksum: digest,
		Requirements: []Requirement{{
			Package: "decimal", App: "decimal_custom", Requirement: "~> 2.0",
		}},
	}
	signed, err := EncodePackage(RegistryPackage{
		Name: pkg.Name, Repository: "hexpm", Releases: []Package{pkg},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePackage(signed, &key.PublicKey)
	if err != nil || decoded.Releases[0].Requirements[0].App != "decimal_custom" {
		t.Fatalf("custom application name was lost in signed metadata: %+v %v", decoded, err)
	}
	if _, err := sign([]byte("untrusted"), nil); err == nil {
		t.Fatal("signed registry payload without a key")
	}
}

func TestUnitHexRegistryRejectsForgedSignatureEnvelope(t *testing.T) {
	key := testKey()
	valid, err := EncodeNames("hexpm", []string{"decimal"}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeNames(valid, nil); err == nil {
		t.Fatal("accepted a registry without a trusted key")
	}
	if _, _, err := DecodeNames([]byte("not gzip"), &key.PublicKey); err == nil {
		t.Fatal("accepted an uncompressed registry")
	}
	var payload []byte
	payload = appendBytes(payload, 1, appendString(nil, 1, "decimal"))
	payload = appendString(payload, 2, "hexpm")
	// Each envelope is gzip-compressed and syntactically valid; the signature
	// must still bind one and only one payload to the trusted public key.
	signed, err := sign(payload, key)
	if err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(bytes.NewReader(signed))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := io.ReadAll(z)
	_ = z.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, _, n := protowire.ConsumeTag(envelope)
	if n < 0 {
		t.Fatal("signed fixture has no payload field")
	}
	_, payloadFieldSize := protowire.ConsumeBytes(envelope[n:])
	if payloadFieldSize < 0 {
		t.Fatal("signed fixture payload is malformed")
	}
	// Wrong signature and duplicate payload are both identity ambiguity.
	for name, bad := range map[string][]byte{
		"duplicate payload":       appendBytes(bytes.Clone(envelope), 1, payload),
		"duplicate signature":     appendBytes(bytes.Clone(envelope), 2, []byte("forged")),
		"missing signature":       bytes.Clone(envelope[:n+payloadFieldSize]),
		"invalid field wire type": wrongRegistryEnvelopeWireType(),
	} {
		t.Run(name, func(t *testing.T) {
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			if _, err := writer.Write(bad); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := DecodeNames(compressed.Bytes(), &key.PublicKey); err == nil {
				t.Fatal("accepted forged registry envelope")
			}
		})
	}
	// A valid gzip envelope with altered signed bytes must fail verification.
	altered := bytes.Clone(envelope)
	altered[n+2] ^= 1
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(altered); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeNames(compressed.Bytes(), &key.PublicKey); err == nil {
		t.Fatal("accepted altered signed metadata")
	}
}

func wrongRegistryEnvelopeWireType() []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}
