package terraform_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The Terraform registry's share of the status table (decision 0040). Its
// format (#552) adds no route: what it adds to the repositories API is the
// terraformType and key pair rules, whose refusals are 400s in that API's
// own envelope.

// terraformRepos are the Terraform repositories the rows act on: a MODULE
// one, a PROVIDER one signing with a GPG pair, and one without
// terraformType.
var terraformRepos = []repo.Config{
	{
		Key: "tf-module", Class: repo.Local, PackageType: repo.Terraform,
		TerraformType: repo.TerraformModule,
	},
	{
		Key: "tf-provider", Class: repo.Local, PackageType: repo.Terraform,
		TerraformType: repo.TerraformProvider, PrimaryKeyPairRef: "tf-gpg",
	},
	{Key: "tf-untyped", Class: repo.Local, PackageType: repo.Terraform},
}

// seedTerraform creates terraformRepos and the key pairs they name: a GPG
// pair and an RSA one.
func seedTerraform(t *testing.T, db store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, k := range []types.Keypair{
		{Name: "tf-gpg", Type: types.KeypairGPG, Alias: "tf-gpg"},
		{Name: "tf-rsa", Type: types.KeypairRSA, Alias: "tf-rsa"},
	} {
		if err := db.Keys().CreateKeypair(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range terraformRepos {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
}

var terraformStatusPart = statusPart{
	rows: func() []statusCase {
		const (
			create  = "PUT /artifactory/api/repositories/{repoKey}"
			update  = "POST /artifactory/api/repositories/{repoKey}"
			bCreate = "PUT /artifactory/api/v2/repositories/batch"
			bUpdate = "POST /artifactory/api/v2/repositories/batch"
			r       = "/artifactory/api/repositories/"
			batch   = "/artifactory/api/v2/repositories/batch"
			tf      = `{"rclass":"local","packageType":"terraform",`
		)
		return []statusCase{
			// MODULE or PROVIDER, in any case.
			{create, r + "tf-new-module", "admin", tf + `"terraformType":"module"}`, 200},
			{
				create, r + "tf-new-provider", "admin",
				tf + `"terraformType":"Provider","primaryKeyPairRef":"tf-gpg"}`, 200,
			},
			{create, r + "tf-bad-type", "admin", tf + `"terraformType":"registry"}`, 400},
			{
				bCreate,
				batch,
				"admin",
				`[{"key":"tf-batch","rclass":"local","packageType":"terraform","terraformType":"x"}]`,
				400,
			},
			// A PROVIDER repository's key pairs are GPG pairs.
			{
				create, r + "tf-rsa-provider", "admin",
				tf + `"terraformType":"PROVIDER","primaryKeyPairRef":"tf-rsa"}`, 400,
			},
			{
				create, r + "tf-rsa-secondary", "admin",
				`{"rclass":"federated","packageType":"terraform","terraformType":"PROVIDER",` +
					`"primaryKeyPairRef":"tf-gpg","secondaryKeyPairRef":"tf-rsa"}`, 400,
			},
			{
				create, r + "tf-rsa-module", "admin",
				tf + `"terraformType":"MODULE","primaryKeyPairRef":"tf-rsa"}`, 200,
			},
			{update, r + "tf-provider", "admin", `{"secondaryKeyPairRef":"tf-rsa"}`, 400},
			// terraformType cannot change once set; it may be set where
			// there was none.
			{update, r + "tf-module", "admin", `{"terraformType":"PROVIDER"}`, 400},
			{update, r + "tf-module", "admin", `{"terraformType":null}`, 400},
			{bUpdate, batch, "admin", `[{"key":"tf-module","terraformType":"provider"}]`, 400},
			{update, r + "tf-module", "admin", `{"terraformType":"module","description":"x"}`, 200},
			{update, r + "tf-untyped", "admin", `{"terraformType":"provider"}`, 200},
		}
	},
	seed: func(t *testing.T, db store.Store, _ blob.Store) {
		t.Helper()
		seedTerraform(t, db)
	},
}

// WorkQueue fails queueing under "enqueue".
func (f faultyStore) WorkQueue() store.WorkQueue {
	if f.fail["enqueue"] {
		return failingWork{f.Store.WorkQueue()}
	}
	return f.Store.WorkQueue()
}

type failingWork struct{ store.WorkQueue }

func (failingWork) Enqueue(context.Context, string, string, []byte, time.Time) error {
	return errBoom
}

// The repositories API's terraformType and key pair refusals name the
// field in its envelope; terraformType is echoed upper-case; and an update
// that sets terraformType where there was none queues the repository's
// reindex with the update, by single and batch update alike (decision
// 0040).
func TestAccTerraformTypeOnTheRepositoriesAPI(t *testing.T) {
	w := newRepoWorld(t)
	seedTerraform(t, w.db)
	ctx := context.Background()
	// A field's validation names the field after the configuration prefix; a
	// change to a field the repository takes once (repo.ValidateUpdate) has
	// its own message, as Helm's Enforce Layout fields do.
	for _, c := range []struct {
		method, path, body, want string
		prefixed                 bool
	}{
		{
			"PUT", "/artifactory/api/repositories/tf-x",
			`{"rclass":"local","packageType":"terraform","terraformType":"registry"}`,
			`terraformType: \"REGISTRY\" is not one of MODULE, PROVIDER`, true,
		},
		{
			"PUT", "/artifactory/api/repositories/tf-x",
			`{"rclass":"local","packageType":"terraform","terraformType":"provider",` +
				`"primaryKeyPairRef":"tf-rsa"}`,
			"primaryKeyPairRef: names the RSA key pair 'tf-rsa', but a PROVIDER Terraform " +
				"repository takes GPG key pairs only", true,
		},
		{
			"POST", "/artifactory/api/repositories/tf-module", `{"terraformType":"provider"}`,
			"The field 'terraformType' of a Terraform repository cannot change once set", false,
		},
	} {
		rec := w.do(c.method, c.path, "admin", c.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) ||
			c.prefixed && !strings.HasPrefix(
				rec.Body.String(),
				`{"errors":[{"message":"invalid repository configuration: `,
			) {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, rec.Code, rec.Body)
		}
	}
	queued := func(key string) bool {
		_, err := w.db.WorkQueue().Get(ctx, format.KindReindex, key)
		return err == nil
	}
	wantStatus(t, w.do("POST", "/artifactory/api/repositories/tf-module", "admin",
		`{"terraformType":"Module","description":"x"}`), http.StatusOK)
	if queued("tf-module") {
		t.Fatal("an update that kept terraformType queued a reindex")
	}
	// A failed queue fails the update with it.
	w.fail["enqueue"] = true
	wantStatus(t, w.do("POST", "/artifactory/api/repositories/tf-untyped", "admin",
		`{"terraformType":"provider"}`), http.StatusInternalServerError)
	if c := w.stored("tf-untyped"); c.TerraformType != "" {
		t.Fatalf("the update outlived its reindex: %s", c.TerraformType)
	}
	delete(w.fail, "enqueue")
	wantStatus(t, w.do("POST", "/artifactory/api/repositories/tf-untyped", "admin",
		`{"terraformType":"provider"}`), http.StatusOK)
	if c := w.stored("tf-untyped"); c.TerraformType != repo.TerraformProvider ||
		!queued("tf-untyped") {
		t.Fatalf("%s, queued %v", c.TerraformType, queued("tf-untyped"))
	}
	var got map[string]any
	jsonBody(t, w.do("GET", "/artifactory/api/repositories/tf-untyped", "admin", ""), &got)
	if got["terraformType"] != "PROVIDER" {
		t.Fatalf("echoed %v", got["terraformType"])
	}
	w.create(repo.Config{Key: "tf-later", Class: repo.Local, PackageType: repo.Terraform})
	wantStatus(t, w.do("POST", "/artifactory/api/v2/repositories/batch", "admin",
		`[{"key":"tf-later","terraformType":"module"},{"key":"tf-module","description":"y"}]`),
		http.StatusOK)
	if !queued("tf-later") || queued("tf-module") {
		t.Fatalf("batch: tf-later queued %v, tf-module %v", queued("tf-later"), queued("tf-module"))
	}
}
