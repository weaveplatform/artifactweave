package main

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// formatImpact names the native scenarios and client inputs used by a format.
// A profile is proposed only for edits inside one existing format-owned Go
// package. The workflow keeps running the full gate until #673 activates the
// affected profile with its coverage and required-check policy.
type formatImpact struct {
	unitRoot string
	tags     string
	clients  []string
}

var formats = map[string]formatImpact{
	"machinelearning": {
		"./internal/format/machinelearning/...",
		"@machinelearning",
		[]string{"frogml"},
	},
	"aieditorextensions": {
		"./internal/format/aieditorextensions/...",
		"@aieditorextensions",
		[]string{"vscode"},
	},
	"chef":    {"./migration/artifacts/ecosystem/chef/...", "@chef", []string{"chef"}},
	"ansible": {"./internal/format/ansible/...", "@ansible", []string{"ansible"}},
	"alpine":  {"./migration/artifacts/ecosystem/alpine/...", "@alpine", []string{"alpine"}},
	"bazelmodules": {
		"./migration/artifacts/ecosystem/bazelmodules/...",
		"@bazelmodules",
		[]string{"bazel"},
	},
	"cargo":         {"./migration/artifacts/ecosystem/cargo/...", "@cargo", []string{"cargo"}},
	"conan":         {"./migration/artifacts/ecosystem/conan/...", "@conan", []string{"conan"}},
	"rpm":           {"./migration/artifacts/ecosystem/rpm/...", "@rpm", []string{"fedora"}},
	"opkg":          {"./migration/artifacts/ecosystem/opkg/...", "@opkg", []string{"opkg"}},
	"p2":            {"./migration/artifacts/ecosystem/p2/...", "@p2", []string{"p2"}},
	"conda":         {"./migration/artifacts/ecosystem/conda/...", "@conda", []string{"conda"}},
	"cran":          {"./migration/artifacts/ecosystem/cran/...", "@cran", []string{"r"}},
	"nix":           {"./migration/artifacts/ecosystem/nix/...", "@nix", []string{"nix"}},
	"puppet":        {"./migration/artifacts/ecosystem/puppet/...", "@puppet", []string{"puppet"}},
	"pub":           {"./migration/artifacts/ecosystem/pub/...", "@pub", []string{"dart"}},
	"agentpackages": {"./migration/artifacts/ecosystem/agentpackages/...", "@apm", []string{"apm"}},
	"bower":         {"./migration/artifacts/ecosystem/bower/...", "@bower", []string{"bower"}},
	"debian":        {"./migration/artifacts/ecosystem/debian/...", "@debian", []string{"debian"}},
	"gems":          {"./migration/artifacts/ecosystem/gems/...", "@gems", []string{"ruby"}},
	"gitlfs":        {"./internal/format/gitlfs/...", "@gitlfs", []string{"gitlfs"}},
	"cocoapods": {
		"./migration/artifacts/ecosystem/cocoapods/...",
		"@cocoapods",
		[]string{"cocoapods"},
	},
	"vagrant": {"./migration/artifacts/ecosystem/vagrant/...", "@vagrant", []string{"vagrant"}},
	"agentplugins": {
		"./migration/artifacts/ecosystem/agentplugins/...",
		"@agentplugins,@agentplugins_clients",
		[]string{"jf"},
	},
	"docker": {
		"./migration/artifacts/ecosystem/docker/...",
		"@clients,@nimmodel",
		[]string{"crane", "oras", "cosign", "helm", "jf"},
	},
	"golang": {"./migration/artifacts/ecosystem/golang/...", "@goproxy,@go,@package_wiring", nil},
	"helm": {
		"./migration/artifacts/ecosystem/helm/...",
		"@helm,@package_wiring",
		[]string{"helm", "jf"},
	},
	"helmoci": {"", "@clients&&@helm", []string{"helm", "jf"}},
	"hex":     {"./migration/artifacts/ecosystem/hex/...", "@hex", []string{"hex"}},
	"jetbrainsplugins": {
		"./migration/artifacts/ecosystem/jetbrainsplugins/http/...",
		"@jetbrainsplugins",
		[]string{"jetbrains"},
	},
	"huggingface": {"./migration/artifacts/ecosystem/huggingface/...", "@huggingface", nil},
	"luarocks":    {"./internal/format/luarocks/...", "@luarocks", []string{"luarocks", "jf"}},
	"composer": {
		"./migration/artifacts/ecosystem/composer/...",
		"@composer",
		[]string{"composer"},
	},
	"maven": {
		"./migration/artifacts/ecosystem/maven/...",
		"@maven&&~@gradle,@package_wiring&&~@gradle",
		[]string{"maven", "maven39", "sbt", "jf"},
	},
	"nimmodel": {"", "@nimmodel", []string{"crane"}},
	"npm": {
		"./migration/artifacts/ecosystem/npm/...",
		"@npm,@package_wiring",
		[]string{"node", "npm12", "jf"},
	},
	"nuget": {
		"./migration/artifacts/ecosystem/nuget/...",
		"@nuget&&~@gradle",
		[]string{"dotnet", "dotnet11", "jf"},
	},
	"pypi": {
		"./migration/artifacts/ecosystem/pypi/...",
		"@pypi,@package_wiring",
		[]string{"python", "jf"},
	},
	"skills": {"./internal/format/skills/...", "@skills", []string{"jf"}},
	"swift":  {"./migration/artifacts/ecosystem/swift/...", "@swift", []string{"swift"}},
	"terraform": {
		"./migration/artifacts/ecosystem/terraform/...",
		"@terraform&&~@terraform_backend&&~@terraform_backend_api,@package_wiring",
		[]string{"terraform", "jf"},
	},
	"terraformbackend": {
		"./migration/artifacts/ecosystem/terraformbackend/...",
		"@terraform_backend,@terraform_backend_api",
		[]string{"terraform"},
	},
	"vcs": {"./migration/artifacts/ecosystem/vcs/http/...", "@vcs", []string{"maven"}},
}

type affectedPlan struct {
	profile                       string
	owner                         string
	reason                        string
	units                         []string
	tags                          string
	clients                       []string
	hasSharedTests                bool
	coverpkg                      string
	changedPackages               []string
	shouldTestTerraformProviders  bool
	shouldTestTerraformDeployment bool
}

func proposeAffected(changes []change) affectedPlan {
	if len(changes) == 0 {
		return affectedPlan{profile: "full", reason: "Empty diff."}
	}
	var owner string
	var provider, deployment bool
	for _, c := range changes {
		if c.status == "" || !strings.ContainsRune("AMDRC", rune(c.status[0])) {
			return affectedPlan{profile: "full", reason: "Unknown change type."}
		}
		if c.old != "" || c.status[0] == 'D' || c.status[0] == 'R' || c.status[0] == 'C' {
			return affectedPlan{
				profile: "full",
				reason:  "A deletion, rename or copy needs full qualification.",
			}
		}
		if documentationPath(c.path) {
			continue
		}
		if isServerTest(c.path) && c.status[0] == 'A' {
			return affectedPlan{
				profile: "full",
				reason:  "A new server test needs ownership review.",
			}
		}
		if isDeploymentPath(c.path) {
			if owner != "" {
				return affectedPlan{
					profile: "full",
					reason:  "Format and deployment changes are combined.",
				}
			}
			providerPath, deploymentPath := terraformPathLanes(c.path)
			provider = provider || providerPath
			deployment = deployment || deploymentPath
			continue
		}
		this, ok := formatSourceOwner(c.path)
		if !ok {
			return affectedPlan{
				profile: "full",
				reason:  fmt.Sprintf("Shared or unmapped path %q.", c.path),
			}
		}
		if provider || deployment || (owner != "" && owner != this) {
			return affectedPlan{profile: "full", reason: "More than one code owner changed."}
		}
		owner = this
	}
	if provider || deployment {
		return affectedPlan{
			profile:                       "deployment",
			shouldTestTerraformProviders:  provider,
			shouldTestTerraformDeployment: deployment,
			reason:                        "Only mapped Terraform inputs and documentation changed.",
		}
	}
	if owner == "" {
		return affectedPlan{profile: "docs", reason: "Only documentation changed."}
	}
	conf := formats[owner]
	inventory, err := loadTestOwnership()
	if err != nil {
		return affectedPlan{profile: "full", reason: "Server test ownership inventory is invalid."}
	}
	units := []string{}
	for pkg, pkgOwner := range inventory.Packages {
		if pkgOwner == owner {
			units = append(units, "./"+pkg)
		}
	}
	if conf.unitRoot != "" {
		units = append(units, conf.unitRoot)
	}
	// scanKinds keeps only the listed files of these shared directories.
	deps := inventory.FormatDependencies[owner]
	for _, dep := range deps {
		units = append(units, "./"+path.Dir(dep))
	}
	if owner == "docker" {
		// Helm OCI and NIM model exercise the same registry protocol stack.
		units = append(
			units,
			"./test/server/artifacts/helmoci",
			"./migration/artifacts/ecosystem/nimmodel/http",
		)
	}
	slices.Sort(units)
	units = slices.Compact(units)
	coverPackages := ownerCoverPackages(owner, conf, inventory)
	// Only mapped format paths reach this point, so a production file outside
	// test/ is format code wherever the layout puts it; test/ support code has
	// no changed-package coverage floor.
	changedPackages := []string{}
	for _, c := range changes {
		if strings.HasSuffix(c.path, ".go") && !strings.HasSuffix(c.path, "_test.go") &&
			!strings.HasPrefix(c.path, "test/") {
			changedPackages = append(changedPackages, modulePath+"/"+path.Dir(c.path))
		}
	}
	slices.Sort(changedPackages)
	changedPackages = slices.Compact(changedPackages)
	// Some production packages (for example agentpackages' server adapter)
	// have no independent package tests in the ownership inventory. They
	// still need instrumentation and an explicit changed-package floor.
	coverPackages = append(coverPackages, changedPackages...)
	slices.Sort(coverPackages)
	coverPackages = slices.Compact(coverPackages)
	return affectedPlan{
		profile: "format", owner: owner, reason: "One format-owned source area changed.",
		units: units, tags: conf.tags, clients: conf.clients, hasSharedTests: len(deps) > 0,
		coverpkg: strings.Join(coverPackages, ","), changedPackages: changedPackages,
	}
}

// ownerCoverPackages derives the instrumented packages from the unit root and
// the inventory, so the list follows a package move once the formats map and
// the inventory are rewritten. A list built from the owner's name under fixed
// prefixes would miss moved packages and would name internal/format/<owner>
// for owners whose code lives elsewhere (vcs, jetbrainsplugins).
func ownerCoverPackages(owner string, conf formatImpact, inventory testOwnership) []string {
	var coverPackages []string
	if conf.unitRoot != "" {
		coverPackages = append(coverPackages, modulePath+strings.TrimPrefix(conf.unitRoot, "."))
	}
	for dir, dirOwner := range inventory.Packages {
		if dirOwner == owner && !strings.HasPrefix(dir, "test/") {
			coverPackages = append(coverPackages, modulePath+"/"+dir+"/...")
		}
	}
	if len(coverPackages) == 0 {
		// Test-only owners still need a stable instrumented package set.
		coverPackages = append(
			coverPackages,
			modulePath+"/migration/artifacts/ecosystem/docker/http/...",
		)
	}
	slices.Sort(coverPackages)
	return slices.Compact(coverPackages)
}

func formatSourceOwner(file string) (string, bool) {
	if !strings.HasSuffix(file, ".go") {
		return "", false
	}
	parts := strings.Split(file, "/")
	if len(parts) < 4 || parts[0] == "" {
		return "", false
	}
	var owner string
	// The staged ecosystem cases accept both roots because migration/ is
	// renamed to internal/ at promotion. Each needs a fifth part so that the
	// owner is a directory: test/server/artifacts/x_test.go is shared.
	switch {
	case len(parts) >= 5 && (parts[0] == "migration" || parts[0] == "internal") &&
		parts[1] == "artifacts" && parts[2] == "ecosystem":
		owner = parts[3]
	case len(parts) >= 5 && parts[0] == "test" && parts[1] == "server" && parts[2] == "artifacts":
		owner = parts[3]
	case parts[0] == "internal" && parts[1] == "format":
		owner = parts[2]
	case parts[0] == "internal" && parts[1] == "server":
		owner = parts[2]
	case parts[0] == "test" && parts[1] == "server":
		owner = parts[2]
	default:
		return "", false
	}
	if _, ok := formats[owner]; !ok {
		return "", false
	}
	// A test package's reviewed owner must agree with its path: the narrow
	// plan's units are the owner's packages, so a test package recorded under
	// another owner (or shared) would change without being run.
	if isServerTest(file) {
		if packageOwner, ok := serverTestOwner(file); !ok || packageOwner != owner {
			return "", false
		}
	}
	return owner, true
}

func isDeploymentPath(file string) bool {
	return strings.HasPrefix(file, "deploy/") || strings.HasPrefix(file, "test/terraform/")
}

// A provider fixture only proves the pinned JFrog provider apply, empty-plan
// and destroy contract. Deployment roots and their mocked terraform tests are
// a separate lane. Unknown deploy tooling can affect both, so it selects both.
func terraformPathLanes(file string) (provider, deployment bool) {
	switch {
	case strings.HasPrefix(file, "deploy/artifactweave/terraform/"),
		strings.HasPrefix(file, "test/terraform/"),
		strings.HasPrefix(file, "deploy/ci/jfrog-"),
		strings.HasPrefix(file, "deploy/ci/testdata/jfrog-providers/"):
		return true, false
	case strings.HasPrefix(file, "deploy/platform/"),
		strings.HasPrefix(file, "deploy/ci/terraform/"):
		return false, true
	default:
		return true, true
	}
}
