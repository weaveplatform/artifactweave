package acceptance

import "github.com/cucumber/godog"

// registerChefCatalog adds Knife's catalog commands. They live apart from
// chef_steps_test.go, whose fingerprint the client recipe evidence lock
// records, so that the Chef install scenarios' evidence does not change.
func (w *world) registerChefCatalog(sc *godog.ScenarioContext) {
	sc.Step(`^I list the Chef cookbooks in "([^"]*)"$`, w.listChef)
	sc.Step(`^I search the Chef cookbooks in "([^"]*)" for "([^"]*)"$`, w.searchChef)
}

// listChef runs knife-art's list, which asks the catalog for
// items=9999999 (Knife 19.0.134, supermarket_list.rb), the query the
// registry refused with 400 before (#1305).
func (w *world) listChef(key string) error {
	if err := w.chefConfig(key); err != nil {
		return err
	}
	return w.runChef("artifactory", "list")
}

// searchChef runs knife-art's search, which sends its argument as q with
// items=9999999 (supermarket_search.rb).
func (w *world) searchChef(key, query string) error {
	if err := w.chefConfig(key); err != nil {
		return err
	}
	return w.runChef("artifactory", "search", query)
}
