package repo_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/repo"
)

func ptr[T any](v T) *T { return &v }

// decode is a helper that normalises JSON for comparison regardless of key order.
func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode: %v\n%s", err, b)
	}
	return m
}

func TestFixturesRoundTrip(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var cfg repo.Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out, err := json.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Artifactory echoes empty strings for description and the like;
			// the model omits unset strings (but keeps the pointer-typed
			// patterns), so compare after dropping empties on both sides.
			dropEmpty := func(m map[string]any) map[string]any {
				for k, v := range m {
					if s, ok := v.(string); ok && s == "" {
						delete(m, k)
					}
				}
				return m
			}
			want := dropEmpty(decode(t, raw))
			if diff := cmp.Diff(want, dropEmpty(decode(t, out))); diff != "" {
				t.Errorf("round trip changed the document (-want +got):\n%s", diff)
			}
			if err := repo.Validate(repo.Defaults(&cfg)); err != nil {
				t.Errorf("fixture does not validate: %v", err)
			}
		})
	}
}

func TestFlexBoolSpellingPerClass(t *testing.T) {
	tests := []struct {
		class repo.Class
		want  string
	}{
		{repo.Local, `"priorityResolution":"true"`},
		{repo.Federated, `"priorityResolution":true`},
		{repo.Remote, `"priorityResolution":true`},
		{repo.Virtual, `"priorityResolution":true`},
	}
	for _, tt := range tests {
		t.Run(string(tt.class), func(t *testing.T) {
			cfg := repo.Config{Key: "k", Class: tt.class, PriorityResolution: repo.Flex(true)}
			out, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("want %s in %s", tt.want, out)
			}
			// The caller's value must not have been mutated into string mode.
			standalone, _ := json.Marshal(cfg.PriorityResolution)
			if string(standalone) != "true" {
				t.Errorf("standalone FlexBool marshalled as %s", standalone)
			}
		})
	}
}

func TestFlexBoolUnmarshal(t *testing.T) {
	tests := []struct {
		in      string
		want    bool
		wantErr error
	}{
		{`true`, true, nil},
		{`false`, false, nil},
		{`"true"`, true, nil},
		{`"false"`, false, nil},
		{`"TRUE"`, true, nil},
		{`"yes"`, false, repo.ErrNotBoolean},
		{`1`, false, repo.ErrNotBoolean},
		{`null`, false, repo.ErrNotBoolean},
		{`{`, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var f repo.FlexBool
			err := json.Unmarshal([]byte(tt.in), &f)
			switch {
			case tt.in == "{":
				if err == nil {
					t.Fatal("expected a syntax error")
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got %v, want %v", err, tt.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case f.Value != tt.want:
				t.Errorf("got %v want %v", f.Value, tt.want)
			}
		})
	}
	var nilFlex *repo.FlexBool
	if nilFlex.Bool() || !repo.Flex(true).Bool() {
		t.Error("Bool on nil or true pointer")
	}
}

func TestMembersEnabledIsAString(t *testing.T) {
	cfg := repo.Config{Key: "f", Class: repo.Federated, Members: []repo.Member{
		{URL: "https://a/x", Enabled: repo.Flex(false)},
		{URL: "https://b/x"},
	}}
	repo.Defaults(&cfg)
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"enabled":"false"`, `"enabled":"true"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("want %s in %s", want, out)
		}
	}
	if !cfg.Members[0].Enabled.Bool() == false || !cfg.Members[1].Enabled.Bool() {
		t.Errorf("defaults changed an explicit member flag: %+v", cfg.Members)
	}
}

func TestTerraformTypeCaseInsensitive(t *testing.T) {
	for _, in := range []string{`"module"`, `"MODULE"`, `"Module"`} {
		var tt repo.TerraformType
		if err := json.Unmarshal([]byte(in), &tt); err != nil {
			t.Fatal(err)
		}
		if tt != repo.TerraformModule {
			t.Errorf("%s -> %q", in, tt)
		}
	}
	var tt repo.TerraformType
	if err := json.Unmarshal([]byte(`1`), &tt); err == nil {
		t.Error("expected an error for a non-string terraformType")
	}
	out, _ := json.Marshal(repo.Config{Key: "t", Class: repo.Local, TerraformType: tt})
	if strings.Contains(string(out), "terraformType") {
		t.Errorf("empty terraformType was emitted: %s", out)
	}
}

func TestDefaults(t *testing.T) {
	tests := []struct {
		name  string
		in    repo.Config
		check func(t *testing.T, c *repo.Config)
	}{
		{"common", repo.Config{Key: "k", Class: repo.Local}, func(t *testing.T, c *repo.Config) {
			want := repo.Config{
				Key:         "k",
				Class:       repo.Local,
				PackageType: repo.Generic,
				IncludesPattern: ptr(
					"**/*",
				),
				ExcludesPattern: ptr(""),
				RepoLayoutRef:   repo.LayoutMaven2,
				BlackedOut:      ptr(false),
				XrayIndex:       ptr(false),
				PriorityResolution: repo.Flex(
					false,
				),
				DownloadRedirect:   repo.Flex(false),
				CDNRedirect:        repo.Flex(false),
				ChecksumPolicyType: repo.ClientChecksums,
				HandleReleases:     ptr(true),
				HandleSnapshots:    ptr(true),
				MaxUniqueSnapshots: ptr(
					0,
				),
				SnapshotVersionBehavior:      repo.Unique,
				SuppressPomConsistencyChecks: ptr(false),
				MaxUniqueTags:                ptr(0),
				DockerTagRetention:           ptr(1),
				DockerAPIVersion:             repo.DockerV2,
				BlockPushingSchema1: repo.Flex(
					false,
				),
				ArchiveBrowsingEnabled:  ptr(false),
				EnableFileListsIndexing: repo.Flex(false),
				ForceNugetAuthentication: ptr(
					false,
				),
				ForceP2Authentication:    ptr(false),
				ForceConanAuthentication: ptr(false),
				ForceMetadataNameVersion: ptr(false),
				ForceNonDuplicateChart:   ptr(false),
			}
			if diff := cmp.Diff(want, *c, cmp.AllowUnexported(repo.FlexBool{})); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		}},
		{
			"local keeps caller values",
			repo.Config{
				Key:                "k",
				Class:              repo.Local,
				RepoLayoutRef:      "simple-default",
				MaxUniqueTags:      ptr(3),
				PriorityResolution: repo.Flex(true),
			},
			func(t *testing.T, c *repo.Config) {
				if c.RepoLayoutRef != "simple-default" || *c.MaxUniqueTags != 3 ||
					!c.PriorityResolution.Bool() {
					t.Errorf("caller values overwritten: %+v", c)
				}
			},
		},
		{
			"terraformbackend encrypts",
			repo.Config{Key: "k", Class: repo.Local, PackageType: repo.TerraformBackend},
			func(t *testing.T, c *repo.Config) {
				if c.EncryptStates == nil || !*c.EncryptStates {
					t.Error("encryptStates should default to true")
				}
			},
		},
		{
			"local other type has no encryptStates",
			repo.Config{Key: "k", Class: repo.Local, PackageType: repo.Maven},
			func(t *testing.T, c *repo.Config) {
				if c.EncryptStates != nil || c.URL != "" || c.SocketTimeoutMillis != nil {
					t.Error("local got remote-only or type-specific defaults")
				}
			},
		},
		{
			"remote maven",
			repo.Config{Key: "k", Class: repo.Remote, PackageType: repo.Maven, URL: "https://x"},
			func(t *testing.T, c *repo.Config) {
				if *c.RetrievalCachePeriodSecs != 7200 || *c.MissedRetrievalCachePeriodSecs != 1800 || *c.SocketTimeoutMillis != 15000 ||
					*c.AssumedOfflinePeriodSecs != 300 ||
					*c.MetadataRetrievalTimeoutSecs != 60 ||
					!*c.StoreArtifactsLocally ||
					!*c.BlockMismatchingMimeTypes ||
					c.RemoteRepoChecksumPolicyType != repo.GenerateIfAbsent ||
					!*c.HandleReleases {
					t.Errorf("remote defaults wrong: %+v", c)
				}
				if c.PyPIRegistryURL != "" || c.V3FeedURL != "" || c.VCSType != "" {
					t.Errorf("maven remote got other ecosystems' defaults: %+v", c)
				}
			},
		},
		{
			"remote docker",
			repo.Config{Key: "k", Class: repo.Remote, PackageType: repo.Docker},
			func(t *testing.T, c *repo.Config) {
				if *c.RetrievalCachePeriodSecs != 21600 ||
					*c.MissedRetrievalCachePeriodSecs != 1800 {
					t.Errorf(
						"docker cache periods: %d %d",
						*c.RetrievalCachePeriodSecs,
						*c.MissedRetrievalCachePeriodSecs,
					)
				}
			},
		},
		{
			"remote helmoci and oci",
			repo.Config{Key: "k", Class: repo.Remote, PackageType: repo.HelmOCI},
			func(t *testing.T, c *repo.Config) {
				if *c.RetrievalCachePeriodSecs != 21600 {
					t.Error("helmoci should use the docker retrieval period")
				}
				o := repo.Defaults(
					&repo.Config{Key: "k", Class: repo.Remote, PackageType: repo.OCI},
				)
				if *o.RetrievalCachePeriodSecs != 21600 {
					t.Error("oci should use the docker retrieval period")
				}
			},
		},
		{
			"remote go",
			repo.Config{Key: "k", Class: repo.Remote, PackageType: repo.Go},
			func(t *testing.T, c *repo.Config) {
				if *c.MissedRetrievalCachePeriodSecs != 600 || c.VCSType != "GIT" ||
					c.VCSGitProvider != repo.GitHub {
					t.Errorf("go remote defaults: %+v", c)
				}
			},
		},
		{
			"remote pypi",
			repo.Config{Key: "k", Class: repo.Remote, PackageType: repo.PyPI},
			func(t *testing.T, c *repo.Config) {
				if c.PyPIRegistryURL != "https://pypi.org" || c.PyPIRepositorySuffix != "simple" {
					t.Errorf("pypi defaults: %+v", c)
				}
			},
		},
		{
			"remote nuget",
			repo.Config{
				Key:         "k",
				Class:       repo.Remote,
				PackageType: repo.NuGet,
				V3FeedURL:   "https://mine",
			},
			func(t *testing.T, c *repo.Config) {
				if c.FeedContextPath != "api/v2" || c.DownloadContextPath != "api/v2/package" ||
					c.V3FeedURL != "https://mine" {
					t.Errorf("nuget defaults: %+v", c)
				}
			},
		},
		{
			"virtual",
			repo.Config{Key: "k", Class: repo.Virtual, Repositories: []string{"a"}},
			func(t *testing.T, c *repo.Config) {
				if *c.VirtualRetrievalCachePeriodSecs != 7200 ||
					c.PomRepositoryReferencesCleanupPolicy != repo.DiscardActiveReference ||
					*c.ArtifactoryRequestsCanRetrieveRemoteArtifacts ||
					c.DockerAPIVersion != repo.DockerV2 ||
					*c.UseNamespaces {
					t.Errorf("virtual defaults: %+v", c)
				}
				if c.ChecksumPolicyType != "" || c.HandleReleases != nil {
					t.Error("virtual got local defaults")
				}
			},
		},
		{
			"unknown class only gets common defaults",
			repo.Config{Key: "k", Class: "weird"},
			func(t *testing.T, c *repo.Config) {
				if c.HandleReleases != nil || c.SocketTimeoutMillis != nil ||
					c.VirtualRetrievalCachePeriodSecs != nil {
					t.Error("class-specific defaults applied to an unknown class")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			got := repo.Defaults(&in)
			if got != &in {
				t.Error("Defaults must return its argument")
			}
			tt.check(t, got)
		})
	}
}

func TestValidate(t *testing.T) {
	valid := func() repo.Config {
		return repo.Config{Key: "libs-release", Class: repo.Local, PackageType: repo.Maven}
	}
	tests := []struct {
		name   string
		mutate func(c *repo.Config)
		field  string
	}{
		{"valid local", func(c *repo.Config) {}, ""},
		{"valid remote", func(c *repo.Config) { c.Class = repo.Remote; c.URL = "https://x" }, ""},
		{"valid virtual", func(c *repo.Config) {
			c.Class = repo.Virtual
			c.Repositories = []string{"a", "b"}
			c.DefaultDeploymentRepo = "a"
		}, ""},
		{"valid federated", func(c *repo.Config) { c.Class = repo.Federated }, ""},
		{"terraformType on local", func(c *repo.Config) {
			c.PackageType = repo.Terraform
			c.TerraformType = repo.TerraformProvider
		}, ""},
		{"terraformType on remote", func(c *repo.Config) {
			c.Class = repo.Remote
			c.URL = "https://x"
			c.PackageType = repo.Terraform
			c.TerraformType = repo.TerraformProvider
		}, "terraformType"},
		{"terraformType on federated", func(c *repo.Config) {
			c.Class = repo.Federated
			c.PackageType = repo.Terraform
			c.TerraformType = repo.TerraformModule
		}, "terraformType"},
		{"proxy on remote", func(c *repo.Config) {
			c.Class = repo.Remote
			c.URL = "https://x"
			c.Proxy = "corp"
		}, ""},
		{
			"proxy on federated",
			func(c *repo.Config) { c.Class = repo.Federated; c.Proxy = "corp" },
			"",
		},
		{"proxy on local", func(c *repo.Config) { c.Proxy = "corp" }, "proxy"},
		{"disableProxy on virtual", func(c *repo.Config) {
			c.Class = repo.Virtual
			c.Repositories = []string{"a"}
			f := false
			c.DisableProxy = &f
		}, "proxy"},
		{"missing key", func(c *repo.Config) { c.Key = "" }, "key"},
		{"key with space", func(c *repo.Config) { c.Key = "my repo" }, "key"},
		{"key with slash", func(c *repo.Config) { c.Key = "a/b" }, "key"},
		{"key with backslash", func(c *repo.Config) { c.Key = `a\b` }, "key"},
		{"cache suffix", func(c *repo.Config) { c.Key = "maven-cache" }, "key"},
		{"missing class", func(c *repo.Config) { c.Class = "" }, "rclass"},
		{"unknown class", func(c *repo.Config) { c.Class = "mirror" }, "rclass"},
		{"type not allowed for remote", func(c *repo.Config) {
			c.Class = repo.Remote
			c.URL = "https://x"
			c.PackageType = repo.TerraformBackend
		}, "packageType"},
		{"type not allowed for virtual", func(c *repo.Config) {
			c.Class = repo.Virtual
			c.Repositories = []string{"a"}
			c.PackageType = repo.Cargo
		}, "packageType"},
		{
			"type not allowed for federated",
			func(c *repo.Config) { c.Class = repo.Federated; c.PackageType = repo.Swift },
			"packageType",
		},
		{
			"vcs allowed for remote only",
			func(c *repo.Config) { c.PackageType = repo.VCS },
			"packageType",
		},
		{
			"project without environments",
			func(c *repo.Config) { c.ProjectKey = "proj" },
			"environments",
		},
		{
			"project with environments",
			func(c *repo.Config) { c.ProjectKey = "proj"; c.Environments = []string{"DEV"} },
			"",
		},
		{"remote without url", func(c *repo.Config) { c.Class = repo.Remote }, "url"},
		{
			"virtual without members",
			func(c *repo.Config) { c.Class = repo.Virtual },
			"repositories",
		},
		{"deployment repo not a member", func(c *repo.Config) {
			c.Class = repo.Virtual
			c.Repositories = []string{"a"}
			c.DefaultDeploymentRepo = "b"
		}, "defaultDeploymentRepo"},
		{
			"negative snapshots",
			func(c *repo.Config) { c.MaxUniqueSnapshots = ptr(-1) },
			"maxUniqueSnapshots",
		},
		{"negative cache period", func(c *repo.Config) {
			c.Class = repo.Remote
			c.URL = "https://x"
			c.RetrievalCachePeriodSecs = ptr(-5)
		}, "retrievalCachePeriodSecs"},
		{"negative ttl", func(c *repo.Config) { c.SignedURLTTL = ptr(-1) }, "signedUrlTtl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid()
			tt.mutate(&c)
			err := repo.Validate(&c)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error on %s", tt.field)
			}
			if !errors.Is(err, repo.ErrInvalid) {
				t.Errorf("error does not unwrap to ErrInvalid: %v", err)
			}
			var errs repo.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("error is not an Errors list: %T", err)
			}
			found := false
			for _, e := range errs {
				if e.Field == tt.field {
					found = true
				}
			}
			if !found || !strings.Contains(err.Error(), tt.field) {
				t.Errorf("expected a %s error, got %v", tt.field, err)
			}
		})
	}
}

func TestValidateCollectsEveryError(t *testing.T) {
	c := repo.Config{
		Key:           "",
		Class:         repo.Virtual,
		PackageType:   repo.Cargo,
		ProjectKey:    "p",
		MaxUniqueTags: ptr(-1),
	}
	err := repo.Validate(&c)
	var errs repo.Errors
	if !errors.As(err, &errs) || len(errs) != 5 {
		t.Fatalf("want 5 errors, got %v", err)
	}
	if fe := errs[0]; fe.Error() != "key: is required" {
		t.Errorf("first error %q", fe.Error())
	}
}

func TestAllowedPackageTypes(t *testing.T) {
	local := repo.AllowedPackageTypes(repo.Local)
	if len(local) != 36 {
		t.Errorf("local has %d types, want 36", len(local))
	}
	has := func(ts []repo.PackageType, t repo.PackageType) bool {
		for _, x := range ts {
			if x == t {
				return true
			}
		}
		return false
	}
	remote := repo.AllowedPackageTypes(repo.Remote)
	if has(remote, repo.TerraformBackend) || has(remote, repo.Vagrant) ||
		has(remote, repo.BazelModules) ||
		!has(remote, repo.VCS) ||
		!has(remote, repo.P2) {
		t.Errorf("remote set wrong: %v", remote)
	}
	virtual := repo.AllowedPackageTypes(repo.Virtual)
	if has(virtual, repo.Cargo) || has(virtual, repo.Opkg) || !has(virtual, repo.Swift) {
		t.Errorf("virtual set wrong: %v", virtual)
	}
	fed := repo.AllowedPackageTypes(repo.Federated)
	if has(fed, repo.Swift) || has(fed, repo.TerraformBackend) || !has(fed, repo.Vagrant) {
		t.Errorf("federated set wrong: %v", fed)
	}
	if repo.AllowedPackageTypes("nope") != nil {
		t.Error("unknown class should return nil")
	}
}

func TestDefaultLayout(t *testing.T) {
	tests := []struct {
		pt   repo.PackageType
		tf   repo.TerraformType
		want string
	}{
		{repo.Maven, "", repo.LayoutMaven2},
		{repo.Gradle, "", repo.LayoutMaven2},
		{repo.Ivy, "", repo.LayoutIvy},
		{repo.SBT, "", repo.LayoutSBT},
		{repo.NPM, "", repo.LayoutNPM},
		{repo.NuGet, "", repo.LayoutNuGet},
		{repo.Go, "", repo.LayoutGo},
		{repo.Bower, "", repo.LayoutBower},
		{repo.Composer, "", repo.LayoutComposer},
		{repo.Conan, "", repo.LayoutConan},
		{repo.Puppet, "", repo.LayoutPuppet},
		{repo.Ansible, "", repo.LayoutAnsible},
		{repo.Swift, "", repo.LayoutSwift},
		{repo.Terraform, repo.TerraformModule, repo.LayoutTerraformModule},
		{repo.Terraform, repo.TerraformProvider, repo.LayoutTerraformProvider},
		{repo.Terraform, "", repo.LayoutSimple},
		{repo.Docker, "", repo.LayoutSimple},
		{repo.Generic, "", repo.LayoutSimple},
		{repo.PyPI, "", repo.LayoutSimple},
	}
	for _, tt := range tests {
		if got := repo.DefaultLayout(tt.pt, tt.tf); got != tt.want {
			t.Errorf("%s/%s: got %s want %s", tt.pt, tt.tf, got, tt.want)
		}
	}
}

func TestMarshalOmitsUnsetAndNested(t *testing.T) {
	cfg := repo.Config{
		Key:   "r",
		Class: repo.Remote,
		URL:   "https://u",
		ContentSynchronisation: &repo.ContentSynchronisation{
			Enabled:    true,
			Statistics: &repo.EnabledFlag{Enabled: true},
			Source:     &repo.ContentSyncSource{OriginAbsenceDetection: true},
		},
		CustomHTTPHeaders: []repo.CustomHTTPHeader{{Name: "A", Value: "b", Sensitive: true}},
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, out)
	if len(got) != 5 {
		t.Errorf("expected only the five set keys, got %v", got)
	}
	var back repo.Config
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(cfg, back); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestConfigUnmarshalJSONRejectsMalformed(t *testing.T) {
	var c repo.Config
	if err := json.Unmarshal(
		[]byte(`{"key":"k","rclass":"local","priorityResolution":"yes"}`),
		&c,
	); err == nil {
		t.Fatal("expected an error for a non-boolean flag")
	}
	if err := json.Unmarshal(
		[]byte(`{"key":"k","rclass":"local","downloadRedirect":"true"}`),
		&c,
	); err != nil ||
		c.Key != "k" {
		t.Fatalf("decode: %v, key %q", err, c.Key)
	}
}
