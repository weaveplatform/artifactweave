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
//   - JFrog's Conan, Terraform and Swift repository pages;
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

	{"npm-default", "@acme/ui/ui-1.2.3.tgz", layout.Values{
		"orgPath": "@acme", "org": "@acme", "module": "ui", "baseRev": "1.2.3",
	}, false},
	{"npm-default", "acme/ui/ui-1.2.3-rc1.tgz", layout.Values{
		"orgPath": "acme", "org": "acme", "module": "ui", "baseRev": "1.2.3", "fileItegRev": "rc1",
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
	{"sbt-ivy-default", "org.example/lib_2.13/1.0/jars/lib_2.13.jar", layout.Values{
		"org": "org.example", "orgPath": "org/example", "module": "lib_2.13", "baseRev": "1.0",
		"type": "jar", "ext": "jar",
	}, false},
	{"sbt-ivy-default", "org.example/lib_2.13/1.0/srcs/lib_2.13-sources.jar", layout.Values{
		"org": "org.example", "orgPath": "org/example", "module": "lib_2.13", "baseRev": "1.0",
		"type": "src", "classifier": "sources", "ext": "jar",
	}, false},
	{"sbt-ivy-default", "org.example/lib_2.13/1.0/ivys/ivy.xml", layout.Values{
		"org": "org.example", "orgPath": "org/example", "module": "lib_2.13", "baseRev": "1.0",
		"type": "ivy",
	}, true},
	{
		"sbt-ivy-default",
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
	{"go-default", "github.com/go-chi/chi/@v/v5.3.2.zip", layout.Values{
		"orgPath": "github.com/go-chi", "org": "github.com.go-chi", "module": "chi",
		"baseRev": "v5.3.2", "ext": "zip",
	}, false},
	// Module is greedy, so a build-info file name is all module, as in
	// Artifactory; the optional revision never takes the number's suffix.
	{"build-default", "my-build/42-1700000000000.json", layout.Values{
		"orgPath": "my-build", "org": "my-build", "module": "42-1700000000000", "ext": "json",
	}, false},

	{"terraform-module-default", "jfrog/test-module/test-provider/1.0.0.zip", layout.Values{
		"orgPath": "jfrog", "org": "jfrog", "module": "test-module", "provider": "test-provider",
		"baseRev": "1.0.0", "ext": "zip",
	}, false},
	{"terraform-module-default", "tera/moduleName/aws/v0.1.2.zip", layout.Values{
		"orgPath": "tera", "org": "tera", "module": "moduleName", "provider": "aws",
		"baseRev": "v0.1.2", "ext": "zip",
	}, false},
	{
		"terraform-provider-default",
		"hashicorp/null/3.1.0/terraform-provider-null_3.1.0_linux_amd64.zip",
		layout.Values{
			"orgPath": "hashicorp", "org": "hashicorp", "module": "null", "baseRev": "3.1.0",
			"os": "linux", "arch": "amd64", "ext": "zip",
		},
		false,
	},
	// JFrog's own example puts a folder above the namespace.
	{
		"terraform-provider-default",
		"terraform-main-provider/hashicorp/null/3.1.0/terraform-provider-null_3.1.0_linux_amd64.zip",
		layout.Values{
			"orgPath": "terraform-main-provider/hashicorp",
			"org":     "terraform-main-provider.hashicorp",
			"module":  "null",
			"baseRev": "3.1.0",
			"os":      "linux",
			"arch":    "amd64",
			"ext":     "zip",
		},
		false,
	},
	{"ansible-default", "geerlingguy/k8s/geerlingguy-k8s-0.11.0.tar.gz", layout.Values{
		"orgPath": "geerlingguy", "org": "geerlingguy", "module": "k8s", "baseRev": "0.11.0",
	}, false},
	{"swift-default", "mona/LinkedList/LinkedList-1.1.1.zip", layout.Values{
		"orgPath": "mona", "org": "mona", "module": "LinkedList", "baseRev": "1.1.1", "ext": "zip",
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
		{"npm-default", "lodash/-/lodash-4.17.21.tgz", "npm's tarball path predates the layout"},
		{"npm-default", "@acme/ui/-/@acme/ui-1.2.3.tgz", "npm's scoped tarball path"},
		{"ivy-default", "org.gradle.sample/project1/1.0/jars/project1.jar", "Ivy's default pattern names files without the revision"},
		{"gradle-default", "org.gradle.sample/project1/1.0/ivy-1.0.xml", "Gradle's ivy-publish writes the descriptor in the revision folder"},
		{"sbt-default", "org.example/lib_2.13/1.0/jars/lib_2.13.jar", "the plugin folders' slashes are mandatory"},
		{"vcs-default", "jfrog/jfrog-cli/releases/v2.50.0/jfrog-cli-v2.50.0.tar.gz", "refs is tags or branches"},
	} {
		if _, err := mustLookup(t, tt.layout).Match(tt.path); !errors.Is(err, layout.ErrNoMatch) {
			t.Errorf("%s %s (%s): want ErrNoMatch, got %v", tt.layout, tt.path, tt.why, err)
		}
	}
}

// Every layout a repository can name is bundled, including the ones the JFrog
// Terraform provider assigns by default.
func TestBundledNames(t *testing.T) {
	want := []string{
		"maven-2-default", "ivy-default", "gradle-default", "maven-1-default", "nuget-default",
		"npm-default", "bower-default", "vcs-default", "sbt-default", "simple-default",
		"composer-default", "conan-default", "puppet-default", "go-default", "build-default",
		"sbt-ivy-default", "terraform-module-default", "terraform-provider-default",
		"ansible-default", "swift-default", "nix-default",
	}
	var got []string
	for _, d := range layout.Bundled() {
		got = append(got, d.Name)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
