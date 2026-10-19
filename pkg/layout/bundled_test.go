package layout_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/layout"
)

// realPaths are paths the clients publish, each parsed by the layout a
// repository of that type uses. Sources:
//   - JFrog's repository-layouts page: the jetty-ajp Maven, Ivy and Gradle
//     paths and the "5.1-20101202161531" Ivy integration revision;
//   - Gradle's publishing_maven and publishing_ivy pages (my-library and
//     project1), and IvyArtifactRepository.layout for its "gradle" and "ivy"
//     layouts;
//   - sbt librarymanagement's Resolver.ivyStylePatterns,
//     [organisation]/[module]/(scala_[scalaVersion]/)(sbt_[sbtVersion]/)[revision]/[type]s/[artifact](-[classifier]).[ext];
//   - JFrog's Cargo, Conan, Terraform and Swift repository pages;
//   - the Maven 1, npm, NuGet, Puppet Forge, Composer and Ansible Galaxy file
//     names those ecosystems publish.
var realPaths = []struct {
	layout string
	path   string
	want   layout.Values
	desc   bool
}{
	// Gradle maven-publish and JFrog's Maven examples.
	{"maven-2-default", "com/example/my-library/1.0/my-library-1.0.pom", layout.Values{
		"orgPath": "com/example", "org": "com.example", "module": "my-library", "baseRev": "1.0",
	}, true},
	{"maven-2-default", "com/example/my-library/1.0/my-library-1.0-sources.jar", layout.Values{
		"orgPath": "com/example", "org": "com.example", "module": "my-library", "baseRev": "1.0",
		"classifier": "sources", "ext": "jar",
	}, false},
	{"maven-2-default", "com/example/my-library/1.0/my-library-1.0.module", layout.Values{
		"orgPath": "com/example", "org": "com.example", "module": "my-library", "baseRev": "1.0",
		"ext": "module",
	}, false},
	{
		"maven-2-default",
		"org/eclipse/jetty/jetty-ajp/7.0.2.v20100331/jetty-ajp-7.0.2.v20100331.jar",
		layout.Values{
			"orgPath": "org/eclipse/jetty", "org": "org.eclipse.jetty", "module": "jetty-ajp",
			"baseRev": "7.0.2.v20100331", "ext": "jar",
		},
		false,
	},
	// A unique snapshot's POM and a non-unique snapshot's jar.
	{
		"maven-2-default",
		"com/example/my-library/1.0-SNAPSHOT/my-library-1.0-20240101.120000-3.pom",
		layout.Values{
			"orgPath":       "com/example",
			"org":           "com.example",
			"module":        "my-library",
			"baseRev":       "1.0",
			"folderItegRev": "SNAPSHOT",
			"fileItegRev":   "20240101.120000-3",
		},
		true,
	},
	{
		"maven-2-default",
		"com/example/my-library/1.0-SNAPSHOT/my-library-1.0-SNAPSHOT.jar",
		layout.Values{
			"orgPath":       "com/example",
			"org":           "com.example",
			"module":        "my-library",
			"baseRev":       "1.0",
			"folderItegRev": "SNAPSHOT",
			"fileItegRev":   "SNAPSHOT",
			"ext":           "jar",
		},
		false,
	},

	{
		"ivy-default",
		"org.eclipse.jetty/jetty-ajp/7.0.2.v20100331/jars/jetty-ajp-7.0.2.v20100331.jar",
		layout.Values{
			"org": "org.eclipse.jetty", "orgPath": "org/eclipse/jetty", "module": "jetty-ajp",
			"baseRev": "7.0.2.v20100331", "type": "jar", "ext": "jar",
		},
		false,
	},
	{
		"ivy-default",
		"org.acme/app/5.1-20101202161531/jars/app-5.1-20101202161531.jar",
		layout.Values{
			"org":           "org.acme",
			"orgPath":       "org/acme",
			"module":        "app",
			"baseRev":       "5.1",
			"folderItegRev": "20101202161531",
			"fileItegRev":   "20101202161531",
			"type":          "jar",
			"ext":           "jar",
		},
		false,
	},
	{
		"ivy-default",
		"org.acme/app/5.1-20101202161531/ivys/ivy-5.1-20101202161531.xml",
		layout.Values{
			"org": "org.acme", "orgPath": "org/acme", "module": "app", "baseRev": "5.1",
			"folderItegRev": "20101202161531", "fileItegRev": "20101202161531", "type": "ivy",
		},
		true,
	},

	// Gradle ivy-publish writes the artifacts this layout expects.
	{"gradle-default", "org.gradle.sample/project1/1.0/project1-1.0.jar", layout.Values{
		"org": "org.gradle.sample", "orgPath": "org/gradle/sample", "module": "project1",
		"baseRev": "1.0", "ext": "jar",
	}, false},
	{"gradle-default", "org.gradle.sample/project1/1.0/project1-1.0.module", layout.Values{
		"org": "org.gradle.sample", "orgPath": "org/gradle/sample", "module": "project1",
		"baseRev": "1.0", "ext": "module",
	}, false},
	{"gradle-default", "org.eclipse.jetty/jetty-ajp/ivy-7.0.2.v20100331.xml", layout.Values{
		"org": "org.eclipse.jetty", "orgPath": "org/eclipse/jetty", "module": "jetty-ajp",
		"baseRev": "7.0.2.v20100331",
	}, true},

	{"maven-1-default", "commons-lang/jars/commons-lang-2.1.jar", layout.Values{
		"org": "commons-lang", "orgPath": "commons-lang", "type": "jar", "module": "commons-lang",
		"baseRev": "2.1", "ext": "jar",
	}, false},
	{"maven-1-default", "commons-lang/poms/commons-lang-2.1.pom", layout.Values{
		"org": "commons-lang", "orgPath": "commons-lang", "type": "pom", "module": "commons-lang",
		"baseRev": "2.1",
	}, true},

	{
		"nuget-default",
		"newtonsoft/Newtonsoft.Json/Newtonsoft.Json.13.0.3.nupkg",
		layout.Values{
			"orgPath": "newtonsoft",
			"org":     "newtonsoft",
			"module":  "Newtonsoft.Json",
			"baseRev": "13.0.3",
		},
		false,
	},

	// Where npm names its tarballs and the npm format stores them
	// (decision 0034): <name>/-/<name>-<version>.tgz, and for a scoped
	// package @scope/name/-/@scope/name-<version>.tgz.
	{"npm-default", "lodash/-/lodash-4.17.21.tgz", layout.Values{
		"orgPath": "lodash", "org": "lodash", "module": "lodash", "baseRev": "4.17.21",
	}, false},
	{"npm-default", "@acme/ui/-/@acme/ui-1.2.3.tgz", layout.Values{
		"orgPath": "@acme/ui", "org": "@acme.ui", "scope": "@acme", "module": "ui",
		"baseRev": "1.2.3",
	}, false},
	{"npm-default", "@types/node/-/@types/node-22.10.2.tgz", layout.Values{
		"orgPath": "@types/node", "org": "@types.node", "scope": "@types", "module": "node",
		"baseRev": "22.10.2",
	}, false},
	// [module] is greedy, so a pre-release splits after its last hyphen, as
	// 7.x's own npm-default splits it.
	{"npm-default", "lodash/-/lodash-5.0.0-rc.1.tgz", layout.Values{
		"orgPath": "lodash", "org": "lodash", "module": "lodash-5.0.0", "baseRev": "rc.1",
	}, false},

	{"bower-default", "acme/widget/widget-1.0.0-beta.1.zip", layout.Values{
		"orgPath": "acme", "org": "acme", "module": "widget", "baseRev": "1.0.0",
		"fileItegRev": "beta.1", "ext": "zip",
	}, false},

	{"vcs-default", "jfrog/jfrog-cli/tags/v2.50.0/jfrog-cli-v2.50.0.tar.gz", layout.Values{
		"orgPath": "jfrog", "org": "jfrog", "module": "jfrog-cli", "refs": "tags",
		"baseRev": "v2.50.0", "ext": "tar.gz",
	}, false},
	{
		"vcs-default",
		"jfrog/jfrog-cli/branches/master/jfrog-cli-master-0123456789abcdef0123456789abcdef01234567.tar.gz",
		layout.Values{
			"orgPath":     "jfrog",
			"org":         "jfrog",
			"module":      "jfrog-cli",
			"refs":        "branches",
			"baseRev":     "master",
			"fileItegRev": "0123456789abcdef0123456789abcdef01234567",
			"ext":         "tar.gz",
		},
		false,
	},

	// sbt plugins, published Ivy style.
	{
		"sbt-default",
		"com.typesafe.sbt/sbt-native-packager/scala_2.10/sbt_0.13/1.0.0/jars/sbt-native-packager.jar",
		layout.Values{
			"org":          "com.typesafe.sbt",
			"orgPath":      "com/typesafe/sbt",
			"module":       "sbt-native-packager",
			"scalaVersion": "2.10",
			"sbtVersion":   "0.13",
			"baseRev":      "1.0.0",
			"type":         "jar",
			"ext":          "jar",
		},
		false,
	},
	{
		"sbt-default",
		"com.typesafe.sbt/sbt-native-packager/scala_2.10/sbt_0.13/1.0.0/ivys/ivy.xml",
		layout.Values{
			"org":          "com.typesafe.sbt",
			"orgPath":      "com/typesafe/sbt",
			"module":       "sbt-native-packager",
			"scalaVersion": "2.10",
			"sbtVersion":   "0.13",
			"baseRev":      "1.0.0",
			"type":         "ivy",
		},
		true,
	},
	// 7.x's sbt-ivy-default: Maven's folders, with the Ivy descriptor in
	// the revision folder and a 14-digit integration revision.
	{"sbt-ivy-default", "org/example/lib_2.13/1.0/lib_2.13-1.0-sources.jar", layout.Values{
		"org": "org.example", "orgPath": "org/example", "module": "lib_2.13", "baseRev": "1.0",
		"classifier": "sources", "ext": "jar",
	}, false},
	{"sbt-ivy-default", "org/example/lib_2.13/1.0/ivy-1.0.xml", layout.Values{
		"org": "org.example", "orgPath": "org/example", "module": "lib_2.13", "baseRev": "1.0",
	}, true},
	{
		"sbt-ivy-default",
		"org/example/lib_2.13/1.0-20260101120000/lib_2.13-1.0-20260101120000.jar",
		layout.Values{
			"org": "org.example", "orgPath": "org/example", "module": "lib_2.13",
			"baseRev": "1.0", "folderItegRev": "20260101120000", "fileItegRev": "20260101120000",
			"ext": "jar",
		},
		false,
	},

	{"simple-default", "acme/tool/tool-1.0.zip", layout.Values{
		"orgPath": "acme", "org": "acme", "module": "tool", "baseRev": "1.0", "ext": "zip",
	}, false},
	{
		"composer-default",
		"monolog/monolog/monolog-3.5.0.zip",
		layout.Values{
			"orgPath": "monolog",
			"org":     "monolog",
			"module":  "monolog",
			"baseRev": "3.5.0",
			"ext":     "zip",
		},
		false,
	},

	// JFrog's Conan page: package ID 9999, recipe revision 1, package
	// revision 4; and the recipe's own files, which have no package part.
	{
		"conan-default",
		"user/lib/1.0/channel/1/package/9999/4/conan_package.tgz",
		layout.Values{
			"org":           "user",
			"orgPath":       "user",
			"module":        "lib",
			"baseRev":       "1.0",
			"channel":       "channel",
			"folderItegRev": "1",
			"package_id":    "9999",
			"fileItegRev":   "4",
			"remainder":     "conan_package.tgz",
		},
		false,
	},
	{"conan-default", "user/lib/1.0/channel/1/export/conanfile.py", layout.Values{
		"org": "user", "orgPath": "user", "module": "lib", "baseRev": "1.0", "channel": "channel",
		"folderItegRev": "1", "remainder": "export/conanfile.py",
	}, false},

	{"puppet-default", "puppetlabs/stdlib/puppetlabs-stdlib-9.4.1.tar.gz", layout.Values{
		"orgPath": "puppetlabs", "org": "puppetlabs", "module": "stdlib", "baseRev": "9.4.1",
	}, false},
	// Where the Go format stores a version's files (decision 0035):
	// <module>/@v/<version>.<zip|mod|info>, for a release, a pre-release,
	// an +incompatible version and pseudo-versions.
	{"go-default", "github.com/go-chi/chi/v5/@v/v5.3.2.zip", layout.Values{
		"orgPath": "github.com/go-chi/chi", "org": "github.com.go-chi.chi", "module": "v5",
		"baseRev": "5.3.2", "ext": "zip",
	}, false},
	{"go-default", "example.com/acme/lib/@v/v1.2.0-rc.1.mod", layout.Values{
		"orgPath": "example.com/acme", "org": "example.com.acme", "module": "lib",
		"baseRev": "1.2.0", "fileItegRev": "rc.1", "ext": "mod",
	}, false},
	{"go-default", "example.com/acme/lib/@v/v1.0.0-beta.info", layout.Values{
		"orgPath": "example.com/acme", "org": "example.com.acme", "module": "lib",
		"baseRev": "1.0.0", "fileItegRev": "beta", "ext": "info",
	}, false},
	{"go-default", "example.com/acme/legacy/@v/v2.0.0+incompatible.zip", layout.Values{
		"orgPath": "example.com/acme", "org": "example.com.acme", "module": "legacy",
		"baseRev": "2.0.0+incompatible", "ext": "zip",
	}, false},
	{"go-default", "example.com/acme/legacy/@v/v2.1.0-rc.2+incompatible.info", layout.Values{
		"orgPath": "example.com/acme", "org": "example.com.acme", "module": "legacy",
		"baseRev": "2.1.0", "fileItegRev": "rc.2+incompatible", "ext": "info",
	}, false},
	{"go-default", "example.com/acme/lib/@v/v0.0.0-20191109021931-daa7c04131f5.mod", layout.Values{
		"orgPath": "example.com/acme", "org": "example.com.acme", "module": "lib",
		"baseRev": "0.0.0", "fileItegRev": "20191109021931-daa7c04131f5", "ext": "mod",
	}, false},
	{
		"go-default",
		"example.com/acme/lib/@v/v1.2.4-pre.0.20191109021931-daa7c04131f5.zip",
		layout.Values{
			"orgPath": "example.com/acme", "org": "example.com.acme", "module": "lib",
			"baseRev": "1.2.4", "fileItegRev": "pre.0.20191109021931-daa7c04131f5", "ext": "zip",
		},
		false,
	},
	// The go command's upper-case module path, as jf go-publish stores it.
	{"go-default", "example.com/Acme/Upper/@v/v1.0.0.info", layout.Values{
		"orgPath": "example.com/Acme", "org": "example.com.Acme", "module": "Upper",
		"baseRev": "1.0.0", "ext": "info",
	}, false},
	// Module is greedy, so a build-info file name is all module, as in
	// Artifactory; the optional revision never takes the number's suffix.
	{"build-default", "my-build/42-1700000000000.json", layout.Values{
		"orgPath": "my-build", "org": "my-build", "module": "42-1700000000000", "ext": "json",
	}, false},

	// JFrog's Cargo page's example, and crate names with an underscore, a
	// hyphen and a pre-release version, as cargo publish names them.
	{"cargo-default", "crates/package/package-1.0.0.crate", layout.Values{
		"module": "package", "baseRev": "1.0.0", "ext": "crate",
	}, false},
	{"cargo-default", "crates/serde_json/serde_json-1.0.114.crate", layout.Values{
		"module": "serde_json", "baseRev": "1.0.114", "ext": "crate",
	}, false},
	{"cargo-default", "crates/tokio-util/tokio-util-0.7.10.crate", layout.Values{
		"module": "tokio-util", "baseRev": "0.7.10", "ext": "crate",
	}, false},
	{"cargo-default", "crates/rand/rand-0.9.0-alpha.1.crate", layout.Values{
		"module": "rand", "baseRev": "0.9.0-alpha.1", "ext": "crate",
	}, false},
	{
		"nix-default",
		"nixpkgs/hello/hello-2.12.1.nar",
		layout.Values{
			"orgPath": "nixpkgs",
			"org":     "nixpkgs",
			"module":  "hello",
			"baseRev": "2.12.1",
			"ext":     "nar",
		},
		false,
	},
}

func TestBundledLayoutsParseRealPaths(t *testing.T) {
	for _, tt := range realPaths {
		t.Run(tt.layout+"/"+tt.path, func(t *testing.T) {
			l := mustLookup(t, tt.layout)
			got, err := l.Match(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
			if l.IsDescriptor(tt.path) != tt.desc {
				t.Errorf("IsDescriptor = %v", !tt.desc)
			}
			build := l.Build
			if tt.desc {
				build = l.BuildDescriptor
			}
			if built, err := build(got); err != nil || built != tt.path {
				t.Errorf("rebuild: %s %v", built, err)
			}
		})
	}
}

// Real paths a bundled layout does not describe. Each is a property of
// Artifactory's definition, not a gap here, so a format that stores these
// paths must not depend on the layout to find them.
func TestBundledLayoutsRejectPathsTheyDoNotDescribe(t *testing.T) {
	for _, tt := range []struct {
		layout, path, why string
	}{
		{"npm-default", "@acme/ui/ui-1.2.3.tgz", "6.20's npm-default path, which npm never used"},
		{"npm-default", "lodash/-/lodash-4.17.21.zip", "npm tarballs are .tgz"},
		{"go-default", "example.com/acme/lib/@v/1.0.0.zip", "a Go version starts with v"},
		{"go-default", "example.com/acme/lib/@v/list", "the version list is no version file"},
		{"sbt-ivy-default", "org.example/lib_2.13/1.0/jars/lib_2.13.jar", "sbt's Ivy-style pattern is not 7.x's"},
		// 7.x's tokens outside the documented set are dropped, as
		// Artifactory's parser drops them, which leaves nothing a real path
		// matches.
		{"terraform-module-default", "jfrog/test-module/test-provider/1.0.0.zip", "JFrog's Terraform page"},
		{"terraform-provider-default", "hashicorp/null/3.1.0/terraform-provider-null_3.1.0_linux_amd64.zip", "JFrog's Terraform page"},
		{"swift-default", "mona/LinkedList/LinkedList-1.1.1.zip", "JFrog's Swift page"},
		{"ansible-default", "collections/geerlingguy/k8s/0.11.0/geerlingguy-k8s-0.11.0.tar.gz", "the export's own shape"},
		{"ivy-default", "org.gradle.sample/project1/1.0/jars/project1.jar", "Ivy's default pattern names files without the revision"},
		{"gradle-default", "org.gradle.sample/project1/1.0/ivy-1.0.xml", "Gradle's ivy-publish writes the descriptor in the revision folder"},
		{"sbt-default", "org.example/lib_2.13/1.0/jars/lib_2.13.jar", "the plugin folders' slashes are mandatory"},
		{"vcs-default", "jfrog/jfrog-cli/releases/v2.50.0/jfrog-cli-v2.50.0.tar.gz", "refs is tags or branches"},
		{"cargo-default", "serde/serde-1.0.197.crate", "JFrog's Cargo page: a crate outside crates/ is not identified"},
		{"cargo-default", "crates/serde/serde_json-1.0.114.crate", "the file is named after its folder's crate"},
	} {
		if _, err := mustLookup(t, tt.layout).Match(tt.path); !errors.Is(err, layout.ErrNoMatch) {
			t.Errorf("%s %s (%s): want ErrNoMatch, got %v", tt.layout, tt.path, tt.why, err)
		}
	}
}

// Every layout a repository can name is bundled, including the ones the JFrog
// Terraform provider assigns by default, in the order 7.x's descriptors list
// them: cargo-default after simple-default, sbt-ivy-default last of theirs,
// gradle-default where 6.20 had it, and nix-default, which none lists, after
// them all.
func TestBundledNames(t *testing.T) {
	want := []string{
		"maven-2-default", "ivy-default", "gradle-default", "maven-1-default", "nuget-default",
		"npm-default", "bower-default", "vcs-default", "sbt-default", "simple-default",
		"cargo-default", "composer-default", "conan-default", "puppet-default", "go-default",
		"build-default", "terraform-module-default", "terraform-provider-default",
		"swift-default", "ansible-default", "sbt-ivy-default", "nix-default",
	}
	var got []string
	for _, d := range layout.Bundled() {
		got = append(got, d.Name)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// A layout-translating copy reads a path with the source layout and builds
// it with the target's, and keeps the path when either fails (item
// Transfer's translator). Between simple-default and the npm and Go
// layouts, every file lands where the npm and Go formats read it.
func TestTranslateWithSimpleDefault(t *testing.T) {
	translate := func(from, to, p string) string {
		v, err := mustLookup(t, from).Match(p)
		if err != nil {
			return p
		}
		if out, err := mustLookup(t, to).Build(v); err == nil {
			return out
		}
		return p
	}
	const (
		simple = "simple-default"
		npm    = "npm-default"
		golang = "go-default"
	)
	for _, tt := range []struct{ from, to, path, want string }{
		// simple-default names the extension, which npm-default spells.
		{simple, npm, "lodash/lodash/lodash-4.17.21.tgz", "lodash/-/lodash-4.17.21.tgz"},
		// npm-default names no extension for simple-default to build with,
		// so the path is kept, and simple-default does not read it back.
		{npm, simple, "lodash/-/lodash-4.17.21.tgz", "lodash/-/lodash-4.17.21.tgz"},
		{simple, npm, "lodash/-/lodash-4.17.21.tgz", "lodash/-/lodash-4.17.21.tgz"},
		{npm, simple, "@acme/ui/-/@acme/ui-1.2.3.tgz", "@acme/ui/-/@acme/ui-1.2.3.tgz"},
		{simple, npm, "@acme/ui/-/@acme/ui-1.2.3.tgz", "@acme/ui/-/@acme/ui-1.2.3.tgz"},
		{simple, golang, "example.com/acme/lib/lib-1.2.3.zip", "example.com/acme/lib/@v/v1.2.3.zip"},
		{simple, golang, "example.com/acme/lib/lib-1.2.3.mod", "example.com/acme/lib/@v/v1.2.3.mod"},
		{golang, simple, "example.com/acme/lib/@v/v1.2.3.info", "example.com/acme/lib/lib-1.2.3.info"},
		{simple, golang, "example.com/acme/lib/lib-1.2.3.info", "example.com/acme/lib/@v/v1.2.3.info"},
	} {
		if got := translate(tt.from, tt.to, tt.path); got != tt.want {
			t.Errorf("%s to %s, %s: %s; want %s", tt.from, tt.to, tt.path, got, tt.want)
		}
	}
}
