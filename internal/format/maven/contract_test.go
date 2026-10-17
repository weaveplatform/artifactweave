package maven_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// file is a fixture file whose content changes with each deploy n.
func file(p string, n int) formattest.File {
	return formattest.File{Path: p, Body: fmt.Appendf(nil, "%s #%d", p, n)}
}

// pomFile is a POM of org.example:p at version v that the POM checks
// accept at path.
func pomFile(path, p, v string) formattest.File {
	return formattest.File{Path: path, Body: fmt.Appendf(nil,
		`<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion>`+
			`<groupId>org.example</groupId><artifactId>%s</artifactId><version>%s</version>`+
			`<name>%s library</name></project>`, p, v, p)}
}

// In a Maven repository the suite's 1.0 is a release with a classifier, and
// its 2.0 a snapshot: a unique build, whose files belong to 2.0-SNAPSHOT,
// beside the artifact's maven-metadata.xml, which belongs to no version.
// (The client's snapshot-level metadata is dropped with 202, so it is no
// file to deploy.)
func TestContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		// Every write to a package queues its metadata, whatever the layout.
		Work: []string{maven.KindMetadata},
		Type: repo.Maven,
		Version: func(p, v string, n int) formattest.Version {
			dir := "org/example/" + p + "/"
			if v == "1.0" {
				return formattest.Version{
					Package: "org.example:" + p, Version: v,
					Files: []formattest.File{
						pomFile(dir+"1.0/"+p+"-1.0.pom", p, "1.0"),
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
					pomFile(build+".pom", p, "2.0-SNAPSHOT"),
					file(dir+"maven-metadata.xml", n),
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
		// Every write to a package queues its metadata, whatever the layout.
		Work:   []string{maven.KindMetadata},
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
		// Every write to a package queues its metadata, whatever the layout.
		Work:   []string{maven.KindMetadata},
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
		// Every write to a package queues its metadata, whatever the layout.
		Work:   []string{maven.KindMetadata},
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
