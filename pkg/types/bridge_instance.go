// Bridge (P9d): declared in migration/platform/instance; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/instance"

var ErrInvalidInstanceID = instance.ErrInvalidInstanceID

const ProtocolVersion = instance.ProtocolVersion

var ValidateInstanceID = instance.ValidateInstanceID

type InstanceIdentity = instance.InstanceIdentity
