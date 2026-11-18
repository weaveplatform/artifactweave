package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

type catalog struct {
	Schema      int    `json:"schema"`
	Reviewed    string `json:"reviewed"`
	Target      string `json:"target"`
	ClassSource string `json:"class_source"`
	Rows        []row  `json:"rows"`
}

type row struct {
	Type            string         `json:"type"`
	Catalog         bool           `json:"catalog"`
	P9c             bool           `json:"p9c"`
	Classes         string         `json:"classes"`
	Client          string         `json:"client"`
	Operations      string         `json:"operations"`
	Route           string         `json:"route"`
	RouteFile       string         `json:"route_file"`
	Metadata        string         `json:"metadata"`
	Lifecycle       string         `json:"lifecycle"`
	Status          string         `json:"status"`
	OwnerIssue      int            `json:"owner_issue"`
	OwnerPhase      string         `json:"owner_phase,omitempty"`
	Evidence        string         `json:"evidence"`
	Source          string         `json:"source"`
	Virtual         string         `json:"virtual"`
	VirtualEvidence []virtualProof `json:"virtual_evidence,omitempty"`
}

// virtualProof distinguishes implemented protocol behavior from client evidence.
type virtualProof struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Anchor string `json:"anchor"`
}

func main() {
	check := flag.Bool("check", false, "verify the committed matrix without writing it")
	inventory := flag.String(
		"inventory",
		"docs/implementation/package-format-inventory.json",
		"catalog JSON",
	)
	output := flag.String(
		"output",
		"docs/implementation/package-format-matrix.md",
		"generated Markdown",
	)
	flag.Parse()
	if err := run(*check, *inventory, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(check bool, inventory, output string) error {
	data, err := os.ReadFile(inventory)
	if err != nil {
		return err
	}
	var c catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	if err := validate(c); err != nil {
		return err
	}
	rendered := render(c)
	if !check {
		return os.WriteFile(output, rendered, 0o644)
	}
	existing, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, rendered) {
		return fmt.Errorf("%s is stale; run go run ./tools/formatinventory", output)
	}
	return nil
}

func validate(c catalog) error {
	if c.Schema != 1 || c.Reviewed == "" || c.Target == "" || c.ClassSource == "" {
		return errors.New("catalog schema, reviewed date, target and class source are required")
	}
	if len(c.Rows) != 49 {
		return fmt.Errorf("catalog has %d rows; expected 49 registered types", len(c.Rows))
	}
	registered := map[repo.PackageType]string{}
	for _, class := range []struct {
		class  repo.Class
		letter string
	}{{repo.Local, "L"}, {repo.Remote, "R"}, {repo.Virtual, "V"}, {repo.Federated, "F"}} {
		for _, typ := range repo.AllowedPackageTypes(class.class) {
			registered[typ] += class.letter
		}
	}
	seen := map[repo.PackageType]bool{}
	catalogCount, p9cCount := 0, 0
	for _, r := range c.Rows {
		typ := repo.PackageType(r.Type)
		if seen[typ] {
			return fmt.Errorf("duplicate format %q", typ)
		}
		seen[typ] = true
		classes, ok := registered[typ]
		if !ok {
			return fmt.Errorf("unregistered inventory format %q", typ)
		}
		if r.Classes != classes {
			return fmt.Errorf("%s class drift: catalog %q, registered %q", typ, r.Classes, classes)
		}
		if err := validateVirtual(r); err != nil {
			return err
		}
		if r.Client == "" || r.Operations == "" || r.Route == "" || r.RouteFile == "" ||
			r.Metadata == "" ||
			r.Lifecycle == "" {
			return fmt.Errorf(
				"%s has an unmapped client, operation, route, metadata or lifecycle",
				typ,
			)
		}
		if _, err := os.Stat(filepath.Clean(r.RouteFile)); err != nil {
			return fmt.Errorf("%s route owner %q: %w", typ, r.RouteFile, err)
		}
		source, err := url.Parse(r.Source)
		if err != nil || source.Scheme != "https" || source.Host == "" {
			return fmt.Errorf("%s has no valid versioned source", typ)
		}
		if r.Catalog {
			catalogCount++
		}
		if r.P9c {
			p9cCount++
			if r.OwnerIssue == 0 {
				return fmt.Errorf("%s P9c type must have an owning issue", typ)
			}
		}
		switch r.Status {
		case "gap":
			if !r.P9c || r.OwnerIssue == 0 || r.Evidence != "" {
				return fmt.Errorf("%s gap must have an issue and no claimed native evidence", typ)
			}
			for _, class := range r.Classes {
				if !strings.Contains(r.Operations, string(class)+":") {
					return fmt.Errorf("%s has no %c client-operation matrix entry", typ, class)
				}
			}
		case "native", "core":
			if r.Evidence == "" {
				return fmt.Errorf("%s claims native behavior without evidence", typ)
			}
			if _, err := os.Stat(filepath.Clean(r.Evidence)); err != nil {
				return fmt.Errorf("%s evidence %q: %w", typ, r.Evidence, err)
			}
		case "planned":
			if r.OwnerIssue == 0 && r.OwnerPhase == "" {
				return fmt.Errorf("%s planned type has no owner", typ)
			}
		default:
			return fmt.Errorf("%s has unknown status %q", typ, r.Status)
		}
	}
	for typ := range registered {
		if !seen[typ] {
			return fmt.Errorf("registered format %s is absent from catalog", typ)
		}
	}
	if catalogCount != 45 || p9cCount != 28 {
		return fmt.Errorf(
			"expected 45 guide types and 28 P9c assignments; got %d and %d",
			catalogCount,
			p9cCount,
		)
	}
	return nil
}

func validateVirtual(r row) error {
	switch r.Virtual {
	case "native merge", "item routes", "first member wins", "not supported":
	default:
		return fmt.Errorf("%s has unknown virtual mechanism %q", r.Type, r.Virtual)
	}
	if strings.Contains(r.Classes, "V") != (r.Virtual != "not supported") {
		return fmt.Errorf("%s virtual mechanism contradicts registered classes", r.Type)
	}
	if r.Virtual == "not supported" {
		if len(r.VirtualEvidence) != 0 || strings.Contains(r.Operations, "V:") {
			return fmt.Errorf("%s unsupported virtual claims evidence or operations", r.Type)
		}
		return nil
	}
	if len(r.VirtualEvidence) == 0 || !strings.Contains(r.Operations, "V:") {
		return fmt.Errorf("%s virtual operations and evidence are required", r.Type)
	}
	for _, proof := range r.VirtualEvidence {
		if err := validateVirtualProof(proof); err != nil {
			return fmt.Errorf("%s virtual evidence: %w", r.Type, err)
		}
	}
	return nil
}

func validateVirtualProof(p virtualProof) error {
	if p.Kind != "client" && p.Kind != "server" {
		return fmt.Errorf("unknown evidence kind %q", p.Kind)
	}
	if p.Anchor == "" || p.Path == "" {
		return errors.New("path and scenario/test anchor are required")
	}
	body, err := os.ReadFile(filepath.Clean(p.Path))
	if err != nil {
		return err
	}
	if strings.HasSuffix(p.Path, ".feature") {
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			for _, prefix := range []string{"Scenario: ", "Scenario Outline: "} {
				if strings.TrimPrefix(line, prefix) == p.Anchor && strings.HasPrefix(line, prefix) {
					return nil
				}
			}
		}
	} else if strings.HasSuffix(p.Path, "_test.go") && strings.HasPrefix(p.Anchor, "TestAcc") {
		file, err := parser.ParseFile(token.NewFileSet(), p.Path, body, 0)
		if err != nil {
			return fmt.Errorf("parse acceptance evidence: %w", err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == p.Anchor {
				return nil
			}
		}
	}
	return fmt.Errorf("missing acceptance scenario/test %q in %s", p.Anchor, p.Path)
}

func render(c catalog) []byte {
	rows := append([]row(nil), c.Rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Type < rows[j].Type })
	var b strings.Builder
	fmt.Fprintln(&b, "# Package format and client matrix")
	fmt.Fprintf(
		&b,
		"\nGenerated from [the versioned inventory](package-format-inventory.json), reviewed %s. Target: %s. Classes: L=local, R=remote, V=virtual, F=federated. The [JFrog class table](%s) omits F; F is the registered configuration contract and needs one-node native proof in each owned ticket.\n\n",
		c.Reviewed,
		c.Target,
		c.ClassSource,
	)
	fmt.Fprint(
		&b,
		"`gap` means repository configuration and generic item storage exist, while the native client protocol remains owned by the linked ticket. A registered class is not a claim of native client support. `core` links proven native paths with remaining format behavior named in metadata and owned by a follow-up; `native` links completed acceptance evidence.\n\n",
	)
	fmt.Fprint(
		&b,
		"[Source conflicts, target-version questions and architecture contracts](package-format-inventory-notes.md) explain the class boundaries behind this matrix.\n\n",
	)
	fmt.Fprint(
		&b,
		"Virtual resolution describes the implementation mechanism, independently of evidence strength. `client` links a retained native-client acceptance scenario; `server` links protocol acceptance only. Neither claims P10 sign-off or cross-peer federation. See [virtual operation rules and family limits](../operations/virtual-repositories.md).\n\n",
	)
	fmt.Fprintln(
		&b,
		"| Type | Guide catalog | Registered classes | Virtual resolution | Client | Client operations by class | Current route | Metadata | Lifecycle | State / owner | Evidence / source |",
	)
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range rows {
		catalog := "yes"
		if !r.Catalog {
			catalog = "extra"
		}
		state := r.Status
		if r.OwnerIssue != 0 {
			state += fmt.Sprintf(
				" [#%d](https://github.com/weaveplatform/artifactweave/issues/%d)",
				r.OwnerIssue,
				r.OwnerIssue,
			)
		}
		if r.OwnerPhase != "" {
			state += " " + r.OwnerPhase
		}
		evidence := "[source](" + r.Source + ")"
		if r.Evidence != "" {
			evidence = "[test](../../" + r.Evidence + "), " + evidence
		}
		virtual := r.Virtual
		for _, proof := range r.VirtualEvidence {
			label := strings.NewReplacer("<", "&lt;", ">", "&gt;", "|", "\\|").Replace(proof.Anchor)
			virtual += fmt.Sprintf(" [%s: %s](../../%s)", proof.Kind, label, proof.Path)
		}
		fmt.Fprintf(
			&b,
			"| `%s` | %s | %s | %s | %s | %s | `%s` ([owner](../../%s)) | %s | %s | %s | %s |\n",
			r.Type,
			catalog,
			r.Classes,
			virtual,
			r.Client,
			r.Operations,
			r.Route,
			r.RouteFile,
			r.Metadata,
			r.Lifecycle,
			state,
			evidence,
		)
	}
	return []byte(b.String())
}
