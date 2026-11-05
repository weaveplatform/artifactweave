package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const recipeDir = "docs/client-recipes"

type recipe struct {
	Schema         int         `json:"schema"`
	Type           string      `json:"type"`
	Name           string      `json:"name"`
	Client         string      `json:"client"`
	VersionSources []string    `json:"version_sources"`
	Endpoint       string      `json:"endpoint"`
	Auth           string      `json:"auth"`
	FreshHome      string      `json:"fresh_home"`
	Prerequisites  []string    `json:"prerequisites"`
	Configure      []snippet   `json:"configure"`
	Publish        operation   `json:"publish"`
	Install        operation   `json:"install"`
	Limitations    []string    `json:"limitations"`
	Evidence       []reference `json:"evidence"`
}

type snippet struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Note     string `json:"note"`
}

type operation struct {
	Classes string    `json:"classes"`
	Note    string    `json:"note"`
	Steps   []snippet `json:"steps"`
}

type reference struct {
	Path     string   `json:"path"`
	Contains []string `json:"contains"`
}

type inventory struct {
	Rows []format `json:"rows"`
}

type format struct {
	Type    string `json:"type"`
	Classes string `json:"classes"`
	Status  string `json:"status"`
	P9c     bool   `json:"p9c"`
}

func main() {
	check := flag.Bool(
		"check",
		false,
		"check committed recipes, documentation and evidence fingerprints",
	)
	flag.Parse()
	if err := run(*check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(check bool) error {
	data, err := os.ReadFile("docs/implementation/package-format-inventory.json")
	if err != nil {
		return err
	}
	var catalog inventory
	if err := json.Unmarshal(data, &catalog); err != nil {
		return err
	}
	paths, err := filepath.Glob(recipeDir + "/src/*.json")
	if err != nil {
		return err
	}
	recipes := make(map[string]recipe, len(paths))
	for _, path := range paths {
		r, err := readRecipe(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if filepath.Base(path) != r.Type+".json" {
			return fmt.Errorf("%s: filename must match recipe type %q", path, r.Type)
		}
		recipes[r.Type] = r
	}
	fingerprints, err := validate(catalog, recipes)
	if err != nil {
		return err
	}
	outputs := map[string][]byte{
		"index.md": renderIndex(catalog, recipes),
	}
	for _, f := range catalog.Rows {
		if r, ok := recipes[f.Type]; ok {
			outputs[f.Type+".md"] = render(r, f)
		}
	}
	data, err = json.MarshalIndent(fingerprints, "", "  ")
	if err != nil {
		return err
	}
	outputs["evidence-lock.json"] = append(data, '\n')
	for _, name := range sortedKeys(outputs) {
		if err := writeOrCheck(filepath.Join(recipeDir, name), outputs[name], check); err != nil {
			return err
		}
	}
	return nil
}

func readRecipe(path string) (recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return recipe{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var r recipe
	if err := decoder.Decode(&r); err != nil {
		return r, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return r, errors.New("expected exactly one recipe object")
	}
	return r, nil
}

func validate(catalog inventory, recipes map[string]recipe) (map[string]string, error) {
	fingerprints := make(map[string]string)
	seen := make(map[string]bool)
	for _, f := range catalog.Rows {
		r, ok := recipes[f.Type]
		if f.Status != "native" && f.Status != "core" {
			if ok {
				return nil, fmt.Errorf("%s: inventory does not claim native support", f.Type)
			}
			continue
		}
		if !ok {
			return nil, fmt.Errorf("%s: missing recipe for implemented format", f.Type)
		}
		if err := validateRecipe(r, f, fingerprints); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Type, err)
		}
		seen[f.Type] = true
	}
	for typ := range recipes {
		if !seen[typ] {
			return nil, fmt.Errorf("%s: recipe absent from implemented inventory", typ)
		}
	}
	return fingerprints, nil
}

func validateRecipe(r recipe, f format, fingerprints map[string]string) error {
	if r.Schema != 1 || r.Type != f.Type || r.Name == "" || r.Client == "" ||
		r.Endpoint == "" || r.Auth == "" || r.FreshHome == "" ||
		len(r.VersionSources) == 0 || len(r.Evidence) == 0 || len(r.Configure) == 0 {
		return errors.New(
			"schema, identity, client, endpoint, authentication, isolation, configuration and evidence are required",
		)
	}
	if !strings.Contains(r.Endpoint, "${AW_URL}") {
		return errors.New("endpoint must identify the selected AW_URL")
	}
	if err := validateSnippets(r.Configure); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	for _, op := range []operation{r.Publish, r.Install} {
		if op.Note == "" || (op.Classes == "") != (len(op.Steps) == 0) {
			return errors.New(
				"each operation needs a note and commands exactly when classes are supported",
			)
		}
		var seen string
		for _, class := range op.Classes {
			if !strings.ContainsRune(f.Classes, class) || strings.ContainsRune(seen, class) {
				return fmt.Errorf(
					"operation classes %q disagree with inventory classes %q",
					op.Classes,
					f.Classes,
				)
			}
			seen += string(class)
		}
		if err := validateSnippets(op.Steps); err != nil {
			return err
		}
	}
	for _, path := range r.VersionSources {
		if _, err := fingerprint(path, fingerprints); err != nil {
			return err
		}
	}
	for _, ref := range r.Evidence {
		data, err := fingerprint(ref.Path, fingerprints)
		if err != nil {
			return err
		}
		if len(ref.Contains) == 0 {
			return fmt.Errorf("%s: evidence requires source anchors", ref.Path)
		}
		for _, anchor := range ref.Contains {
			if strings.TrimSpace(anchor) == "" || !bytes.Contains(data, []byte(anchor)) {
				return fmt.Errorf("%s: missing evidence anchor %q", ref.Path, anchor)
			}
		}
	}
	return nil
}

func validateSnippets(snippets []snippet) error {
	for _, s := range snippets {
		if strings.TrimSpace(s.Code) == "" || s.Language == "" ||
			strings.ContainsAny(s.Language, " \t\r\n`") {
			return errors.New("snippets need a safe language identifier and code")
		}
		for _, secret := range []string{"admin:password", "password123", "{basicauth}", "{password}", "{admin}", "Authorization: Bearer eyJ"} {
			if strings.Contains(s.Code, secret) {
				return fmt.Errorf(
					"snippet contains test credential marker %q; use runtime credentials",
					secret,
				)
			}
		}
	}
	return nil
}

func fingerprint(path string, hashes map[string]string) ([]byte, error) {
	if !fs.ValidPath(path) {
		return nil, fmt.Errorf("evidence path must be repository-relative: %q", path)
	}
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		return nil, err
	}
	hashes[path] = fmt.Sprintf("%x", sha256.Sum256(data))
	return data, nil
}

func writeOrCheck(path string, data []byte, check bool) error {
	if !check {
		return os.WriteFile(path, data, 0o644)
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, existing) {
		return fmt.Errorf("%s is stale; review source changes and run make client-recipes", path)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
