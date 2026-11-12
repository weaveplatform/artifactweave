package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/devtools/specoracle"
)

// Indexes are the llms.txt files of every API family in scope (research 01).
// Families that are stubbed or ignored are still fetched so the baseline
// records them as missing explicitly rather than by omission.
//
// Since 2026-11-12 two of these answer 404, repositories-api and
// jfrog-artifactory-builds-api: JFrog renamed both families, to
// jfrog-artifactory-repositories-api and jfrog-artifactory-build-api. Fetch
// takes their pages from the parent index (#1308). Moving to the new URLs
// would also rename the families' slugs, which api/oracle-inventory.json and
// research 14 use, so it belongs with a refresh of the inventory.
var Indexes = []string{
	"https://docs.jfrog.com/artifactory/reference/artifacts-storage-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/package-type-specific-apis/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/repositories-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/federated-repositories-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/repository-replication-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/searches-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/trash-cangarbage-collection-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/release-bundles-v1-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/retention-release-bundle-v1-api/llms.txt",
	"https://docs.jfrog.com/artifactory/reference/jfrog-distribution-api/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-platform-rest-apis/llms.txt",
	"https://docs.jfrog.com/administration/reference/storage-and-archive/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-artifactory-security-api/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-artifactory-loggers-api/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-artifactory-import-export-api/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-platform-artifactory-system-api/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-platform-aws-iam-api/llms.txt",
	"https://docs.jfrog.com/administration/reference/jfrog-platform-access-federation-api/llms.txt",
	"https://docs.jfrog.com/projects/reference/jfrog-platform-projects-api/llms.txt",
	"https://docs.jfrog.com/integrations/reference/jfrog-platform-webhooks-api/llms.txt",
	"https://docs.jfrog.com/integrations/reference/jfrog-artifactory-live-logs-api/llms.txt",
	// The Builds API (build-info publish, promotion, retention, deletion).
	// docs.jfrog.com also lists the same pages under jfrog-artifactory-build-api,
	// with "-1" slugs until 2026-11-12; fetching both would only duplicate
	// every operation.
	"https://docs.jfrog.com/integrations/reference/jfrog-artifactory-builds-api/llms.txt",
}

// BaselineFile is the committed list of accepted gaps, relative to -api.
const BaselineFile = "parity-baseline.json"

// InventoryFile is the committed fact inventory, relative to -api. fetch also
// reads it for the pages each family had, which pick the family's section of
// the parent index when the family's own index fails.
const InventoryFile = "oracle-inventory.json"

// serviceBases maps each service under srcDir (one directory per service,
// each with an info.yaml) to the prefix its servers[0].url declares, so adding
// a service is a new directory, not a code change; scaffolded path keys are
// relative to that prefix.
func serviceBases(srcDir string) (map[string]string, error) {
	infos, err := filepath.Glob(filepath.Join(srcDir, "*", "info.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	bases := make(map[string]string, len(infos))
	for _, info := range infos {
		raw, err := os.ReadFile(info)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", info, err)
		}
		var doc struct {
			Servers []struct {
				URL string `yaml:"url"`
			} `yaml:"servers"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", info, err)
		}
		if len(doc.Servers) == 0 || doc.Servers[0].URL == "" {
			return nil, fmt.Errorf("%w: %s", errNoServer, info)
		}
		bases[filepath.Base(filepath.Dir(info))] = doc.Servers[0].URL
	}
	return bases, nil
}

// specFiles lists the bundled specs in apiDir, sorted for stable reports.
func specFiles(apiDir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(apiDir, "*.openapi.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list specs: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

var (
	errUsage = errors.New(
		"usage: specoracle fetch|report|baseline|inventory|scaffold [flags]",
	)
	errUnknownService  = errors.New("scaffold: -service must name a directory under -src")
	errNoServer        = errors.New("info.yaml declares no servers[0].url")
	errScaffoldOut     = errors.New("scaffold: -out is required")
	errBaselineUpdate  = errors.New("baseline: pass -update to rewrite")
	errIncompleteCache = errors.New(
		"the oracle cache lacks an API family; run make oracle-fetch again",
	)
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "specoracle:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	cache := fs.String("cache", ".specoracle", "cache directory (gitignored)")
	api := fs.String("api", "api", "directory holding the authored specs and the baseline")
	src := fs.String("src", "api/src", "directory holding one sub-directory per service (scaffold)")
	update := fs.Bool("update", false, "baseline: rewrite the file from the current report")
	family := fs.String("family", "", "scaffold: cache family (index slug) to scaffold")
	service := fs.String("service", "", "scaffold: artifactory, access or event")
	out := fs.String("out", "", "scaffold: output fragment path; inventory: markdown path")
	skip := fs.String(
		"skip-schemas",
		"ErrorResponse",
		"scaffold: comma-separated schema names the service already defines",
	)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "inventory":
		// The inventory is committed and fetch reads it for the pages each
		// family had, so one written without a family would also take that
		// family's fallback away.
		if err := specoracle.CheckFamilies(*cache); err != nil {
			return fmt.Errorf("%w\n%w", errIncompleteCache, err)
		}
		inv, err := specoracle.BuildInventory(*cache)
		if err != nil {
			return err
		}
		jsonPath := filepath.Join(*api, InventoryFile)
		if err := inv.WriteJSON(jsonPath); err != nil {
			return err
		}
		md := *out
		if md == "" {
			md = filepath.Join("docs", "research", "14-data-model-inventory.md")
		}
		if err := inv.WriteMarkdown(md); err != nil {
			return err
		}
		fmt.Printf("inventory: %d pages, %d operations, %d schemas -> %s, %s\n",
			inv.Pages, len(inv.Operations), len(inv.Schemas), jsonPath, md)
		return nil
	case "scaffold":
		bases, err := serviceBases(*src)
		if err != nil {
			return err
		}
		base, ok := bases[*service]
		if !ok {
			return fmt.Errorf("%w: %q", errUnknownService, *service)
		}
		if *out == "" {
			return errScaffoldOut
		}
		res, err := specoracle.Scaffold(specoracle.ScaffoldOptions{
			CacheDir:    *cache,
			Family:      *family,
			ServiceBase: base,
			SkipSchemas: strings.Split(*skip, ","),
		})
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, res.YAML, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", *out, err)
		}
		fmt.Printf("scaffold: %d operations, %d schemas -> %s\n", res.Operations, res.Schemas, *out)
		for from, to := range res.Renamed {
			fmt.Printf("  renamed %s -> %s (shape differs elsewhere)\n", from, to)
		}
		for _, s := range res.Skipped {
			fmt.Printf("  skipped (outside %s): %s\n", base, s)
		}
		return nil
	case "fetch":
		known, err := specoracle.KnownPages(filepath.Join(*api, InventoryFile))
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		m, err := specoracle.Fetch(ctx, specoracle.FetchOptions{
			Indexes:  Indexes,
			CacheDir: *cache,
			Known:    known,
		})
		if err != nil {
			return err
		}
		failed := 0
		for _, fam := range m.Families {
			if fam.Error == "" {
				continue
			}
			failed++
			if fam.Source != "" {
				fmt.Fprintf(os.Stderr, "  %s: %s; took its pages from %q in %s\n",
					fam.Family, fam.Error, fam.Section, fam.Source)
				continue
			}
			fmt.Fprintf(os.Stderr, "  %s: %s\n", fam.Family, fam.Error)
		}
		for _, e := range m.Entries {
			if e.Error != "" {
				failed++
				fmt.Fprintf(os.Stderr, "  %s: %s\n", e.URL, e.Error)
			}
		}
		fmt.Printf(
			"fetched %d pages, %d OpenAPI blocks, %d failures, cache %s\n",
			len(m.Entries),
			m.Blocks(),
			failed,
			*cache,
		)
		// The cache is kept so it can be looked at, and the parity test
		// fails on it, naming the same families.
		if err := specoracle.CheckFamilies(*cache); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		}
		return nil
	case "report", "baseline":
		families := specoracle.CheckFamilies(*cache)
		report, err := compare(*cache, *api)
		if err != nil {
			return err
		}
		if args[0] == "report" {
			if families != nil {
				fmt.Fprintf(os.Stderr, "%v\n%v\n", errIncompleteCache, families)
			}
			fmt.Print(report.String())
			return nil
		}
		path := filepath.Join(*api, BaselineFile)
		if !*update {
			return fmt.Errorf("%w: %s", errBaselineUpdate, path)
		}
		// Rewriting the baseline from such a cache would delete every gap
		// it records for the missing family.
		if families != nil {
			return fmt.Errorf("%w\n%w", errIncompleteCache, families)
		}
		if err := report.WriteBaseline(path); err != nil {
			return err
		}
		fmt.Printf(
			"wrote %s: %d missing, %d divergent\n",
			path,
			len(report.Missing),
			len(report.Divergent),
		)
		return nil
	default:
		return fmt.Errorf("%w: %q", errUsage, args[0])
	}
}

func compare(cache, api string) (specoracle.Report, error) {
	theirs, err := specoracle.Load(cache)
	if err != nil {
		return specoracle.Report{}, fmt.Errorf(
			"load cache %s (run specoracle fetch first): %w",
			cache,
			err,
		)
	}
	paths, err := specFiles(api)
	if err != nil {
		return specoracle.Report{}, err
	}
	ours, err := specoracle.LoadOurs(paths...)
	if err != nil {
		return specoracle.Report{}, err
	}
	return specoracle.Compare(theirs, ours), nil
}
