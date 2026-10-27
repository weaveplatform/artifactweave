package terraform_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/terraform"
)

// A module zip is <ns>/<name>/<system>/<version>.zip at exactly that depth,
// with Terraform's name rules, the case kept, and a semantic version
// spelled with or without a "v".
func TestUnitParseModule(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, tc := range []struct {
		path     string
		expected terraform.Module
		ok       bool
	}{
		{"acme/vpc/aws/1.0.0.zip", terraform.Module{
			Namespace: "acme", Name: "vpc", System: "aws", Version: "1.0.0",
		}, true},
		{"terraform-local__Acme_Corp/My-Module/aws/v0.1.2.zip", terraform.Module{
			Namespace: "terraform-local__Acme_Corp", Name: "My-Module", System: "aws",
			Version: "0.1.2", V: true,
		}, true},
		{"acme/vpc/aws/1.0.0-rc.1+build.5.zip", terraform.Module{
			Namespace: "acme", Name: "vpc", System: "aws", Version: "1.0.0-rc.1+build.5",
		}, true},
		{long + "/" + long + "/" + long + "/1.0.0.zip", terraform.Module{
			Namespace: long, Name: long, System: long, Version: "1.0.0",
		}, true},
		// Depth.
		{"vpc/aws/1.0.0.zip", terraform.Module{}, false},
		{"org/acme/vpc/aws/1.0.0.zip", terraform.Module{}, false},
		// Names: a leading or trailing "-" or "_", a ".", over 64.
		{"-acme/vpc/aws/1.0.0.zip", terraform.Module{}, false},
		{"acme/vpc_/aws/1.0.0.zip", terraform.Module{}, false},
		{"ac.me/vpc/aws/1.0.0.zip", terraform.Module{}, false},
		{long + "a/vpc/aws/1.0.0.zip", terraform.Module{}, false},
		// The system: lower-case letters and digits only.
		{"acme/vpc/AWS/1.0.0.zip", terraform.Module{}, false},
		{"acme/vpc/a-ws/1.0.0.zip", terraform.Module{}, false},
		// The version: semantic, no leading zero, at most one "v".
		{"acme/vpc/aws/1.0.zip", terraform.Module{}, false},
		{"acme/vpc/aws/01.0.0.zip", terraform.Module{}, false},
		{"acme/vpc/aws/vv1.0.0.zip", terraform.Module{}, false},
		{"acme/vpc/aws/V1.0.0.zip", terraform.Module{}, false},
		{"acme/vpc/aws/1.0.0.tar.gz", terraform.Module{}, false},
	} {
		got, ok := terraform.ParseModule(tc.path)
		if ok != tc.ok || got != tc.expected {
			t.Errorf("%s: %+v %v, want %+v %v", tc.path, got, ok, tc.expected, tc.ok)
			continue
		}
		if ok && got.Path() != tc.path {
			t.Errorf("%s: path %s", tc.path, got.Path())
		}
	}
	if m, _ := terraform.ParseModule("acme/vpc/aws/v1.0.0.zip"); m.Package() != "acme/vpc/aws" {
		t.Errorf("package %s", m.Package())
	}
}

// A file of a Terraform provider package's release is recognised by its
// name at any depth below its <ns>/<type>/<version> folders, whose type and
// version the name must repeat; the namespace and type are normalised as
// Terraform normalises an address.
func TestUnitParseProviderPackageFile(t *testing.T) {
	const at = "hashicorp/null/3.3.2/terraform-provider-null_3.3.2_"
	type file = terraform.ProviderPackageFile
	null := func(k terraform.Kind, platform ...string) file {
		f := file{Namespace: "hashicorp", Type: "null", Version: "3.3.2", Kind: k}
		if len(platform) == 2 {
			f.OS, f.Arch = platform[0], platform[1]
		}
		return f
	}
	for _, tc := range []struct {
		path     string
		expected file
		ok       bool
	}{
		{at + "linux_amd64.zip", null(terraform.KindArchive, "linux", "amd64"), true},
		{at + "SHA256SUMS", null(terraform.KindSums), true},
		{at + "SHA256SUMS.sig", null(terraform.KindSignature), true},
		{at + "manifest.json", null(terraform.KindManifest), true},
		// Folders before the namespace, as JFrog's deploy example adds.
		{"Terraform-local/terraform-main-provider/" + at + "SHA256SUMS", null(terraform.KindSums), true},
		{"a/b/c/" + at + "darwin_arm64.zip", null(terraform.KindArchive, "darwin", "arm64"), true},
		// Case: folders are normalised; the name's type need only match.
		{"HashiCorp/Null/3.3.2/terraform-provider-NULL_3.3.2_SHA256SUMS", null(terraform.KindSums), true},
		// A namespace may hold "_" and "-"; a type "-".
		{"my_org-1/aws-cc/1.0.0-beta.1/terraform-provider-aws-cc_1.0.0-beta.1_SHA256SUMS", file{
			Namespace: "my_org-1", Type: "aws-cc", Version: "1.0.0-beta.1", Kind: terraform.KindSums,
		}, true},
		// A non-ASCII namespace in the form Terraform sends.
		{"Münchner/null/1.0.0/terraform-provider-null_1.0.0_SHA256SUMS", file{
			Namespace: "münchner", Type: "null", Version: "1.0.0", Kind: terraform.KindSums,
		}, true},
		// Not a release file: HashiCorp's key-specific signature, another
		// suffix, an uppercase platform, a platform with three parts.
		{at + "SHA256SUMS.72D7468F.sig", file{}, false},
		{at + "README.md", file{}, false},
		{at + "Linux_amd64.zip", file{}, false},
		{at + "linux_amd64_v2.zip", file{}, false},
		{at + "linux.zip", file{}, false},
		{at + "linux_amd64.tar.gz", file{}, false},
		// The name's type or version differs from the folders'.
		{"hashicorp/null/3.3.2/terraform-provider-random_3.3.2_SHA256SUMS", file{}, false},
		{"hashicorp/null/3.3.2/terraform-provider-null_3.3.1_SHA256SUMS", file{}, false},
		{"hashicorp/null/3.3.2/terraform-provider-null_v3.3.2_SHA256SUMS", file{}, false},
		// The version folder is not a semantic version.
		{"hashicorp/null/v3.3.2/terraform-provider-null_v3.3.2_SHA256SUMS", file{}, false},
		// Too shallow, or not a release name.
		{"null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"hashicorp/null/3.3.2/null_3.3.2_SHA256SUMS", file{}, false},
		// Terraform's rules: no ".", no "--", no "_" in a type, no leading
		// or trailing "-" or "_", no other ASCII punctuation.
		{"hashi.corp/null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"hashi--corp/null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"_hashicorp/null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"hashicorp_/null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"-hashicorp/null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"hashi$corp/null/3.3.2/terraform-provider-null_3.3.2_SHA256SUMS", file{}, false},
		{"hashicorp/-null/3.3.2/terraform-provider--null_3.3.2_SHA256SUMS", file{}, false},
		{"hashicorp/nu--ll/3.3.2/terraform-provider-nu--ll_3.3.2_SHA256SUMS", file{}, false},
		{"hashicorp/null-/3.3.2/terraform-provider-null-_3.3.2_SHA256SUMS", file{}, false},
	} {
		got, ok := terraform.ParseProviderPackageFile(tc.path)
		if ok != tc.ok || got != tc.expected {
			t.Errorf("%s: %+v %v, want %+v %v", tc.path, got, ok, tc.expected, tc.ok)
		}
	}
	f, _ := terraform.ParseProviderPackageFile(at + "linux_amd64.zip")
	if f.Package() != "hashicorp/null" || f.Platform() != "linux_amd64" {
		t.Errorf("package %s, platform %s", f.Package(), f.Platform())
	}
}

// A request's namespace and type find the package whatever their case, as
// the folders do, and the names Terraform refuses find none.
func TestUnitProviderPackageName(t *testing.T) {
	for _, tc := range []struct {
		ns, typ, expected string
		ok                bool
	}{
		{"hashicorp", "null", "hashicorp/null", true},
		{"HashiCorp", "Null", "hashicorp/null", true},
		{"my_team", "null", "my_team/null", true},
		{"_team", "null", "", false},
		{"team", "nu_ll", "", false},
		{"te.am", "null", "", false},
	} {
		got, ok := terraform.ProviderPackageName(tc.ns, tc.typ)
		if got != tc.expected || ok != tc.ok {
			t.Errorf("%s/%s: %q %v, want %q %v", tc.ns, tc.typ, got, ok, tc.expected, tc.ok)
		}
	}
}
