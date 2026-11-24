package configdoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The descriptor's two base URL scalars, each one document under
// BaseURLKey whose only field is named after the scalar. The configuration
// module writes them (PATCH, config apply, bootstrap, PUT baseUrl); the
// artifacts and federation modules read the federated one through
// FederatedBase, which is why the kinds live here rather than in the
// configuration module.
const (
	ConfigURLBase              ConfigKind = "url_base"
	ConfigFederatedRepoURLBase ConfigKind = "federated_repo_url_base"
	BaseURLKey                 string     = "default"
	URLBaseField               string     = "urlBase"
	FederatedRepoURLBaseField  string     = "federatedRepoUrlBase"
)

// StoredScalar reads the string field of the document (kind, BaseURLKey);
// ok is false when there is none. kernel.ErrStore when the document holds no
// such string.
func StoredScalar(ctx context.Context, configs ConfigStore, kind ConfigKind, field string) (string, bool, error) {
	doc, err := configs.Get(ctx, kind, BaseURLKey)
	if errors.Is(err, kernel.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", field, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(doc.Document, &fields); err != nil {
		return "", false, fmt.Errorf("stored %s: %w", field, err)
	}
	s, ok := fields[field].(string)
	if !ok || s == "" {
		return "", false, fmt.Errorf("stored %s: %w", field, kernel.ErrStore)
	}
	return s, true, nil
}

// FederatedBase is the base URL of this instance's own federated repository
// members and of a federated pairing token: federatedRepoUrlBase, or urlBase
// when that is empty, as 7.161.26's getFederatedRepoUrlBaseOrDefault gives
// (FederatedMemberUtils.java:49), or fallback, the installation's configured
// base URL, which stands in for urlBase as the router's does. "" means none
// is configured, which a federated repository with members is refused for.
func FederatedBase(ctx context.Context, configs ConfigStore, fallback string) (string, error) {
	for _, s := range []struct {
		kind  ConfigKind
		field string
	}{{ConfigFederatedRepoURLBase, FederatedRepoURLBaseField}, {ConfigURLBase, URLBaseField}} {
		v, ok, err := StoredScalar(ctx, configs, s.kind, s.field)
		if err != nil {
			return "", err
		}
		if ok {
			return strings.TrimRight(v, "/"), nil
		}
	}
	return strings.TrimRight(strings.TrimSpace(fallback), "/"), nil
}
