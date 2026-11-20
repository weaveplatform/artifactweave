package terraformhttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func moduleChecksumURL(raw, archive string, c repo.Config) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || strings.Contains(raw, "#") {
		return "", remoteModuleError()
	}
	target, err := moduleURL(archive, raw)
	if err != nil {
		return "", err
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", remoteModuleError()
	}
	for _, control := range []string{"archive", "checksum", "terraform-get"} {
		if _, present := query[control]; present {
			return "", remoteModuleError()
		}
	}
	selected, _ := url.Parse(target)
	selected.RawQuery = query.Encode()
	selected.ForceQuery = false
	target = selected.String()
	if err := allowModuleURL(c, target); err != nil {
		return "", err
	}
	return target, nil
}

func combineModuleChecksumURL(a, b string) (string, error) {
	if a != "" && b != "" && a != b {
		return "", remoteModuleError()
	}
	if a != "" {
		return a, nil
	}
	return b, nil
}

func parseModuleChecksumControl(r io.Reader, archive, checksumURL string) (kernel.Checksums, error) {
	const limit = (source.MaxModuleChecksumDocumentBytes+2)/3*4 + 16384
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || len(data) > limit {
		return kernel.Checksums{}, remoteModuleError()
	}
	var control source.ModuleChecksumResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&control) != nil || decoder.Decode(new(any)) != io.EOF {
		return kernel.Checksums{}, remoteModuleError()
	}
	return parseModuleChecksumFile(control.Content, archive, checksumURL)
}

func parseModuleChecksumFile(data []byte, archive, checksumURL string) (kernel.Checksums, error) {
	if len(data) == 0 || len(data) > source.MaxModuleChecksumDocumentBytes || !utf8.Valid(data) ||
		bytes.IndexFunc(data, func(r rune) bool { return unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n' }) >= 0 {
		return kernel.Checksums{}, remoteModuleError()
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 8195)
	var selected kernel.Checksums
	records, matches := 0, 0
	standalone := false
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 8192 || strings.ContainsRune(line, '\r') {
			return kernel.Checksums{}, remoteModuleError()
		}
		if strings.Trim(line, " \t") == "" {
			continue
		}
		records++
		checksums, name, err := moduleChecksumRecord(line)
		if err != nil {
			return kernel.Checksums{}, err
		}
		if name == "" {
			standalone = true
		}
		if name == "" || moduleChecksumNameMatches(name, archive, checksumURL) {
			selected = checksums
			matches++
		}
	}
	if scanner.Err() != nil || matches != 1 || standalone && records != 1 {
		return kernel.Checksums{}, remoteModuleError()
	}
	return selected, nil
}

func moduleChecksumRecord(line string) (kernel.Checksums, string, error) {
	if algorithm, rest, bsd := strings.Cut(line, " ("); bsd && !strings.ContainsAny(algorithm, " \t") {
		i := strings.LastIndex(rest, ") = ")
		if i <= 0 {
			return kernel.Checksums{}, "", remoteModuleError()
		}
		checksums, err := moduleChecksum(algorithm + ":" + rest[i+4:])
		if err != nil || !validModuleChecksumName(rest[:i]) {
			return kernel.Checksums{}, "", remoteModuleError()
		}
		return checksums, rest[:i], nil
	}
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		if strings.ContainsRune(line, ':') {
			return kernel.Checksums{}, "", remoteModuleError()
		}
		checksums, err := moduleChecksum(line)
		return checksums, "", err
	}
	if i == 0 || len(line)-i < 3 || line[i] != ' ' || strings.ContainsRune(line[:i], ':') ||
		!strings.ContainsRune(" *?", rune(line[i+1])) || !validModuleChecksumName(line[i+2:]) {
		return kernel.Checksums{}, "", remoteModuleError()
	}
	checksums, err := moduleChecksum(line[:i])
	return checksums, line[i+2:], err
}

func validModuleChecksumName(name string) bool {
	return name != "" && !strings.ContainsRune(name, '\\') &&
		!strings.ContainsFunc(name, unicode.IsControl)
}

func moduleChecksumNameMatches(name, archive, checksumURL string) bool {
	a, err := url.Parse(archive)
	if err != nil {
		return false
	}
	if !strings.ContainsRune(name, '/') {
		return name == path.Base(a.Path)
	}
	c, err := url.Parse(checksumURL)
	if err != nil || a.Scheme != c.Scheme || !strings.EqualFold(a.Host, c.Host) {
		return false
	}
	resolved := path.Clean(name)
	if !strings.HasPrefix(name, "/") {
		resolved = path.Join(path.Dir(c.Path), name)
	}
	return resolved == a.Path
}

func (h handler) resolveModuleChecksum(r *http.Request, m tf.Module, location moduleSource, metadata source.Result) (moduleSource, source.Result, error) {
	if location.ChecksumURL == "" {
		return location, metadata, nil
	}
	c := pkgroute.RequestFrom(r).Repo
	target, err := moduleChecksumURL(location.ChecksumURL, location.Target, c)
	if err != nil {
		return moduleSource{}, metadata, err
	}
	resource := source.Resource{
		Kind: source.ResourceKindMetadata, Method: http.MethodGet, Path: "module-metadata/checksum",
		ExternalURL: target, CheckURL: moduleURLGuard(c, target),
		PermissionFolder: true, PermissionPath: m.Package(), BlackoutOnly: true,
		// Validation selects a record for this archive. Its private URL must
		// distinguish admission variants even when two modules share one file.
		Variant: "terraform-module-checksum-v1:url-policy-v2:" + location.Target,
		Accept:  "text/plain, application/octet-stream", ExpectedContentType: "application/json",
		ModuleChecksumDocument: true,
	}
	resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
		_, err := parseModuleChecksumControl(body, location.Target, target)
		return err
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return moduleSource{}, metadata, fmt.Errorf("terraform module checksum: %w", err)
	}
	defer closeModuleResult(result)
	if result.Stream == nil {
		return moduleSource{}, metadata, remoteModuleError()
	}
	pins, err := parseModuleChecksumControl(result.Stream.Body, location.Target, target)
	if err != nil {
		return moduleSource{}, metadata, err
	}
	location.Checksums, err = combineModuleChecksums(location.Checksums, pins)
	if err != nil {
		return moduleSource{}, metadata, err
	}
	u, _ := url.Parse(location.Target)
	location.ChecksumURL, location.ChecksumName = target, path.Base(u.Path)
	metadata.Revalidate = combineModuleFences([]source.Result{metadata, result}, nil, nil)
	metadata.Used = combineModuleUsage([]source.Result{metadata, result})
	metadata.RejectMetadata = result.RejectMetadata
	return location, metadata, nil
}

func rejectModuleChecksumOnArchiveFailure(ctx context.Context, metadata source.Result, failure error) error {
	var answer *apierror.Error
	var upstream *source.HTTPError
	// The HTTP source adapter already sanitizes failures into API errors.
	// A rejected gateway representation leaves this checksum decision
	// unconfirmed; forget only its exact generation before a fresh retry.
	rejected := errors.As(failure, &answer) && answer.Status == http.StatusBadGateway ||
		errors.As(failure, &upstream) && upstream.Status == http.StatusBadGateway
	if metadata.RejectMetadata == nil || !rejected {
		return failure
	}
	if err := metadata.RejectMetadata(ctx); err != nil {
		return fmt.Errorf("reject invalid module checksum decision: %w", err)
	}
	return failure
}
