package repo_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// keyError returns the message of the key's error, or "" when the key passed.
func keyError(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var errs repo.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("error is not an Errors list: %T", err)
	}
	for _, e := range errs {
		if e.Field == "key" {
			return e.Message
		}
	}
	return ""
}

func TestUnitValidateKeyRules(t *testing.T) {
	config := func(key string, class repo.Class, pt repo.PackageType) repo.Config {
		c := repo.Config{Key: key, Class: class, PackageType: pt}
		switch class {
		case repo.Remote:
			c.URL = "https://up.example"
		case repo.Virtual:
			c.Repositories = []string{"a"}
		}
		return c
	}
	n := func(length int) string { return strings.Repeat("k", length) }
	tests := []struct {
		name  string
		c     repo.Config
		error string
	}{
		{"64 on local", config(n(64), repo.Local, repo.Generic), ""},
		{"65 on local", config(n(65), repo.Local, repo.Generic), "at most 64 characters"},
		{"64 on virtual", config(n(64), repo.Virtual, repo.Generic), ""},
		{"65 on virtual", config(n(65), repo.Virtual, repo.Generic), "at most 64 characters"},
		{"64 on federated", config(n(64), repo.Federated, repo.Generic), ""},
		{"65 on federated", config(n(65), repo.Federated, repo.Generic), "at most 64 characters"},
		{"58 on remote", config(n(58), repo.Remote, repo.Generic), ""},
		{
			"59 on remote",
			config(n(59), repo.Remote, repo.Generic),
			"at most 58 characters in a remote",
		},
		{"59 on docker remote", config(n(59), repo.Remote, repo.Docker), "at most 58 characters"},
		{"63 on docker local", config(n(63), repo.Local, repo.Docker), ""},
		{
			"64 on docker local",
			config(n(64), repo.Local, repo.Docker),
			"at most 63 characters in a local docker",
		},
		{"64 on oci federated", config(n(64), repo.Federated, repo.OCI), "at most 63 characters"},
		{"64 on helmoci local", config(n(64), repo.Local, repo.HelmOCI), "at most 63 characters"},
		{"64 on docker virtual", config(n(64), repo.Virtual, repo.Docker), ""},
		{"counts characters", config(strings.Repeat("é", 64), repo.Local, repo.Generic), ""},
		{"no class", config(n(65), "", ""), "at most 64 characters"},

		{"leading digit", config("1abc", repo.Local, repo.Generic), ""},
		{"dots, hyphens, underscores", config("a.b-c_d", repo.Local, repo.Generic), ""},
		{"space", config("my repo", repo.Local, repo.Generic), `must not contain ' '`},
		{"tab", config("my\trepo", repo.Local, repo.Generic), `must not contain '\t'`},
		{"backslash", config(`a\b`, repo.Local, repo.Generic), `must not contain '\\'`},
		{"double quote", config(`a"b`, repo.Local, repo.Generic), `must not contain '"'`},
		{"non-ASCII", config("dépôt", repo.Local, repo.Generic), ""},

		{
			"reserved",
			config("release-bundles", repo.Local, repo.Generic),
			`"release-bundles" is a reserved`,
		},
		{"reserved dot", config(".", repo.Local, repo.Generic), "reserved repository key"},
		{"reserved dot dot", config("..", repo.Local, repo.Generic), "reserved repository key"},
		{
			"reserved in another case",
			config("Auto-Trashcan", repo.Local, repo.Generic),
			"reserved repository key",
		},
		{
			"reserved build info",
			config("ARTIFACTORY-BUILD-INFO", repo.Local, repo.Generic),
			"reserved",
		},
		{"reserved intransit", config("_intransit", repo.Local, repo.Generic), "reserved"},
		{"reserved name inside a key", config("my-release-bundles", repo.Local, repo.Generic), ""},
		{
			"reserved prefix", config("jfrog-system-reserved-x", repo.Local, repo.Generic),
			"must not start with jfrog-system-reserved",
		},
		{
			"reserved prefix in another case",
			config("Jfrog-Artifactory-System1", repo.Local, repo.Generic),
			"must not start with jfrog-artifactory-system",
		},
		{"cache suffix", config("maven-cache", repo.Local, repo.Generic), "-cache"},

		{"docker lower case", config("docker-local", repo.Local, repo.Docker), ""},
		{
			"docker upper case", config("Docker-Local", repo.Local, repo.Docker),
			"must be lower case without an underscore for package type docker",
		},
		{"docker underscore", config("docker_local", repo.Remote, repo.Docker), "underscore"},
		{"oci upper case", config("OCI", repo.Virtual, repo.OCI), "for package type oci"},
		{"helmoci underscore", config("helm_oci", repo.Federated, repo.HelmOCI), "underscore"},
		{
			"generic upper case and underscore",
			config("Generic_Local", repo.Local, repo.Generic),
			"",
		},
	}
	for _, c := range []rune(`!@#$%^&*()+={}[]:;<>,/?~` + "`" + `|`) {
		tests = append(tests, struct {
			name  string
			c     repo.Config
			error string
		}{"character " + string(c), config("a"+string(c)+"b", repo.Local, repo.Generic), "must not contain"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keyError(t, repo.Validate(&tt.c))
			switch {
			case tt.error == "" && got != "":
				t.Errorf("refused: %s", got)
			case tt.error != "" && !strings.Contains(got, tt.error):
				t.Errorf("got %q, want it to contain %q", got, tt.error)
			}
		})
	}
}

// A stored key is not checked again: the server creates repositories under
// names the rules reserve, and their updates go through ValidateStored.
func TestUnitValidateStoredSkipsTheKeyRules(t *testing.T) {
	c := repo.Config{Key: "artifactory-build-info", Class: repo.Local, PackageType: repo.Generic}
	if err := repo.Validate(&c); keyError(t, err) == "" {
		t.Fatalf("Validate accepted a reserved key: %v", err)
	}
	if err := repo.ValidateStored(&c); err != nil {
		t.Fatalf("ValidateStored: %v", err)
	}
	c.Class = "mirror"
	if err := repo.ValidateStored(&c); err == nil {
		t.Fatal("ValidateStored skipped the other rules")
	}
}

// The server's build-info repository keeps its package type on an update,
// which a create and any other key may not have (#472).
func TestUnitValidateStoredKeepsTheBuildInfoType(t *testing.T) {
	c := repo.Config{
		Key: "artifactory-build-info", Class: repo.Local, PackageType: repo.BuildInfo,
	}
	if err := repo.ValidateStored(&c); err != nil {
		t.Fatalf("ValidateStored: %v", err)
	}
	if err := repo.Validate(&c); err == nil || !strings.Contains(err.Error(), "packageType") {
		t.Fatalf("Validate: %v", err)
	}
	for _, other := range []repo.Config{
		{Key: "builds", Class: repo.Local, PackageType: repo.BuildInfo},
		{Key: "Artifactory-Build-Info", Class: repo.Local, PackageType: repo.BuildInfo},
	} {
		if err := repo.ValidateStored(&other); err == nil {
			t.Errorf("%s kept buildinfo", other.Key)
		}
	}
}

// terraformType is read in any case and must then be MODULE or PROVIDER;
// the refusal echoes the value upper-cased, as it would be stored
// (decision 0040).
func TestUnitTerraformTypeEnum(t *testing.T) {
	const doc = `{"key":"tf","rclass":"%s","packageType":"terraform","terraformType":%q}`
	for _, tc := range []struct {
		class, value, want string
	}{
		{"local", "module", ""},
		{"federated", "Provider", ""},
		{"local", "backend", `terraformType: "BACKEND" is not one of MODULE, PROVIDER`},
		{"federated", " module", `terraformType: " MODULE" is not one of MODULE, PROVIDER`},
	} {
		var c repo.Config
		if err := json.Unmarshal(fmt.Appendf(nil, doc, tc.class, tc.value), &c); err != nil {
			t.Fatal(err)
		}
		err := repo.Validate(&c)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q: %v", tc.value, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%q: %v, want %s", tc.value, err, tc.want)
		}
	}
}

// Once set, a Terraform repository's terraformType cannot change or be
// removed; an update may set it where there was none, and other package
// types keep the field inert.
func TestUnitValidateUpdateTerraformType(t *testing.T) {
	tf := func(tt repo.TerraformType) *repo.Config {
		return &repo.Config{
			Key: "tf", Class: repo.Local, PackageType: repo.Terraform, TerraformType: tt,
		}
	}
	generic := func(tt repo.TerraformType) *repo.Config {
		return &repo.Config{
			Key:           "g",
			Class:         repo.Local,
			PackageType:   repo.Generic,
			TerraformType: tt,
		}
	}
	for _, tc := range []struct {
		stored, updated *repo.Config
		want            string
	}{
		{tf(repo.TerraformModule), tf(repo.TerraformModule), ""},
		{tf(""), tf(repo.TerraformProvider), ""},
		{tf(""), tf(""), ""},
		{
			tf(repo.TerraformModule), tf(repo.TerraformProvider),
			"The field 'terraformType' of a Terraform repository cannot change once set",
		},
		{
			tf(repo.TerraformProvider), tf(""),
			"The field 'terraformType' of a Terraform repository cannot change once set",
		},
		{generic(repo.TerraformModule), generic(""), ""},
	} {
		err := repo.ValidateUpdate(tc.stored, tc.updated)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%q to %q: %v", tc.stored.TerraformType, tc.updated.TerraformType, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) ||
			!errors.Is(err, repo.ErrInvalid)):
			t.Errorf("%q to %q: %v, want %s",
				tc.stored.TerraformType, tc.updated.TerraformType, err, tc.want)
		}
	}
}

// Debian Release and Terraform PROVIDER signing need GPG keys in either field.
func TestUnitValidateKeyPair(t *testing.T) {
	c := func(class repo.Class, pt repo.PackageType, tt repo.TerraformType) *repo.Config {
		return &repo.Config{Key: "r", Class: class, PackageType: pt, TerraformType: tt}
	}
	provider := c(repo.Local, repo.Terraform, repo.TerraformProvider)
	for _, tc := range []struct {
		c        *repo.Config
		pairType string
		refused  bool
	}{
		{provider, "GPG", false},
		{provider, "gpg", false},
		{provider, "RSA", true},
		{c(repo.Federated, repo.Terraform, repo.TerraformProvider), "RSA", true},
		{c(repo.Local, repo.Terraform, repo.TerraformModule), "RSA", false},
		{c(repo.Local, repo.Terraform, ""), "RSA", false},
		{c(repo.Local, repo.Debian, ""), "GPG", false},
		{c(repo.Local, repo.Debian, ""), "RSA", true},
		{c(repo.Federated, repo.Debian, ""), "RSA", true},
		{c(repo.Local, repo.Generic, repo.TerraformProvider), "RSA", false},
	} {
		for _, field := range []string{"primaryKeyPairRef", "secondaryKeyPairRef"} {
			err := repo.ValidateKeyPair(tc.c, field, "signing", tc.pairType)
			want := field + ": names the RSA key pair 'signing'"
			if tc.refused != (err != nil) || tc.refused && !strings.Contains(err.Error(), want) {
				t.Errorf("%s %s %q %s: %v", tc.c.Class, tc.c.PackageType, tc.c.TerraformType,
					tc.pairType, err)
			}
		}
	}
}

func TestUnitAlpineIndexRequiresRSAKeyPair(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Virtual, repo.Federated} {
		c := &repo.Config{Key: "apk", Class: class, PackageType: repo.Alpine}
		if err := repo.ValidateKeyPair(c, "keyPair", "apk-key", "RSA"); err != nil {
			t.Errorf("%s RSA: %v", class, err)
		}
		if err := repo.ValidateKeyPair(c, "keyPair", "apk-key", "GPG"); err == nil ||
			!strings.Contains(err.Error(), "RSA only") {
			t.Errorf("%s GPG: %v", class, err)
		}
	}
}
