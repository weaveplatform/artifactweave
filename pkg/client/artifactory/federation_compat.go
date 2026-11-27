package artifactory

// GetFederationStateDetails200JSONResponseBodyFederationsPriority retains the former
// generated inline response name after state reports became named schemas.
// Deprecated: use FederationStateListFederationsPriority.
type GetFederationStateDetails200JSONResponseBodyFederationsPriority = FederationStateListFederationsPriority

// GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatus retains the former
// generated inline response name after the connection row became a named schema.
// Deprecated: use FederationConnectionStatus.
type GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatus = FederationConnectionStatus

// Former inline connection status constants retained for source compatibility.
const (
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusDELAYED         = FederationConnectionStatusDELAYED
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusDISABLED        = FederationConnectionStatusDISABLED
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusERROR           = FederationConnectionStatusERROR
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusFULLSYNCRUNNING = FederationConnectionStatusFULLSYNCRUNNING
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusHEALTHY         = FederationConnectionStatusHEALTHY
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusNA              = FederationConnectionStatusNA
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusPENDINGFS       = FederationConnectionStatusPENDINGFS
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusREADONLY        = FederationConnectionStatusREADONLY
	GetFederationConnectionDetails200JSONResponseBodyMemberConnectionsStatusUNSUPPORTED     = FederationConnectionStatusUNSUPPORTED
)
