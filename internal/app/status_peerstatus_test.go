package app

// The peer API's status read (#1949) and federated repository
// configuration (#1930). Only a paired peer's signed request reaches them;
// test/peers reads status between running instances and
// TestAccPeerOperationsSignedBetweenInstances sends both signed. These
// rows hold what one instance answers a request without the protocol
// header: 400, before its signature is read, whoever sends it.

const (
	stPeerStatus     = "GET /artifactweave/peer/v1/status"
	stPeerStatusURL  = "/artifactweave/peer/v1/status"
	stPeerRepoConfig = "PUT /artifactweave/peer/v1/repositories/{repoKey}"
	stPeerRepoURL    = "/artifactweave/peer/v1/repositories/libs-fed"
	stPeerRepoBody   = `{"configuration":{"key":"libs-fed","rclass":"federated"}}`
)

var peerStatusStatusPart = statusPart{
	generated: map[string]bool{stPeerStatus: true, stPeerRepoConfig: true},
	rows: func() []statusCase {
		return []statusCase{
			{stPeerStatus, stPeerStatusURL, "", "", 400},
			{stPeerStatus, stPeerStatusURL, "admin", "", 400},
			{stPeerRepoConfig, stPeerRepoURL, "", stPeerRepoBody, 400},
			{stPeerRepoConfig, stPeerRepoURL, "admin", stPeerRepoBody, 400},
		}
	},
}

var peerStatusMatrixPart = matrixPart{
	rows: func() []matrixRow {
		return []matrixRow{
			// A user credential means nothing to the peer API, a failing one
			// included.
			{route: stPeerStatus, path: stPeerStatusURL, who: noCredentials, ok: 400, open: true},
			{route: stPeerRepoConfig, path: stPeerRepoURL, body: stPeerRepoBody, who: noCredentials, ok: 400, open: true},
		}
	},
}
