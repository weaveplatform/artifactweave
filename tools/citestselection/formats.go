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
	"chef":          {"./internal/format/chef/...", "@chef", []string{"chef"}},
	"ansible":       {"./internal/format/ansible/...", "@ansible", []string{"ansible"}},
	"alpine":        {"./internal/format/alpine/...", "@alpine", []string{"alpine"}},
	"bazelmodules":  {"./internal/format/bazelmodules/...", "@bazelmodules", []string{"bazel"}},
	"cargo":         {"./internal/format/cargo/...", "@cargo", []string{"cargo"}},
	"conan":         {"./internal/format/conan/...", "@conan", []string{"conan"}},
	"rpm":           {"./internal/format/rpm/...", "@rpm", []string{"fedora"}},
	"opkg":          {"./internal/format/opkg/...", "@opkg", []string{"opkg"}},
	"p2":            {"./internal/format/p2/...", "@p2", []string{"p2"}},
	"conda":         {"./internal/format/conda/...", "@conda", []string{"conda"}},
	"cran":          {"./internal/format/cran/...", "@cran", []string{"r"}},
	"nix":           {"./internal/format/nix/...", "@nix", []string{"nix"}},
	"puppet":        {"./internal/format/puppet/...", "@puppet", []string{"puppet"}},
	"pub":           {"./internal/format/pub/...", "@pub", []string{"dart"}},
	"agentpackages": {"./internal/format/agentpackages/...", "@apm", []string{"apm"}},
	"bower":         {"./internal/format/bower/...", "@bower", []string{"bower"}},
	"debian":        {"./internal/format/debian/...", "@debian", []string{"debian"}},
	"gems":          {"./internal/format/gems/...", "@gems", []string{"ruby"}},
	"gitlfs":        {"./internal/format/gitlfs/...", "@gitlfs", []string{"gitlfs"}},
	"cocoapods":     {"./internal/format/cocoapods/...", "@cocoapods", []string{"cocoapods"}},
	"vagrant":       {"./internal/format/vagrant/...", "@vagrant", []string{"vagrant"}},
	"agentplugins": {
		"./internal/format/agentplugins/...",
		"@agentplugins,@agentplugins_clients",
		[]string{"jf"},
	},
	"docker": {
		"./internal/format/docker/...",
		"@clients,@nimmodel",
		[]string{"crane", "oras", "cosign", "helm", "jf"},
	},
	"golang": {"./internal/format/golang/...", "@goproxy,@go,@package_wiring", nil},
	"helm": {
		"./internal/format/helm/...",
		"@helm,@package_wiring",
		[]string{"helm", "jf"},
	},
	"helmoci": {"", "@clients&&@helm", []string{"helm", "jf"}},
	"hex":     {"./internal/format/hex/...", "@hex", []string{"hex"}},
	"jetbrainsplugins": {
		"./internal/server/jetbrainsplugins/...",
		"@jetbrainsplugins",
		[]string{"jetbrains"},
	},
	"huggingface": {"./internal/format/huggingface/...", "@huggingface", nil},
	"luarocks":    {"./internal/format/luarocks/...", "@luarocks", []string{"luarocks", "jf"}},
	"composer":    {"./internal/format/composer/...", "@composer", []string{"composer"}},
	"maven": {
		"./internal/format/maven/...",
		"@maven&&~@gradle,@package_wiring&&~@gradle",
		[]string{"maven", "maven39", "jf"},
	},
	"nimmodel": {"", "@nimmodel", []string{"crane"}},
	"npm": {
		"./internal/format/npm/...",
		"@npm,@package_wiring",
		[]string{"node", "npm12", "jf"},
	},
	"nuget": {
		"./internal/format/nuget/...",
		"@nuget&&~@gradle",
		[]string{"dotnet", "dotnet11", "jf"},
	},
	"pypi": {
		"./internal/format/pypi/...",
		"@pypi,@package_wiring",
		[]string{"python", "jf"},
	},
	"skills": {"./internal/format/skills/...", "@skills", []string{"jf"}},
	"swift":  {"./internal/format/swift/...", "@swift", []string{"swift"}},
	"terraform": {
		"./internal/format/terraform/...",
		"@terraform&&~@terraform_backend&&~@terraform_backend_api,@package_wiring",
		[]string{"terraform", "jf"},
	},
	"terraformbackend": {
		"./internal/format/terraformbackend/...",
		"@terraform_backend,@terraform_backend_api",
		[]string{"terraform"},
	},
	"vcs": {"./internal/server/vcs/...", "@vcs", nil},
}

type affectedPlan struct {
	profile                       string
	owner                         string
	reason                        string
	units                         []string
	tags                          string
	clients                       []string
	hasRootTests                  bool
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
	rootTest := len(inventory.RootFormatDependencies[owner]) != 0
	if rootTest {
		units = append(units, "./internal/server")
	}
	if len(inventory.AppFormatDependencies[owner]) != 0 {
		units = append(units, "./internal/app")
	}
	if owner == "docker" {
		// Helm OCI and NIM model exercise the same registry protocol stack.
		units = append(units, "./test/server/helmoci", "./internal/server/nimmodel")
	}
	slices.Sort(units)
	units = slices.Compact(units)
	var coverPackages []string
	if conf.unitRoot != "" {
		coverPackages = append(
			coverPackages,
			"github.com/weaveplatform/artifactweave/internal/format/"+owner+"/...",
		)
	}
	if _, ok := inventory.Packages["internal/server/"+owner]; ok {
		coverPackages = append(
			coverPackages,
			"github.com/weaveplatform/artifactweave/internal/server/"+owner+"/...",
		)
	}
	if len(coverPackages) == 0 {
		// Test-only owners still need a stable instrumented package set.
		coverPackages = append(
			coverPackages,
			"github.com/weaveplatform/artifactweave/internal/server/docker/...",
		)
	}
	changedPackages := []string{}
	for _, c := range changes {
		if strings.HasSuffix(c.path, ".go") && !strings.HasSuffix(c.path, "_test.go") &&
			(strings.HasPrefix(c.path, "internal/format/") || strings.HasPrefix(c.path, "internal/server/")) {
			changedPackages = append(
				changedPackages,
				"github.com/weaveplatform/artifactweave/"+path.Dir(c.path),
			)
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
		units: units, tags: conf.tags, clients: conf.clients, hasRootTests: rootTest,
		coverpkg: strings.Join(coverPackages, ","), changedPackages: changedPackages,
	}
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
	switch {
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
	if isServerTest(file) {
		if _, ok := serverTestOwner(file); !ok {
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
