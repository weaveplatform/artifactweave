// Bridge (P9d): declared in migration/access/api; PRO-02 deletes it.

package authz

import accessapi "github.com/weaveplatform/artifactweave/migration/access/api"

type (
	Repo    = accessapi.Repo
	Subject = accessapi.Subject
)
