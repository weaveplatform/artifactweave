package replication_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type pushWorld struct {
	*world
	push              replication.Push
	source            map[string]artifactsapi.TransferItem
	bodies            map[string]string
	target            map[string]port.PushTargetItem
	targetBodies      map[string]string
	transportRevision string
	sourceError       error
	putError          error
	putBeforeError    error
	propertyError     error
	deleteError       error
	putAfter          func()
	deleteAfter       func()
	puts, deletes     int
	patches, closes   int
	wrongChecksum     bool
}

func newPushWorld(t *testing.T) *pushWorld {
	t.Helper()
	w := &pushWorld{
		world: newWorld(), source: map[string]artifactsapi.TransferItem{}, bodies: map[string]string{},
		target: map[string]port.PushTargetItem{}, targetBodies: map[string]string{}, transportRevision: "transport-v1",
	}
	must(t, w.service.Replace(t.Context(), "local", replication.Input{URL: ptr("https://target.example/artifactory/destination"), Enabled: ptr(true)}))
	w.push = replication.Push{
		Store: w.db, Configurations: w.service, Source: pushSource{w}, Connections: pushConnections{w},
		Now: func() time.Time { return time.Unix(100, 0) },
		Restore: func(ctx context.Context, ref kernel.CredentialReference) (context.Context, error) {
			if w.sourceError != nil {
				return nil, w.sourceError
			}
			return accessapi.WithCaller(ctx, ref.Principal(), &accessapitest.Decider{}), nil
		},
	}
	return w
}

func pushRepository() artifactsapi.TransferRepository {
	return artifactsapi.TransferRepository{Key: "local", Incarnation: "source-lifetime", Generation: "source-generation", PackageType: repo.Generic}
}

func pushSums(body string) kernel.Checksums {
	digest := sha256.Sum256([]byte(body))
	return kernel.Checksums{SHA256: hex.EncodeToString(digest[:])}
}

func pushView(source artifactsapi.TransferItem, properties bool) artifactsapi.TransferItem {
	source = kernel.Clone(source)
	source.PropertiesIncluded, source.Version = properties, ""
	if !properties {
		source.Properties = nil
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		panic(err) // fixture values contain only JSON-representable metadata
	}
	source.Version = pushSums(string(encoded)).SHA256
	return source
}

func (w *pushWorld) addSource(path, body string) {
	id := int64(len(w.source) + 1)
	if old, ok := w.source[path]; ok {
		id = old.ID
	}
	w.source[path] = artifactsapi.TransferItem{
		Repository: pushRepository(), ID: id, Path: path,
		Size: int64(len(body)), Actual: pushSums(body), Properties: map[string][]string{"quality": {"source"}},
	}
	w.bodies[path] = body
}

func (w *pushWorld) addTarget(path, body string) {
	w.target[path] = port.PushTargetItem{
		Path: path, Snapshot: model.PushTargetSnapshot{Size: int64(len(body)), Actual: pushSums(body)},
		Properties: map[string][]string{"obsolete": {"remove"}},
	}
	w.targetBodies[path] = body
}

func pushContext(ctx context.Context) context.Context {
	return accessapi.WithCaller(ctx, kernel.Principal{Name: "reader", TokenID: "original-token"}, &accessapitest.Decider{})
}

func (w *pushWorld) start(t *testing.T, root string) model.PushRun {
	t.Helper()
	target := stored(t, w.world, "local")
	return storekit.Get[model.PushRun](t)(w.push.Start(pushContext(t.Context()), replication.PushRequest{
		ID: "logical-run", TargetKey: target.Key, TargetRevision: target.Revision, Root: root,
	}, nil))
}

func (w *pushWorld) finish(t *testing.T, run model.PushRun) model.PushRun {
	t.Helper()
	for range 100 {
		if run.Phase.Terminal() {
			return run
		}
		run = storekit.Get[model.PushRun](t)(w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil }))
	}
	t.Fatal("bounded push did not complete")
	return model.PushRun{}
}

type pushSource struct{ w *pushWorld }

func (s pushSource) Repository(context.Context, string) (artifactsapi.TransferRepository, error) {
	return pushRepository(), s.w.sourceError
}

func (s pushSource) Items(
	_ context.Context,
	_ artifactsapi.TransferRepository,
	selection artifactsapi.TransferSelection,
	page kernel.Page,
) ([]artifactsapi.TransferItem, string, error) {
	var rows []artifactsapi.TransferItem
	for _, source := range s.w.source {
		if selection.Root == "" || source.Path == selection.Root || strings.HasPrefix(source.Path, selection.Root+"/") {
			rows = append(rows, pushView(source, selection.Properties))
		}
	}
	slices.SortFunc(rows, func(a, b artifactsapi.TransferItem) int { return strings.Compare(a.Path, b.Path) })
	return pushFixturePage(rows, page, func(row artifactsapi.TransferItem) string { return row.Path })
}

func pushFixturePage[T any](rows []T, page kernel.Page, path func(T) string) ([]T, string, error) {
	key, err := page.Key(1)
	if err != nil {
		return nil, "", err
	}
	if key != nil {
		rows = slices.DeleteFunc(rows, func(row T) bool { return path(row) <= key[0] })
	}
	result, cursor := kernel.Trim(rows, page.Limit, func(row T) []string { return []string{path(row)} })
	return result, cursor, nil
}

func (s pushSource) WithCurrent(ctx context.Context, expected artifactsapi.TransferItem, effect func(context.Context) error) error {
	return s.w.db.Tx(ctx, func(ctx context.Context) error {
		current, exists := s.w.source[expected.Path]
		if !exists {
			return kernel.ErrNotFound
		}
		if !reflect.DeepEqual(pushView(current, expected.PropertiesIncluded), expected) {
			return kernel.ErrConflict
		}
		if effect != nil {
			return effect(ctx)
		}
		return nil
	})
}

func (s pushSource) WithAbsent(ctx context.Context, _ artifactsapi.TransferRepository, path string, effect func(context.Context) error) error {
	return s.w.db.Tx(ctx, func(ctx context.Context) error {
		if _, exists := s.w.source[path]; exists {
			return kernel.ErrConflict
		}
		if effect != nil {
			return effect(ctx)
		}
		return nil
	})
}

type pushBody struct {
	*bytes.Reader
	close func()
}

func (b pushBody) Close() error { b.close(); return nil }

func (s pushSource) Open(ctx context.Context, expected artifactsapi.TransferItem) (io.ReadSeekCloser, error) {
	if s.w.db.InTx(ctx) {
		return nil, kernel.ErrInvalid
	}
	if err := s.WithCurrent(ctx, expected, nil); err != nil {
		return nil, err
	}
	return pushBody{Reader: bytes.NewReader([]byte(s.w.bodies[expected.Path])), close: func() { s.w.closes++ }}, nil
}

type pushConnections struct{ w *pushWorld }

func (c pushConnections) Connection(ctx context.Context, _ model.Replication) (port.PushConnection, error) {
	if !c.w.db.InTx(ctx) {
		return nil, kernel.ErrStore
	}
	return pushConnection{c.w, c.w.transportRevision}, nil
}

type pushConnection struct {
	w        *pushWorld
	revision string
}

func (c pushConnection) Revision() string { return c.revision }

func (c pushConnection) outside(ctx context.Context) error {
	if c.w.db.InTx(ctx) {
		return fmt.Errorf("network called in a metadata transaction: %w", kernel.ErrStore)
	}
	return nil
}

func (c pushConnection) Inspect(ctx context.Context, path string) (port.PushTargetItem, error) {
	if err := c.outside(ctx); err != nil {
		return port.PushTargetItem{}, err
	}
	row, found := c.w.target[path]
	if !found {
		return port.PushTargetItem{}, kernel.ErrNotFound
	}
	return kernel.Clone(row), nil
}

func (c pushConnection) Put(ctx context.Context, source artifactsapi.TransferItem, body io.ReadSeeker, before port.PushMutationFence) error {
	if err := c.outside(ctx); err != nil {
		return err
	}
	if c.w.putBeforeError != nil {
		return c.w.putBeforeError
	}
	content, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if err := before(ctx); err != nil {
		return err
	}
	c.w.puts++
	c.w.addTarget(source.Path, string(content))
	if c.w.wrongChecksum {
		row := c.w.target[source.Path]
		row.Snapshot.Actual = pushSums("corrupt")
		c.w.target[source.Path] = row
	}
	if c.w.putAfter != nil {
		c.w.putAfter()
	}
	return c.w.putError
}

func (c pushConnection) Properties(ctx context.Context, path string, properties map[string][]string, before port.PushMutationFence) error {
	if err := c.outside(ctx); err != nil {
		return err
	}
	if err := before(ctx); err != nil {
		return err
	}
	c.w.patches++
	row := c.w.target[path]
	row.Properties = kernel.Clone(properties)
	c.w.target[path] = row
	return c.w.propertyError
}

func (c pushConnection) Files(ctx context.Context, _ string, page kernel.Page) ([]port.PushTargetItem, string, error) {
	if err := c.outside(ctx); err != nil {
		return nil, "", err
	}
	var rows []port.PushTargetItem
	for _, row := range c.w.target {
		rows = append(rows, kernel.Clone(row))
	}
	slices.SortFunc(rows, func(a, b port.PushTargetItem) int { return strings.Compare(a.Path, b.Path) })
	return pushFixturePage(rows, page, func(row port.PushTargetItem) string { return row.Path })
}

func (c pushConnection) Delete(ctx context.Context, path string, expected model.PushTargetSnapshot, before port.PushMutationFence) error {
	if err := c.outside(ctx); err != nil {
		return err
	}
	current, found := c.w.target[path]
	if !found || current.Snapshot != expected {
		return kernel.ErrConflict
	}
	if err := before(ctx); err != nil {
		return err
	}
	c.w.deletes++
	delete(c.w.target, path)
	delete(c.w.targetBodies, path)
	if c.w.deleteAfter != nil {
		c.w.deleteAfter()
	}
	return c.w.deleteError
}

func TestUnitPushDriverCopiesAndReconcilesOnlySelectedPaths(t *testing.T) {
	w := newPushWorld(t)
	must(t, w.service.Update(t.Context(), "local", replication.Input{SyncDeletes: ptr(true)}))
	w.addSource("selected/one.pkg", "source content")
	w.addSource("private/retained.pkg", "private content")
	w.addTarget("selected/stale.pkg", "stale")
	w.addTarget("private/unrelated.pkg", "unrelated")
	run := w.finish(t, w.start(t, "selected"))
	if run.Phase != model.PushComplete || w.puts != 1 || w.patches != 1 || w.deletes != 1 || w.closes != 1 {
		t.Fatal("push did not perform exact selected effects", run.Phase, w.puts, w.patches, w.deletes, w.closes)
	}
	if w.targetBodies["selected/one.pkg"] != "source content" || w.targetBodies["private/unrelated.pkg"] != "unrelated" {
		t.Fatal("push lost selected bytes or changed an unrelated destination")
	}
	if _, found := w.target["private/retained.pkg"]; found {
		t.Fatal("push transferred an unselected source")
	}
	rows, _, err := w.db.PushProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 10})
	must(t, err)
	if len(rows) != 2 || rows[0].Outcome != model.PushSucceeded || rows[1].Outcome != model.PushSucceeded {
		t.Fatal("verified push results missing", rows)
	}
}

var errPushLostAck = errors.New("fixture connection ended after target commit")

func TestUnitPushDriverRecoversAcknowledgementLoss(t *testing.T) {
	for _, action := range []string{"upload", "properties", "delete"} {
		t.Run(action, func(t *testing.T) {
			w := newPushWorld(t)
			switch action {
			case "upload":
				w.addSource("selected/one.pkg", "content")
				w.putError = errPushLostAck
			case "properties":
				w.addSource("selected/one.pkg", "content")
				w.addTarget("selected/one.pkg", "content")
				w.propertyError = errPushLostAck
			default:
				must(t, w.service.Update(t.Context(), "local", replication.Input{SyncDeletes: ptr(true)}))
				w.addTarget("selected/stale.pkg", "stale")
				w.deleteError = errPushLostAck
			}
			run := w.start(t, "selected")
			var err error
			for range 10 {
				run, err = w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil })
				if err != nil {
					break
				}
			}
			storekit.WantErr(t, err, errPushLostAck)
			w.putError, w.propertyError, w.deleteError = nil, nil, nil
			// Rebuild the driver value: durable evidence, not process fields,
			// determines whether the already committed mutation is repeated.
			w.push = replication.Push{Store: w.db, Configurations: w.service, Source: pushSource{w}, Connections: pushConnections{w}, Restore: w.push.Restore, Now: w.push.Now}
			run = w.finish(t, run)
			if run.Phase != model.PushComplete || action == "upload" && w.puts != 1 || action == "properties" && w.patches != 1 || action == "delete" && w.deletes != 1 {
				t.Fatal("acknowledgement retry repeated a committed mutation", run.Phase, w.puts, w.deletes)
			}
		})
	}
}

func TestUnitPushDriverRecordsPermanentTargetRefusalWithoutSuccess(t *testing.T) {
	for _, refusal := range []port.PushTargetFailure{port.PushTargetUnauthorized, port.PushTargetForbidden} {
		t.Run(refusal.Reason(), func(t *testing.T) {
			w := newPushWorld(t)
			w.addSource("selected/one.pkg", "original")
			w.putBeforeError = refusal
			run := w.finish(t, w.start(t, "selected"))
			row := storekit.Get[model.PushItem](t)(w.db.PushProgress().Item(t.Context(), run.Reference, model.PushUpload, "selected/one.pkg"))
			if row.Outcome != model.PushItemFailed || row.Reason != refusal.Reason() || w.puts != 0 || len(w.target) != 0 {
				t.Fatal("target refusal fabricated a successful mutation", row.Outcome, row.Reason, w.puts)
			}
		})
	}
}

func TestUnitPushDriverNeverAcknowledgesObsoleteSourceOrWrongChecksum(t *testing.T) {
	for _, change := range []string{"source overwrite", "checksum"} {
		t.Run(change, func(t *testing.T) {
			w := newPushWorld(t)
			w.addSource("selected/one.pkg", "original")
			if change == "source overwrite" {
				w.putAfter = func() { w.addSource("selected/one.pkg", "replacement") }
			} else {
				w.wrongChecksum = true
			}
			run := w.finish(t, w.start(t, "selected"))
			row := storekit.Get[model.PushItem](t)(w.db.PushProgress().Item(t.Context(), run.Reference, model.PushUpload, "selected/one.pkg"))
			if row.Outcome == model.PushSucceeded || row.Phase != model.PushItemComplete {
				t.Fatal("obsolete/corrupt content was acknowledged", row)
			}
			if change == "checksum" && (row.Outcome != model.PushItemFailed || row.Reason != string(port.PushTargetChecksum)) {
				t.Fatal("checksum refusal was hidden", row.Outcome, row.Reason)
			}
		})
	}
}

func TestUnitPushDriverFencesConfigAndTransportChangesDuringTransfer(t *testing.T) {
	for _, change := range []string{"disable", "transport"} {
		t.Run(change, func(t *testing.T) {
			w := newPushWorld(t)
			w.addSource("selected/one.pkg", "original")
			w.putAfter = func() {
				if change == "transport" {
					w.transportRevision = "transport-v2"
				} else {
					must(t, w.service.Update(t.Context(), "local", replication.Input{Enabled: ptr(false)}))
				}
			}
			run := w.start(t, "selected")
			_, err := w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil })
			must(t, err)
			_, err = w.push.Step(t.Context(), run.Reference, func(context.Context) error { return nil })
			storekit.WantErr(t, err, kernel.ErrConflict)
			row := storekit.Get[model.PushItem](t)(w.db.PushProgress().Item(t.Context(), run.Reference, model.PushUpload, "selected/one.pkg"))
			if row.Phase != model.PushIntent || row.Outcome != "" || w.puts != 1 {
				t.Fatal("revoked transfer advanced its checkpoint")
			}
		})
	}
}

func TestUnitPushDriverAdmissionAndFinalFenceRollback(t *testing.T) {
	w := newPushWorld(t)
	w.addSource("selected/one.pkg", "content")
	target := stored(t, w.world, "local")
	request := replication.PushRequest{ID: "logical-run", TargetKey: target.Key, TargetRevision: target.Revision, Root: "selected"}
	_, err := w.push.Start(pushContext(t.Context()), request, func(context.Context, model.PushRun) error { return errPushLostAck })
	storekit.WantErr(t, err, errPushLostAck)
	run := w.start(t, "selected")
	fences := 0
	_, err = w.push.Step(t.Context(), run.Reference, func(context.Context) error {
		fences++
		if fences == 2 {
			return errPushLostAck
		}
		return nil
	})
	storekit.WantErr(t, err, errPushLostAck)
	if fences != 2 {
		t.Fatal("did not reach final publication fence after creating intent/cursor")
	}
	current := storekit.Get[model.PushRun](t)(w.db.PushProgress().Run(t.Context(), run.Reference))
	if current != run {
		t.Fatal("failed final fence advanced traversal")
	}
	rows, _, err := w.db.PushProgress().Items(t.Context(), run.Reference, false, kernel.Page{Limit: 10})
	must(t, err)
	if len(rows) != 0 || w.puts != 0 || w.deletes != 0 {
		t.Fatal("failed final fence published an intent/effect")
	}
}

func TestUnitPushDriverDoesNotAcceptMissingStatisticsCapability(t *testing.T) {
	w := newPushWorld(t)
	must(t, w.service.Update(t.Context(), "local", replication.Input{SyncStatistics: ptr(true)}))
	target := stored(t, w.world, "local")
	_, err := w.push.Start(pushContext(t.Context()), replication.PushRequest{ID: "statistics", TargetKey: target.Key, TargetRevision: target.Revision}, nil)
	storekit.WantErr(t, err, replication.ErrPushStatistics)
	if w.puts != 0 || w.deletes != 0 {
		t.Fatal("unsupported statistics silently started a transfer")
	}
}
