package artifactshttp

import (
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	agentpackageshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentpackages/http"
	aieditorextensionshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/aieditorextensions/http"
	ansiblehttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/ansible/http"
	bazelmoduleshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bazelmodules/http"
	bowerhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower/http"
	cargohttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cargo/http"
	chefhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef/http"
	cocoapodshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/cocoapods/http"
	composerhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/composer/http"
	conanhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conan/http"
	condahttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda/http"
	gemshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gems/http"
	gitlfshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/gitlfs/http"
	golanghttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/golang/http"
	helmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm/http"
	hexhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex/http"
	huggingfacehttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/huggingface/http"
	jetbrainspluginshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/jetbrainsplugins/http"
	luarockshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks/http"
	machinelearninghttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/machinelearning/http"
	nimmodelhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nimmodel/http"
	nixhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix/http"
	npmhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm/http"
	nugethttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget/http"
	pubhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub/http"
	puppethttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet/http"
	pypihttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi/http"
	skillshttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/skills/http"
	swifthttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/swift/http"
	terraformhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/http"
	vagranthttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/vagrant/http"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// ProtocolOptions gives the router kit the item routes' download, deploy
// and error mapping, so a protocol's files are redirected, counted, hooked
// and refused as theirs are.
func (s ArtifactoryAPI) ProtocolOptions() pkgroute.Options {
	return pkgroute.Options{
		Store: s.cfg.Store,
		Items: func() *item.Service { return s.Items(s.cfg.Store) },
		ServeItem: func(w http.ResponseWriter, r *http.Request, c repo.Config, it model.Item) {
			p, _ := kernel.FromContext(r.Context())
			s.ServeFile(w, r, s.cfg.Store, itemkit.Found{Repo: c, Item: it}, p)
		},
		DeployItem: func(w http.ResponseWriter, r *http.Request, c repo.Config, rest string) {
			s.deployEscapedItem(w, r, url.PathEscape(c.Key)+"/"+rest)
		},
		ItemError: s.ItemError,
		Created: func(w http.ResponseWriter, r *http.Request, it model.Item) {
			httpkit.WriteJSON(w, http.StatusCreated, itemkit.ItemCreated(r.Context(), it))
		},
		Fault: func(r *http.Request, err error) { httpkit.NoteCause(r.Context(), err) },
	}
}

// Protocols returns the package protocols with routers of their own, which
// the router mounts through the router kit (decision 0032) in this order,
// ahead of any test-only protocol. A format with a protocol adds its
// constructor here.
func Protocols(d Deps) []pkgroute.Protocol {
	return []pkgroute.Protocol{
		nixhttp.Protocol(nixhttp.Options{
			RemoteSources: d.RemoteSources,
			Authorizer:    d.Access.Authorizer,
			KeyPairs:      d.Access.Keys,
		}),
		puppethttp.Protocol(
			puppethttp.Options{Blobs: d.Blobs, RemoteSources: d.RemoteSources},
		),
		bazelmoduleshttp.Protocol(d.RemoteSources),
		bowerhttp.Protocol(bowerhttp.Options{RemoteSources: d.RemoteSources}),
		condahttp.Protocol(d.RemoteSources, d.FormatSettings.Conda),
		ansiblehttp.Protocol(d.RemoteSources),
		machinelearninghttp.Protocol(),
		chefhttp.Protocol(d.RemoteSources),
		aieditorextensionshttp.Protocol(d.RemoteSources),
		huggingfacehttp.Protocol(huggingfacehttp.Options{
			XetEnabled:    d.HuggingFaceXetEnabled,
			Authenticator: d.Access.Authenticator,
			Authorizer:    d.Access.Authorizer,
			Grants:        d.Access.Grants,
			RemoteSources: d.RemoteSources,
			Keys:          d.Keys,
		}),
		gitlfshttp.Protocol(gitlfshttp.Options{RemoteSources: d.RemoteSources}),
		agentpackageshttp.Protocol(),
		conanhttp.Protocol(conanhttp.Options{
			RemoteSources: d.RemoteSources, Tokens: d.Access.Tokens,
		}),
		cocoapodshttp.Protocol(cocoapodshttp.Options{RemoteSources: d.RemoteSources}),
		pypihttp.Protocol(
			pypihttp.Options{Format: d.FormatSettings.PyPI, RemoteSources: d.RemoteSources},
		),
		terraformhttp.Protocol(terraformhttp.Options{
			Authenticator:   d.Access.Authenticator,
			Authorizer:      d.Access.Authorizer,
			KeyPairs:        d.Access.Keys,
			Restrictions:    d.Access.Restrictions,
			Identity:        d.Access.Identity,
			AnonymousAccess: d.Access.AnonymousAccess,
			RemoteSources:   d.RemoteSources,
			Keys:            d.Keys,
		}),
		helmhttp.Protocol(
			helmhttp.Options{Format: d.FormatSettings.Helm, RemoteSources: d.RemoteSources},
		),
		hexhttp.Protocol(
			hexhttp.Options{RemoteSources: d.RemoteSources, KeyPairs: d.Access.Keys},
		),
		luarockshttp.Protocol(luarockshttp.Options{RemoteSources: d.RemoteSources}),
		cargohttp.Protocol(d.RemoteSources),
		composerhttp.Protocol(
			composerhttp.Options{Blobs: d.Blobs, RemoteSources: d.RemoteSources},
		),
		nimmodelhttp.Protocol(d.RemoteSources),
		jetbrainspluginshttp.Protocol(d.RemoteSources),
		golanghttp.Protocol(),
		npmhttp.Protocol(npmhttp.Options{
			RemoteSources:  d.RemoteSources,
			Accounts:       npmAccounts(d),
			MaxPublishSize: d.NPMMaxPublishSize,
			Format:         d.FormatSettings.NPM,
			KeyPairs:       d.Access.Keys,
		}),
		nugethttp.V3(d.RemoteSources),
		nugethttp.V2(d.RemoteSources),
		gemshttp.Protocol(gemshttp.Options{RemoteSources: d.RemoteSources}),
		pubhttp.Protocol(pubhttp.Options{Blobs: d.Blobs, RemoteSources: d.RemoteSources}),
		skillshttp.Protocol(),
		vagranthttp.Protocol(),
		swifthttp.Protocol(swifthttp.Options{RemoteSources: d.RemoteSources}),
	}
}

// npmAccounts is what npm's login and logout need from d: the access
// module's authenticator and token service. An absent one must stay a nil
// interface, which the npm handlers answer 501. d.Access holds nil
// interfaces, never typed nils, and a nil interface converts to a nil one,
// so they are assigned as they are.
func npmAccounts(d Deps) npmhttp.Accounts {
	return npmhttp.Accounts{
		Passwords:   d.Access.Authenticator,
		Tokens:      d.Access.Tokens,
		TokenExpiry: d.NPMLoginTokenExpiry,
	}
}
