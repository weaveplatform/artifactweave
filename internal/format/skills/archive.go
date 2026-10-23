package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	MaxArchiveBytes = 32 << 20
	MaxSkillBytes   = 5 << 20
	MaxFiles        = 256
)

var ErrArchive = errors.New("invalid Skills bundle")

// Metadata is the package record derived from the canonical path and
// SKILL.md. The publish request may supply display fields when the manifest
// omits them; the format's path and digest remain authoritative.
type Metadata struct {
	Slug        string   `json:"slug"`
	Version     string   `json:"version"`
	Fingerprint string   `json:"fingerprint"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	DisplayName string   `json:"displayName,omitempty"` //nolint:tagliatelle // ClawHub's wire name
	Summary     string   `json:"summary,omitempty"`
	Changelog   string   `json:"changelog,omitempty"`
}

type fields struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version"`
	Author      string   `yaml:"author"`
	Tags        []string `yaml:"tags"`
	DisplayName string   `yaml:"displayName"` //nolint:tagliatelle // SKILL.md metadata name
	Summary     string   `yaml:"summary"`
	Changelog   string   `yaml:"changelog"`
}

type frontmatter struct {
	fields   `yaml:",inline"`
	Metadata struct {
		Skills   fields `yaml:"skills"`
		Clawdbot fields `yaml:"clawdbot"`
		Clawdis  fields `yaml:"clawdis"`
	} `yaml:"metadata"`
}

// ReadArchive validates a bounded ZIP without extracting it. Every archived
// name is checked before parsing metadata so a bundle cannot smuggle paths
// outside the skill directory into a later single-file download.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size < 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil || len(data) > MaxArchiveBytes || int64(len(data)) != size {
		return Metadata{}, ErrArchive
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) == 0 || len(z.File) > MaxFiles {
		return Metadata{}, ErrArchive
	}
	seen := map[string]bool{}
	var skill *zip.File
	var total uint64
	for _, file := range z.File {
		name := file.Name
		if !safeArchiveName(name) || seen[name] ||
			!file.Mode().IsRegular() && !file.FileInfo().IsDir() {
			return Metadata{}, ErrArchive
		}
		seen[name] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if err := validateEntry(file, &total); err != nil {
			return Metadata{}, ErrArchive
		}
		if path.Base(name) == "SKILL.md" {
			if file.UncompressedSize64 > MaxSkillBytes {
				return Metadata{}, ErrArchive
			}
			if skill == nil || strings.Count(name, "/") < strings.Count(skill.Name, "/") ||
				strings.Count(name, "/") == strings.Count(skill.Name, "/") && name < skill.Name {
				skill = file
			}
		}
	}
	if skill == nil {
		return Metadata{}, nil
	}
	rc, err := skill.Open()
	if err != nil {
		return Metadata{}, fmt.Errorf("skills: open SKILL.md: %w", ErrArchive)
	}
	defer rc.Close()
	data, err = io.ReadAll(io.LimitReader(rc, MaxSkillBytes+1))
	if err != nil || len(data) > MaxSkillBytes {
		return Metadata{}, ErrArchive
	}
	return parseFrontmatter(data)
}

func validateEntry(file *zip.File, total *uint64) error {
	if file.UncompressedSize64 > MaxArchiveBytes {
		return ErrArchive
	}
	*total += file.UncompressedSize64
	if *total > MaxArchiveBytes {
		return ErrArchive
	}
	rc, err := file.Open()
	if err != nil {
		return ErrArchive
	}
	n, readErr := io.Copy(io.Discard, io.LimitReader(rc, MaxArchiveBytes+1))
	closeErr := rc.Close()
	// io.Copy counts bytes from zero, so this conversion cannot be negative.
	if readErr != nil || closeErr != nil || uint64(n) != file.UncompressedSize64 || //nolint:gosec
		n > MaxArchiveBytes {
		return ErrArchive
	}
	return nil
}

func safeArchiveName(name string) bool {
	if name == "" || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) ||
		strings.HasPrefix(name, "/") ||
		strings.HasPrefix(name, "./") ||
		path.Clean(name) != strings.TrimSuffix(name, "/") {
		return false
	}
	for part := range strings.SplitSeq(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func parseFrontmatter(data []byte) (Metadata, error) {
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) < 3 || strings.TrimSpace(string(lines[0])) != "---" {
		return Metadata{}, nil
	}
	end := slices.IndexFunc(lines[1:], func(line []byte) bool {
		return strings.TrimSpace(string(line)) == "---"
	})
	if end < 0 {
		return Metadata{}, ErrArchive
	}
	var doc frontmatter
	if err := yaml.Unmarshal(bytes.Join(lines[1:1+end], []byte("\n")), &doc); err != nil {
		return Metadata{}, ErrArchive
	}
	f := doc.fields
	for _, nested := range []fields{doc.Metadata.Skills, doc.Metadata.Clawdbot, doc.Metadata.Clawdis} {
		if nested.Name != "" {
			f.Name = nested.Name
		}
		if nested.Description != "" {
			f.Description = nested.Description
		}
		if nested.Version != "" {
			f.Version = nested.Version
		}
		if nested.Author != "" {
			f.Author = nested.Author
		}
		if len(nested.Tags) > 0 {
			f.Tags = nested.Tags
		}
		if nested.DisplayName != "" {
			f.DisplayName = nested.DisplayName
		}
		if nested.Summary != "" {
			f.Summary = nested.Summary
		}
		if nested.Changelog != "" {
			f.Changelog = nested.Changelog
		}
	}
	if !validFields(f) {
		return Metadata{}, ErrArchive
	}
	return Metadata{
		Name: f.Name, Description: f.Description, Author: f.Author,
		Tags: f.Tags, DisplayName: f.DisplayName, Summary: f.Summary, Changelog: f.Changelog,
	}, nil
}

func validFields(f fields) bool {
	for _, value := range []string{
		f.Name, f.Description, f.Version, f.Author, f.DisplayName, f.Summary, f.Changelog,
	} {
		if len(value) > 4096 {
			return false
		}
	}
	if len(f.Tags) > 64 {
		return false
	}
	for _, tag := range f.Tags {
		if len(tag) > 256 {
			return false
		}
	}
	return true
}
