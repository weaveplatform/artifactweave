package maven

import (
	"cmp"
	"context"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// KindSnapshots applies a package's maxUniqueSnapshots, keyed by
// groupId:artifactId (decision 0036, #283). It is periodic work: a deploy
// marks the package, and the runtime runs the marked packages on a timer
// (work.Queue.HandleEvery), as Artifactory 7.x marks the builds over the
// limit on deploy and deletes them in a background process every five
// minutes.
const KindSnapshots = "maven.snapshots"

// cleanupCandidate reports a file the cleanup counts, as 6.20's
// SnapshotVersionsRetriever filters them: one of an integration version,
// and, under a layout whose folder and file revisions differ, one whose
// file revision is not its folder's. That leaves out Maven's non-unique
// -SNAPSHOT files, which every build overwrites.
func cleanupCandidate(l *layout.Layout, co layout.Coordinates) bool {
	d := l.Definition()
	return co.Integration() &&
		(d.FolderIntegrationRevisionRegExp == d.FileIntegrationRevisionRegExp ||
			co.FolderItegRev != co.FileItegRev)
}

// queueCleanup marks co's package for its cleanup when a file the cleanup
// counts lands in a local repository with a limit, as Artifactory's
// IntegrationCleanerInterceptor notes the folder of every such file.
func queueCleanup(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r reader,
	it types.Item,
	co Coordinates,
	indexed bool,
) error {
	if !indexed || it.Cached || c.Class != repo.Local || format.UniqueSnapshots(c) == 0 ||
		!cleanupCandidate(r.l, co.Coordinates) {
		return nil
	}
	return enqueue(ctx, env, KindSnapshots, c.Key, co.Package)
}

// uniqueBuild is one build the cleanup counts: the files of one base
// revision that share a file revision, such as every file of
// 1.0-20261017.090000-3.
type uniqueBuild struct {
	rev string
	// created is the earliest creation of its files, which dates it.
	created time.Time
	// files are its files' paths with their classifiers.
	files []classified
}

type classified struct{ path, classifier string }

// uniqueRev is a unique snapshot's file revision, such as
// 20261017.090000-3, with its build number.
var uniqueRev = regexp.MustCompile(`^[0-9]{8}.[0-9]{6}-([0-9]{1,9})$`)

// older orders builds oldest first: by date, then, for builds of one
// moment, by Maven's build number, so build 10 follows build 9, or else by
// the revision's text.
func older(a, b *uniqueBuild) int {
	if c := a.created.Compare(b.created); c != 0 {
		return c
	}
	am, bm := uniqueRev.FindStringSubmatch(a.rev), uniqueRev.FindStringSubmatch(b.rev)
	if am != nil && bm != nil {
		an, _ := strconv.Atoi(am[1])
		bn, _ := strconv.Atoi(bm[1])
		if an != bn {
			return cmp.Compare(an, bn)
		}
	}
	return cmp.Compare(a.rev, b.rev)
}

// cleanSnapshots applies package name's maxUniqueSnapshots in c with
// Artifactory's rule (IntegrationCleanupServiceImpl, research 27 section
// 7): for each base revision, the counted files are grouped into builds by
// file revision, and for each classifier, while more builds have files of
// it than the limit allows, the oldest build's files of that classifier
// are deleted. The limit is format.UniqueSnapshots, read as the run finds
// it. The deletes go through the item service (Env.Remove), so the trash
// can, the index and the metadata queue see them, and the folders they
// empty are pruned.
func cleanSnapshots(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	r reader,
	name string,
) error {
	limit := format.UniqueSnapshots(c)
	if limit == 0 {
		return nil
	}
	vs, byVersion, err := Versions(ctx, env.Store, c, name)
	if err != nil {
		return err
	}
	builds := map[[2]string]*uniqueBuild{}
	for _, v := range vs {
		for _, it := range byVersion[v.ID] {
			co, ok := r.read(it.FullPath())
			if !ok || !cleanupCandidate(r.l, co.Coordinates) {
				continue
			}
			k := [2]string{co.BaseRev, co.FileItegRev}
			b := builds[k]
			if b == nil {
				b = &uniqueBuild{rev: co.FileItegRev}
				builds[k] = b
			}
			b.created = earliest(b.created, it.Created)
			b.files = append(b.files, classified{it.FullPath(), co.Classifier})
		}
	}
	// Each base revision's builds that hold a file of each classifier.
	byClassifier := map[[2]string][]*uniqueBuild{}
	for k, b := range builds {
		seen := map[string]bool{}
		for _, f := range b.files {
			if !seen[f.classifier] {
				seen[f.classifier] = true
				ck := [2]string{k[0], f.classifier}
				byClassifier[ck] = append(byClassifier[ck], b)
			}
		}
	}
	var doomed []string
	for _, ck := range slices.SortedFunc(maps.Keys(byClassifier), func(a, b [2]string) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]))
	}) {
		list := byClassifier[ck]
		slices.SortFunc(list, older)
		for _, b := range list[:max(len(list)-limit, 0)] {
			for _, f := range b.files {
				if f.classifier == ck[1] {
					doomed = append(doomed, f.path)
				}
			}
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	slices.Sort(doomed)
	return env.Remove(ctx, c, doomed, Deriver) //nolint:wrapcheck // the item service's own
}
