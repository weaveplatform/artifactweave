package operations_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-cmp/cmp"

	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/platform/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/platform/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/platform/gen/grid"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	pfedgen "github.com/weaveplatform/artifactweave/internal/platform/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/platform/gen/worker"
)

// mounts registers each spec's generated server under the base URL
// router.go gives it, with its 501 stubs as the implementation.
var mounts = map[string]func(chi.Router){
	"access": func(r chi.Router) {
		accessgen.HandlerWithOptions(accessgen.NewStrictHandler(accessgen.UnimplementedStrict{}, nil),
			accessgen.ChiServerOptions{BaseURL: "/access", BaseRouter: r})
	},
	"artifactory": func(r chi.Router) {
		artgen.HandlerWithOptions(artgen.NewStrictHandler(artgen.UnimplementedStrict{}, nil),
			artgen.ChiServerOptions{BaseURL: "/artifactory", BaseRouter: r})
	},
	"distribution": func(r chi.Router) {
		distributiongen.HandlerWithOptions(distributiongen.NewStrictHandler(distributiongen.UnimplementedStrict{}, nil),
			distributiongen.ChiServerOptions{BaseURL: "/distribution", BaseRouter: r})
	},
	"event": func(r chi.Router) {
		eventgen.HandlerWithOptions(eventgen.NewStrictHandler(eventgen.UnimplementedStrict{}, nil),
			eventgen.ChiServerOptions{BaseURL: "/event", BaseRouter: r})
	},
	"grid": func(r chi.Router) {
		gridgen.HandlerWithOptions(gridgen.NewStrictHandler(gridgen.UnimplementedStrict{}, nil),
			gridgen.ChiServerOptions{BaseURL: "/grid", BaseRouter: r})
	},
	"pfed": func(r chi.Router) {
		pfedgen.HandlerWithOptions(pfedgen.NewStrictHandler(pfedgen.UnimplementedStrict{}, nil),
			pfedgen.ChiServerOptions{BaseURL: "/pfed", BaseRouter: r})
	},
	"worker": func(r chi.Router) {
		workergen.HandlerWithOptions(workergen.NewStrictHandler(workergen.UnimplementedStrict{}, nil),
			workergen.ChiServerOptions{BaseURL: "/worker", BaseRouter: r})
	},
}

func TestUnitTableMatchesGeneratedServers(t *testing.T) {
	services := map[string]bool{}
	for _, op := range operations.Table {
		services[op.Service] = true
	}
	for s := range services {
		if mounts[s] == nil {
			t.Errorf("service %s has no generated server in this test; add it", s)
		}
	}
	for service, mount := range mounts {
		r := chi.NewRouter()
		mount(r)
		var served []string
		if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			served = append(served, method+" "+route)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var listed []string
		for _, op := range operations.Table {
			if op.Service == service && !op.Handwritten {
				listed = append(listed, op.Method+" "+op.Pattern)
			}
		}
		sort.Strings(served)
		sort.Strings(listed)
		if diff := cmp.Diff(served, listed); diff != "" {
			t.Errorf("%s: generated routes (-) and table rows (+) differ:\n%s", service, diff)
		}
		if len(listed) == 0 {
			t.Errorf("%s: no generated operation in the table", service)
		}
	}
}

var parameterRE = regexp.MustCompile(`\{[^{}]+\}`)

func TestUnitIndexFindsEveryOperation(t *testing.T) {
	x, err := operations.NewIndex(operations.Table)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range operations.Table {
		// A value no static segment of any spec uses, so the parameter
		// route is the one chi matches.
		path := parameterRE.ReplaceAllString(want.Pattern, "zz-value")
		got, ok := x.Find(want.Method, path)
		if !ok || got.OperationID != want.OperationID || got.Service != want.Service {
			t.Errorf("%s %s found %v %+v, want %s", want.Method, path, ok, got, want.OperationID)
		}
	}
}

func TestUnitIndexFind(t *testing.T) {
	table := []operations.Operation{
		{Service: "s", Method: "GET", Pattern: "/s/api/repositories/{repoKey}", OperationID: "byKey"},
		{Service: "s", Method: "GET", Pattern: "/s/api/repositories/batch", OperationID: "batch"},
		{Service: "s", Method: "POST", Pattern: "/s/api/build/append/{buildName}/{buildNumber}", OperationID: "append"},
	}
	x, err := operations.NewIndex(table)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, target, want string
	}{
		{"GET", "/s/api/repositories/batch", "batch"},
		{"GET", "/s/api/repositories/libs", "byKey"},
		{"POST", "/s/api/build/append/a%2Fb/1", "append"},
		{"POST", "/s/api/build/append/a/b/1", ""},
		{"PUT", "/s/api/repositories/libs", ""},
		{"GET", "/s/unknown", ""},
		{"BREW", "/s/api/repositories/libs", ""},
	}
	for _, c := range cases {
		got, ok := x.FindRequest(httptest.NewRequest(c.method, c.target, nil))
		if ok != (c.want != "") || got.OperationID != c.want {
			t.Errorf("%s %s found %v %q, want %q", c.method, c.target, ok, got.OperationID, c.want)
		}
	}
}

func TestUnitNewIndexErrors(t *testing.T) {
	op := operations.Operation{Method: "GET", Pattern: "/a/{b}", OperationID: "a"}
	if _, err := operations.NewIndex([]operations.Operation{op, op}); err == nil ||
		!strings.Contains(err.Error(), "twice") {
		t.Fatalf("a row listed twice: %v", err)
	}
	if _, err := operations.NewIndex([]operations.Operation{{Method: "GET", Pattern: "no-slash"}}); err == nil {
		t.Fatal("expected chi to refuse a pattern without a leading slash")
	}
}
