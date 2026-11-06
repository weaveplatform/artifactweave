package aql

import (
	"errors"
	"strings"
	"testing"
)

func TestUnitTransitiveTargetRequiresEveryAlternative(t *testing.T) {
	for _, criteria := range []string{
		`{"repo":"remote"}`,
		`{"$or":[{"$and":[{"repo":"remote","name":"a"}]},{"$and":[{"repo":{"$eq":"remote"},"name":"b"}]}]}`,
		`{"repo":"remote","$or":[{"name":"a"},{"name":"b"}]}`,
		`{"$and":[{"repo":"remote"},{"$or":[{"path":"."},{"name":"b"}]}]}`,
	} {
		q := mustParse(t, "items.find("+criteria+").transitive()")
		if got, err := q.TransitiveTarget(); err != nil || got != "remote" {
			t.Fatalf("%s: %q %v", criteria, got, err)
		}
	}
	for _, query := range []string{
		`items.find({}).transitive()`,
		`items.find({"repo":{"$match":"remote"}}).transitive()`,
		`items.find({"repo":""}).transitive()`,
		`items.find({"repo":"a","repo":"b"}).transitive()`,
		`items.find({"$or":[{"repo":"remote"},{"name":"b"}]}).transitive()`,
		`items.find({"$or":[{"repo":"remote"},{"repo":"other"}]}).transitive()`,
		`items.find({"repo":"remote"}).sort({"$asc":["name"]}).transitive()`,
		`items.find({"repo":"remote"}).offset(0).transitive()`,
		`items.find({"repo":"remote"}).include("stat").transitive()`,
		`items.find({"repo":"remote"}).include("artifact.module.build.name").transitive()`,
		`builds.find({"repo":"remote"}).transitive()`,
	} {
		q := mustParse(t, query)
		if _, err := q.TransitiveTarget(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s: %v", query, err)
		}
	}
}

func TestUnitPeerQueryPreservesCriteria(t *testing.T) {
	for _, criteria := range []string{
		`{"repo":"remote","name":{"$match":"*.txt"},"size":{"$gte":12},"created":{"$gt":"2026-01-01T12:34:56.123456789Z"}}`,
		`{"repo":"remote","modified":{"$last":"3days"},"created":{"$before":"2minutes"},"stat.downloads":{"$eq":null}}`,
		`{"repo":"remote","@color":{"$match":"b*"},"property.key":"color","property.value":{"$ne":"red"}}`,
		`{"repo":"remote","$msp":[{"@color":{"$match":"b*"}},{"@color":{"$ne":"black"}}]}`,
		`{"repo":"remote","artifact.module.build.name":"build","artifact.module.build.number":"12"}`,
		`{"repo":"remote","artifact.module.build.@quality":"good"}`,
		`{"repo":"remote","$msp":[{"artifact.module.build.@quality":"good"},{"artifact.module.build.@quality":{"$ne":"bad"}}]}`,
		`{"$or":[{"$and":[{"repo":"remote","path":"."}]},{"$and":[{"repo":"remote","path":{"$match":"sub/*"}}]}]}`,
	} {
		q := mustParse(t, "items.find("+criteria+").transitive()")
		got, err := q.PeerQuery("source", 19)
		if err != nil {
			t.Fatalf("%s: %v", criteria, err)
		}
		peer := mustParse(t, got)
		if target, err := peer.TransitiveTarget(); err != nil || target != "source" ||
			peer.Transitive ||
			peer.Limit != 19 {
			t.Fatalf("bad peer query %s: %v", got, err)
		}
		if strings.Contains(got, `"remote"`) {
			t.Fatalf("local repository leaked: %s", got)
		}
		// Re-parsing preserves the source constraint, including joined predicates.
		again, err := peer.PeerQuery("source", 19)
		if err != nil || !strings.Contains(again, `"source"`) {
			t.Fatalf("unstable roundtrip %s -> %s (%v)", got, again, err)
		}
		if strings.Contains(criteria, "123456789") && !strings.Contains(got, "123456789") {
			t.Fatal("date precision lost", got)
		}
	}
	q := mustParse(t, `items.find({"repo":"r"})`)
	for _, value := range []struct {
		repo  string
		limit int
	}{{"", 1}, {"s", -1}, {strings.Repeat("x", MaxLength), 1}} {
		if _, err := q.PeerQuery(value.repo, value.limit); err == nil {
			t.Fatal("accepted invalid peer bounds", value)
		}
	}
	q = mustParse(t, `items.find({})`)
	if _, err := q.PeerQuery("s", 1); err == nil {
		t.Fatal("unbounded query accepted")
	}
}
