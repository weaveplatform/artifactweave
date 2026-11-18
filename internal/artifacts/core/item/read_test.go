package item

import (
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

type readConfigStore struct {
	configdoc.ConfigStore
	err   error
	reads int
}

func (s *readConfigStore) Get(ctx context.Context, kind configdoc.ConfigKind, key string) (configdoc.ConfigDocument, error) {
	s.reads++
	if s.err != nil {
		return configdoc.ConfigDocument{}, s.err
	}
	return s.ConfigStore.Get(ctx, kind, key)
}

type readStore struct {
	*inmem.Store
	configs *readConfigStore
}

func (s readStore) Configs() configdoc.ConfigStore { return s.configs }

func TestUnitReadSessionCachesErrorsAndRefreshes(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown layout", true: "storage fault"}[failure], func(t *testing.T) {
			db := inmem.New()
			configs := &readConfigStore{ConfigStore: db.Configs()}
			boom := errors.New("configuration unavailable")
			if failure {
				configs.err = boom
			}
			s := &Service{Store: readStore{Store: db, configs: configs}}
			r := s.ReadSession()
			_, first := r.Layout(t.Context(), "missing")
			_, again := r.Env().Layouts.Layout(t.Context(), "missing")
			if first == nil || first != again || configs.reads != 1 {
				t.Fatal(first, again, configs.reads)
			}
			if failure && !errors.Is(again, boom) {
				t.Fatal(again)
			}
			if _, err := s.ReadSession().Layout(t.Context(), "missing"); err == nil || configs.reads != 2 {
				t.Fatal(err, configs.reads)
			}
			// Ordinary service calls never inherit a session's snapshot.
			for range 2 {
				_, _ = s.Layout(t.Context(), "missing")
			}
			if configs.reads != 4 {
				t.Fatal(configs.reads)
			}
		})
	}
}
