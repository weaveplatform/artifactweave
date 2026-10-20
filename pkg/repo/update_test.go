package repo_test

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// A Helm repository's Enforce Layout fields are set only when it is
// created: an update that changes either is refused, naming it, and one that
// repeats the stored value, or leaves the field to its default, passes.
// Other package types keep no such field.
func TestValidateUpdate(t *testing.T) {
	yes, no := true, false
	helm := func(class repo.Class, unique, names *bool) *repo.Config {
		return &repo.Config{
			Key: "h", Class: class, PackageType: repo.Helm,
			ForceNonDuplicateChart: unique, ForceMetadataNameVersion: names,
		}
	}
	for _, c := range []struct {
		name            string
		stored, updated *repo.Config
		field           string
	}{
		{"unchanged", helm(repo.Local, &yes, &no), helm(repo.Local, &yes, &no), ""},
		{"unset is off", helm(repo.Local, nil, nil), helm(repo.Local, &no, &no), ""},
		{"duplicates turned off", helm(repo.Local, &yes, &no), helm(repo.Local, &no, &no), "forceNonDuplicateChart"},
		{"names turned on", helm(repo.Federated, &no, nil), helm(repo.Federated, &no, &yes), "forceMetadataNameVersion"},
		{"both, the first named", helm(repo.Local, nil, nil), helm(repo.Local, &yes, &yes), "forceNonDuplicateChart"},
		{
			"another package type",
			&repo.Config{Key: "g", Class: repo.Local, PackageType: repo.Generic},
			&repo.Config{Key: "g", Class: repo.Local, PackageType: repo.Generic, ForceNonDuplicateChart: &yes},
			"",
		},
	} {
		err := repo.ValidateUpdate(c.stored, c.updated)
		var refused repo.CreationOnlyError
		switch {
		case c.field == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.field == "":
		case !errors.As(err, &refused) || refused.Field != c.field || !errors.Is(err, repo.ErrInvalid):
			t.Errorf("%s: %v", c.name, err)
		case err.Error() != "The field '"+c.field+"' of a Helm repository can be set only when "+
			"the repository is created":
			t.Errorf("%s: %q", c.name, err)
		}
	}
}
