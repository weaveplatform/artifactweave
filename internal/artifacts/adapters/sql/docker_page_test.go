package sql_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	artsql "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/sql"
	artstoretest "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// TestAccDockerPagesContract runs the image layout page reader's contract
// on SQLite and PostgreSQL: each engine folds and orders the indexes'
// expressions itself, and both must order as layout.Compare does.
func TestAccDockerPagesContract(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			artstoretest.RunDockerPages(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

// TestAccDockerPagesAtScale reads a late page of a 5000-tag image and of a
// 5000-image repository on SQLite and PostgreSQL, and keeps each query's
// plan: a seek on items_docker_tags or items_docker_images, with no sort of
// the image's or repository's manifests.
func TestAccDockerPagesAtScale(t *testing.T) {
	sqltest.RunModule(t, mods, artsql.New,
		func(t *testing.T, newStore func(*testing.T) *artsql.Store) {
			dockerPagesAtScale(t, newStore(t))
		}, sqltest.WithPostgres())
}

// TestAccDockerPagesClosedDatabase: both page reads report the driver's
// error on a closed pool.
func TestAccDockerPagesClosedDatabase(t *testing.T) {
	s := open(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pages := s.Items().DockerPages()
	sqltest.Fails(t, map[string]error{
		"DockerPages.ManifestFolders": sqltest.Err(pages.ManifestFolders(ctx,
			port.DockerFolderQuery{RepoKey: "r", Image: "app", Limit: 1})),
		"DockerPages.ImageManifests": sqltest.Err(pages.ImageManifests(ctx,
			port.DockerImageQuery{RepoKey: "r", Limit: 1})),
	})
}

const dockerScale = 5000

func dockerPagesAtScale(t *testing.T, s *artsql.Store) {
	t.Helper()
	ctx := t.Context()
	put := func(ctx context.Context, full string) error {
		_, err := s.Items().Put(ctx, model.Item{RepoKey: "docker-local", Name: full})
		return err //nolint:wrapcheck // the test reports it
	}
	err := s.Tx(ctx, func(ctx context.Context) error {
		for i := range dockerScale {
			for _, full := range []string{
				fmt.Sprintf("app/v%04d/manifest.json", i),
				fmt.Sprintf("app/v%04d/sha256__%064d", i, i),
				fmt.Sprintf("image-%04d/v1/manifest.json", i),
			} {
				if err := put(ctx, full); err != nil {
					return fmt.Errorf("seed %s: %w", full, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, "ANALYZE"); err != nil {
		t.Fatal(err)
	}
	pages := s.Items().DockerPages()
	tagQuery := port.DockerFolderQuery{
		RepoKey: "docker-local", Image: "app", AfterFold: "v4988", After: "v4988", Limit: 11,
	}
	start := time.Now()
	tags, err := pages.ManifestFolders(ctx, tagQuery)
	if err != nil || len(tags) != 11 || tags[0] != "v4989" || tags[10] != "v4999" {
		t.Fatalf("late tag page = %v, %v", tags, err)
	}
	t.Logf("%s: 11 of %d tags after v4988: %s", s.Dialect().Name, dockerScale, time.Since(start))
	imageQuery := port.DockerImageQuery{RepoKey: "docker-local", After: "image-4988", Limit: 11}
	start = time.Now()
	images, err := pages.ImageManifests(ctx, imageQuery)
	if err != nil || len(images) != 11 || images[0].Image != "image-4989" ||
		images[10].Image != "image-4999" {
		t.Fatalf("late image page = %v, %v", images, err)
	}
	t.Logf("%s: 11 of %d images after image-4988: %s", s.Dialect().Name, dockerScale,
		time.Since(start))

	query, args := artsql.DockerFolderQuery(s, tagQuery)
	dockerPlan(t, s, "tags", "items_docker_tags", query, args)
	query, args = artsql.DockerImageQuery(s, imageQuery)
	dockerPlan(t, s, "images", "items_docker_images", query, args)
}

// dockerPlan explains query and requires it to read index in order, with
// no sort: a sort would read every manifest of the image or repository.
func dockerPlan(t *testing.T, s *artsql.Store, what, index, query string, args []any) {
	t.Helper()
	ctx := t.Context()
	explain := "EXPLAIN QUERY PLAN "
	if s.Dialect().Dollar {
		explain = "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF, SUMMARY OFF) "
	}
	rows, err := s.DB().QueryContext(ctx, s.Dialect().Rebind(explain+query), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if s.Dialect().Dollar {
			err = rows.Scan(&line)
		} else {
			var id, parent, unused int
			err = rows.Scan(&id, &parent, &unused, &line)
		}
		if err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: late %s page plan:\n%s", s.Dialect().Name, what, plan.String())
	text := plan.String()
	if !strings.Contains(text, index) || strings.Contains(text, "TEMP B-TREE") ||
		strings.Contains(text, "Sort") {
		t.Fatalf("the late %s page does not seek %s in order:\n%s", what, index, text)
	}
}
