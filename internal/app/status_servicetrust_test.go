package app

// The service_trust pairing operations (#1926). A redeem or an unpair
// that reaches another instance runs in test/server/federation; these rows
// hold what one instance answers alone: the administrator boundary, 7.x's
// 404 for an unknown use case, the 501 of a use case a later phase serves,
// and the refusals that need no peer.

const (
	stIssue       = "POST /artifactory/api/v1/service_trust/pairing/{usecase}"
	stRedeem      = "PUT /artifactory/api/v1/service_trust/pairing/{usecase}"
	stUnpair      = "DELETE /artifactory/api/v1/service_trust/pairing/{usecase}"
	stPairings    = "GET /artifactory/api/v1/service_trust/pairings/{usecase}"
	stRevoke      = "DELETE /artifactory/api/v1/service_trust/revoke/{usecase}"
	stExchange    = "POST /artifactory/api/v1/service_trust/exchange"
	stAccessIssue = "POST /access/api/v1/service_trust/pairing/{useCase}"

	stPairing     = "/artifactory/api/v1/service_trust/pairing/"
	stList        = "/artifactory/api/v1/service_trust/pairings/"
	stRevokePath  = "/artifactory/api/v1/service_trust/revoke/"
	stAccessPair  = "/access/api/v1/service_trust/pairing/"
	stFederated   = "federated-repo/*"
	stUnknown     = "artifactory-cold"
	stTokenBody   = `{"pairing_token":"not-a-token"}`
	stPeerRedeem  = "POST /artifactweave/peer/v1/pair"
	stPeerCancel  = "DELETE /artifactweave/peer/v1/pair"
	stPeerPair    = "/artifactweave/peer/v1/pair"
	stBaseURLBody = `{"base_url":"https://absent.example"}`
)

var serviceTrustRoutes = map[string]bool{
	stIssue: true, stRedeem: true, stUnpair: true, stPairings: true,
	stRevoke: true, stExchange: true, stAccessIssue: true,
}

var serviceTrustStatusPart = statusPart{
	handwritten: serviceTrustRoutes,
	// The peer API's /pair, which a peer signs and no user credential
	// reaches (peerRequest). A signed request runs in
	// test/server/federation; one without the protocol header is refused
	// before its signature is read, whoever sends it.
	generated: map[string]bool{stPeerRedeem: true, stPeerCancel: true},
	rows: func() []statusCase {
		return []statusCase{
			{stIssue, stPairing + stFederated, "admin", "", 200},
			{stIssue, stPairing + "federated-repo%2Fr", "admin", "", 200},
			{stIssue, stPairing + stFederated, "", "", 401},
			{stIssue, stPairing + stFederated, "user", "", 403},
			{stIssue, stPairing + stUnknown, "admin", "", 404},
			{stIssue, stPairing + "artifactory-cold/ns", "admin", "", 404},
			// 7.x checks the role before it reads the use case, so an
			// unknown use case tells a non-administrator nothing.
			{stIssue, stPairing + stUnknown, "user", "", 403},
			// A use case a later phase serves keeps today's 501, whoever
			// asks.
			{stIssue, stPairing + "distribution", "user", "", 501},
			{stIssue, stPairing + "access-federation", "admin", "", 501},
			{stRedeem, stPairing + stFederated, "admin", stTokenBody, 400},
			{stRedeem, stPairing + stFederated, "admin", "", 400},
			{stRedeem, stPairing + stFederated, "", stTokenBody, 401},
			{stRedeem, stPairing + stFederated, "user", stTokenBody, 403},
			{stRedeem, stPairing + stUnknown, "admin", stTokenBody, 404},
			{stRedeem, stPairing + "mission-control", "admin", stTokenBody, 501},
			{stUnpair, stPairing + stFederated, "admin", stBaseURLBody, 400},
			{stUnpair, stPairing + stFederated + "?force=true", "admin", stBaseURLBody, 400},
			{stUnpair, stPairing + stFederated, "admin", "{}", 400},
			{stUnpair, stPairing + stFederated, "", stBaseURLBody, 401},
			{stUnpair, stPairing + stFederated, "user", stBaseURLBody, 403},
			{stUnpair, stPairing + stUnknown, "admin", stBaseURLBody, 404},
			{stUnpair, stPairing + "bridge", "admin", stBaseURLBody, 501},
			{stPairings, stList + stFederated, "admin", "", 200},
			{stPairings, stList + stFederated, "", "", 401},
			{stPairings, stList + stFederated, "user", "", 403},
			{stPairings, stList + stUnknown, "admin", "", 404},
			{stPairings, stList + "platform-federation", "admin", "", 501},
			{stRevoke, stRevokePath + stFederated, "admin", "", 200},
			{stRevoke, stRevokePath + stFederated, "", "", 401},
			{stRevoke, stRevokePath + stFederated, "user", "", 403},
			{stRevoke, stRevokePath + stUnknown, "admin", "", 404},
			{stRevoke, stRevokePath + "distribution", "admin", "", 501},
			{stExchange, "/artifactory/api/v1/service_trust/exchange", "admin", "{}", 400},
			{stExchange, "/artifactory/api/v1/service_trust/exchange", "user", "{}", 400},
			{stAccessIssue, stAccessPair + stFederated, "admin", "", 200},
			{stAccessIssue, stAccessPair + "federated-repo%2Fr", "admin", "", 200},
			{stAccessIssue, stAccessPair + stFederated, "", "", 401},
			{stAccessIssue, stAccessPair + stFederated, "user", "", 403},
			{stAccessIssue, stAccessPair + stUnknown, "admin", "", 404},
			{stAccessIssue, stAccessPair + "mission-control", "admin", "", 501},
			{stPeerRedeem, stPeerPair, "", "{}", 400},
			{stPeerRedeem, stPeerPair, "admin", "{}", 400},
			{stPeerCancel, stPeerPair + "?use_case=federated-repo%2F%2A", "", "", 400},
		}
	},
}

var serviceTrustMatrixPart = matrixPart{
	rows: func() []matrixRow {
		return []matrixRow{
			row(stIssue, stPairing+stFederated, "", administrators, 200),
			row(stRedeem, stPairing+stFederated, stTokenBody, administrators, 400),
			row(stUnpair, stPairing+stFederated, stBaseURLBody, administrators, 400),
			row(stPairings, stList+stFederated, "", administrators, 200),
			row(stRevoke, stRevokePath+stFederated, "", administrators, 200),
			row(stExchange, "/artifactory/api/v1/service_trust/exchange", "{}", anyone, 400),
			row(stAccessIssue, stAccessPair+stFederated, "", administrators, 200),
			// A user credential means nothing to the peer API, a failing one
			// included.
			{route: stPeerRedeem, path: stPeerPair, body: "{}", who: noCredentials, ok: 400, open: true},
			{route: stPeerCancel, path: stPeerPair + "?use_case=federated-repo%2F%2A", who: noCredentials, ok: 400, open: true},
		}
	},
}
