package remote

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

func TestAccRegistryAuthenticationFeature(t *testing.T) {
	var fixture *registryFixture
	suite := godog.TestSuite{Name: "upstream-registry-authentication", Options: &godog.Options{
		Format:   "progress",
		Paths:    []string{"testdata/registry_auth.feature"},
		TestingT: t,
		Strict:   true,
	}, ScenarioInitializer: func(sc *godog.ScenarioContext) {
		sc.Step(
			`^a controlled registry for upstream user "([^"]*)"$`,
			func(user string) { fixture = newRegistryFixture(t, user) },
		)
		sc.Step(
			`^the source pulls "([^"]*)" twice using a local registry bearer token$`,
			func(image string) error {
				for range 2 {
					if err := registryGet(t.Context(), fixture.registry, image); err != nil {
						return err
					}
				}
				return nil
			},
		)
		sc.Step(
			`^the token service received (one|two) exchanges? for "([^"]*)"$`,
			func(count, scope string) error {
				want := int32(1)
				if count == "two" {
					want = 2
				}
				if fixture.exchanges.Load() != want {
					return fmt.Errorf("exchange count %d, want %d", fixture.exchanges.Load(), want)
				}
				fixture.tokensMu.Lock()
				defer fixture.tokensMu.Unlock()
				for _, got := range fixture.scopes {
					if got != scope {
						return fmt.Errorf("unexpected scope %q", got)
					}
				}
				return nil
			},
		)
		sc.Step(`^the upstream token expires$`, func() { fixture.now.Add(61) })
		sc.Step(`^the token service received separate scopes for both images$`, func() error {
			fixture.tokensMu.Lock()
			defer fixture.tokensMu.Unlock()
			if strings.Join(
				fixture.scopes,
				"|",
			) != "repository:team/image:pull|repository:other/image:pull" {
				return fmt.Errorf("unexpected scopes %q", fixture.scopes)
			}
			return nil
		})
	}}
	if suite.Run() != 0 {
		t.Fatal("upstream registry authentication feature failed")
	}
}
