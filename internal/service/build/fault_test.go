package build

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errBoom = errors.New("boom")

// faulty fails the store operation named fail with errBoom. A name with a
// colon fails only for that key or path ("repo:libs-local").
type faulty struct {
	*inmem.Store
	fail string
}

func (f faulty) Builds() store.BuildStore { return faultyBuilds{f.Store.Builds(), f.fail} }
func (f faulty) Items() store.ItemStore   { return faultyItems{f.Store.Items(), f.fail} }
func (f faulty) Repos() store.RepoStore   { return faultyRepos{f.Store.Repos(), f.fail} }

type faultyBuilds struct {
	store.BuildStore
	fail string
}

func (f faultyBuilds) Put(ctx context.Context, b types.Build) (types.Build, error) {
	if f.fail == "put" {
		return b, errBoom
	}
	return f.BuildStore.Put(ctx, b)
}

func (f faultyBuilds) Replace(ctx context.Context, b types.Build) error {
	if f.fail == "replace" {
		return errBoom
	}
	return f.BuildStore.Replace(ctx, b)
}

func (f faultyBuilds) Get(
	ctx context.Context, project, name, number string, started time.Time,
) (types.Build, error) {
	if f.fail == "get" {
		return types.Build{}, errBoom
	}
	return f.BuildStore.Get(ctx, project, name, number, started)
}

func (f faultyBuilds) ByID(ctx context.Context, id int64) (types.Build, error) {
	if f.fail == "byid" {
		return types.Build{}, errBoom
	}
	return f.BuildStore.ByID(ctx, id)
}

func (f faultyBuilds) Runs(
	ctx context.Context, project, name string, p store.Page,
) ([]types.BuildRun, string, error) {
	if f.fail == "runs" {
		return nil, "", errBoom
	}
	return f.BuildStore.Runs(ctx, project, name, p)
}

func (f faultyBuilds) Names(
	ctx context.Context, project string, p store.Page,
) ([]types.BuildName, string, error) {
	if f.fail == "names" {
		return nil, "", errBoom
	}
	return f.BuildStore.Names(ctx, project, p)
}

func (f faultyBuilds) DeleteNumbers(
	ctx context.Context, project, name string, numbers []string,
) ([]types.BuildRun, error) {
	if f.fail == "deletenumbers" {
		return nil, errBoom
	}
	return f.BuildStore.DeleteNumbers(ctx, project, name, numbers)
}

func (f faultyBuilds) DeleteAll(
	ctx context.Context,
	project, name string,
) ([]types.BuildRun, error) {
	if f.fail == "deleteall" {
		return nil, errBoom
	}
	return f.BuildStore.DeleteAll(ctx, project, name)
}

func (f faultyBuilds) AddPromotion(
	ctx context.Context, p types.BuildPromotion,
) (types.BuildPromotion, error) {
	if f.fail == "addpromotion" {
		return p, errBoom
	}
	return f.BuildStore.AddPromotion(ctx, p)
}

func (f faultyBuilds) PutRetention(ctx context.Context, r types.BuildRetention) error {
	if f.fail == "putretention" {
		return errBoom
	}
	return f.BuildStore.PutRetention(ctx, r)
}

func (f faultyBuilds) Rename(ctx context.Context, project, from, to string) error {
	if f.fail == "rename" {
		return errBoom
	}
	return f.BuildStore.Rename(ctx, project, from, to)
}

func (f faultyBuilds) Promotions(ctx context.Context, id int64) ([]types.BuildPromotion, error) {
	if f.fail == "promotions" {
		return nil, errBoom
	}
	return f.BuildStore.Promotions(ctx, id)
}

type faultyItems struct {
	store.ItemStore
	fail string
}

func (f faultyItems) Get(ctx context.Context, k string, c bool, p string) (types.Item, error) {
	if f.fail == "item" || f.fail == "item:"+p {
		return types.Item{}, errBoom
	}
	return f.ItemStore.Get(ctx, k, c, p)
}

func (f faultyItems) Put(ctx context.Context, it types.Item) (types.Item, error) {
	if f.fail == "itemput" && !it.Folder {
		return it, errBoom
	}
	return f.ItemStore.Put(ctx, it)
}

func (f faultyItems) Delete(
	ctx context.Context, k string, c bool, p string,
) (store.DeleteResult, error) {
	if f.fail == "delete" || f.fail == "delete:"+p {
		return store.DeleteResult{}, errBoom
	}
	return f.ItemStore.Delete(ctx, k, c, p)
}

func (f faultyItems) Descendants(
	ctx context.Context, k string, c bool, p string, pg store.Page,
) ([]types.Item, string, error) {
	if f.fail == "descendants" {
		return nil, "", errBoom
	}
	return f.ItemStore.Descendants(ctx, k, c, p, pg)
}

func (f faultyItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	if f.fail == "properties" {
		return nil, errBoom
	}
	return f.ItemStore.Properties(ctx, id)
}

func (f faultyItems) PatchProperties(
	ctx context.Context, id int64, set types.Properties, remove []string, at time.Time,
) error {
	if f.fail == "patch" {
		return errBoom
	}
	return f.ItemStore.PatchProperties(ctx, id, set, remove, at)
}

func (f faultyItems) FindByChecksum(
	ctx context.Context, c types.Checksums, p store.Page,
) ([]types.Item, string, error) {
	if f.fail == "find" {
		return nil, "", errBoom
	}
	return f.ItemStore.FindByChecksum(ctx, c, p)
}

type faultyRepos struct {
	store.RepoStore
	fail string
}

func (f faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if f.fail == "repo" || f.fail == "repo:"+key {
		return repo.Config{}, errBoom
	}
	return f.RepoStore.Get(ctx, key)
}

// TestFaults passes every store failure on: nothing is half done, and no
// failure reads as a missing build or a refusal.
func TestFaults(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		fail string
		op   func(s *Service) error
	}{
		{"repo", publishApp},
		{"get", publishApp},
		{"item", publishApp},
		{"put", publishApp},
		{"itemput", publishApp},
		{"get", func(s *Service) error {
			_, err := s.Run(ctx, "", "seeded", "1", time.Time{}, everyone)
			return err
		}},
		{"runs", func(s *Service) error {
			_, err := s.Runs(ctx, "", "seeded", everyone)
			return err
		}},
		{"names", func(s *Service) error { _, err := s.Names(ctx, "", everyone); return err }},
		{"repo", appendSeeded},
		{"get", appendSeeded},
		{"replace", appendSeeded},
		{"itemput", appendSeeded},
		{"repo", renameSeeded},
		{"runs", renameSeeded},
		{"rename", renameSeeded},
		{"byid", renameSeeded},
		{"replace", renameSeeded},
		{"itemput", renameSeeded},
		{"delete", renameSeeded},
		{"repo", deleteSeeded(false)},
		{"runs", deleteSeeded(true)},
		{"byid", deleteSeeded(true)},
		{"deletenumbers", deleteSeeded(false)},
		{"deleteall", func(s *Service) error {
			_, err := s.Delete(ctx, Delete{Name: "seeded", All: true}, everyone)
			return err
		}},
		{"item", deleteSeeded(false)},
		{"item:seeded", deleteSeeded(false)},
		{"descendants", deleteSeeded(false)},
		{"delete", deleteSeeded(false)},
		{"delete:seeded", deleteSeeded(false)},
		{"find", deleteSeeded(true)},
		{"properties", deleteSeeded(true)},
		{"repo:libs-local", deleteSeeded(true)},
		{"delete:app/a.bin", deleteSeeded(true)},
		{"find", func(s *Service) error {
			_, err := s.Artifacts(ctx, "", "seeded", "1", everyone)
			return err
		}},
		{"find", searchSeeded},
		{"properties", searchSeeded},
		{"runs", findSeeded("")},
		{"promotions", findSeeded("Released")},
		{"byid", findSeeded("")},
	} {
		w := newWorld(t)
		w.seed("seeded")
		s := &Service{Store: faulty{w.db, tc.fail}, Blobs: w.blobs}
		if err := tc.op(s); !errors.Is(err, errBoom) {
			t.Errorf("failing %s: %v, want the store's error", tc.fail, err)
		}
	}
}

func publishApp(s *Service) error {
	_, err := s.Publish(
		context.Background(),
		"",
		[]byte(doc("app", "1", started0)),
		"admin",
		everyone,
	)
	return err
}

func appendSeeded(s *Service) error {
	return s.Append(context.Background(), Append{
		Name: "seeded", Number: "1", Modules: []byte(`[{"id":"m"}]`),
	}, everyone)
}

func renameSeeded(s *Service) error {
	return s.Rename(context.Background(), "", "seeded", "renamed", "admin", everyone)
}

func deleteSeeded(artifacts bool) func(s *Service) error {
	return func(s *Service) error {
		_, err := s.Delete(context.Background(), Delete{
			Name: "seeded", Numbers: []string{"1"}, Artifacts: artifacts,
		}, everyone)
		return err
	}
}

// TestPromotionFaults passes every store failure of a promotion, a
// retention and a publish's retention on.
func TestPromotionFaults(t *testing.T) {
	ctx := context.Background()
	promote := func(p Promotion) func(*Service) error {
		return func(s *Service) error {
			p.Name, p.Number, p.By = "app", "1", "admin"
			_, err := s.Promote(ctx, p, everyone)
			return err
		}
	}
	tagged := types.Properties{"k": {"v"}}
	discard := func(s *Service) error {
		_, err := s.Discard(ctx, Discard{Name: "app", Retention: types.BuildRetention{Count: 1}},
			everyone)
		return err
	}
	for _, tc := range []struct {
		fail string
		op   func(s *Service) error
	}{
		{"get", promote(Promotion{})},
		{"repo:release-local", promote(Promotion{TargetRepo: "release-local"})},
		{"repo:release-local", promote(Promotion{TargetRepo: "release"})},
		{"find", promote(Promotion{Artifacts: true})},
		{"properties", promote(Promotion{Artifacts: true})},
		{"repo:libs-local", promote(Promotion{Artifacts: true})},
		{"find", promote(Promotion{Dependencies: true})},
		{"itemput", promote(Promotion{TargetRepo: "release-local", Artifacts: true})},
		{"item:app/a.bin", promote(Promotion{Artifacts: true, Properties: tagged})},
		{"patch", promote(Promotion{Artifacts: true, Properties: tagged})},
		{"addpromotion", promote(Promotion{Status: "released"})},
		{"runs", discard},
		{"deletenumbers", discard},
		{"putretention", func(s *Service) error {
			_, err := s.Publish(ctx, "", []byte(`{"name":"app","number":"2","started":"`+
				at(9)+`","buildRetention":{"count":1}}`), "admin", everyone)
			return err
		}},
	} {
		w := promotionWorld(t)
		w.put("libs-local", false, "app/a.bin", "a.bin", buildProps("app", "1"))
		w.put("libs-local", false, "dep/d.jar", "d.jar", nil)
		w.publish(doc("app", "1", started0, fileModule([]string{"a.bin"}, []string{"d.jar"})))
		w.publish(doc("app", "0", at(-1)))
		s := &Service{Store: faulty{w.db, tc.fail}, Blobs: w.blobs}
		if err := tc.op(s); !errors.Is(err, errBoom) {
			t.Errorf("failing %s: %v, want the store's error", tc.fail, err)
		}
	}
}

func findSeeded(status string) func(s *Service) error {
	return func(s *Service) error {
		_, err := s.Find(context.Background(), "", "seeded", Latest, status, everyone)
		return err
	}
}

func searchSeeded(s *Service) error {
	ctx := context.Background()
	b, err := s.Store.Builds().Get(ctx, "", "seeded", "1", time.Time{})
	if err != nil {
		return err
	}
	_, err = s.SearchArtifacts(ctx, b, nil, everyone)
	return err
}
