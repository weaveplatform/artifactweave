package maven_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// file is a fixture file whose content changes with each deploy n.
func file(p string, n int) formattest.File {
	return formattest.File{Path: p, Body: fmt.Appendf(nil, "%s #%d", p, n)}
}

// In a Maven repository the suite's 1.0 is a release with a classifier, and
// its 2.0 a snapshot: a unique build, whose files belong to 2.0-SNAPSHOT,
// beside the snapshot folder's maven-metadata.xml, which belongs to no
// version.
func TestContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Maven,
		Version: func(p, v string, n int) formattest.Version {
			dir := "org/example/" + p + "/"
			if v == "1.0" {
				return formattest.Version{
					Package: "org.example:" + p, Version: v,
					Files: []formattest.File{
						file(dir+"1.0/"+p+"-1.0.pom", 0),
						file(dir+"1.0/"+p+"-1.0.jar", n),
						file(dir+"1.0/"+p+"-1.0-sources.jar", 0),
					},
				}
			}
			build := dir + "2.0-SNAPSHOT/" + p + "-2.0-20261017.090000-1"
			return formattest.Version{
				Package: "org.example:" + p, Version: "2.0-SNAPSHOT",
				Files: []formattest.File{
					file(build+".jar", n),
					file(build+".pom", 0),
					file(dir+"2.0-SNAPSHOT/maven-metadata.xml", n),
				},
				Linked: []string{build + ".jar", build + ".pom"},
			}
		},
	})
}

// Gradle's layout keeps a version's Ivy descriptor beside the module's
// version folders, so a version's files span two folders, and names an
// integration version by a 14-digit timestamp on the folder and the files.
func TestContractGradle(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:   repo.Gradle,
		Layout: repo.LayoutGradle,
		Version: func(p, v string, n int) formattest.Version {
			if v == "2.0" {
				v = "2.0-20261017090000"
			}
			dir := "org.example/" + p + "/"
			return formattest.Version{
				Package: "org.example:" + p, Version: v,
				Files: []formattest.File{
					file(dir+v+"/"+p+"-"+v+".jar", n),
					file(dir+v+"/"+p+"-"+v+"-sources.jar", 0),
					file(dir+"ivy-"+v+".xml", 0),
				},
			}
		},
	})
}

// Ivy's layout keeps each type of file in a folder of its own inside the
// version's.
func TestContractIvy(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:   repo.Ivy,
		Layout: repo.LayoutIvy,
		Version: func(p, v string, n int) formattest.Version {
			dir := "org.example/" + p + "/" + v + "/"
			return formattest.Version{
				Package: "org.example:" + p, Version: v,
				Files: []formattest.File{
					file(dir+"jars/"+p+"-"+v+".jar", n),
					file(dir+"ivys/ivy-"+v+".xml", 0),
				},
			}
		},
	})
}

// sbt-default reads only sbt plugins' paths, whose files carry no version
// in their names.
func TestContractSBT(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type:   repo.SBT,
		Layout: repo.LayoutSBT,
		Version: func(p, v string, n int) formattest.Version {
			dir := "org.example/" + p + "/scala_2.12/sbt_1.0/" + v + "/"
			return formattest.Version{
				Package: "org.example:" + p, Version: v,
				Files: []formattest.File{
					file(dir+"jars/"+p+".jar", n),
					file(dir+"ivys/ivy.xml", 0),
				},
			}
		},
	})
}
