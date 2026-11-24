package app

// The peer API's stream and blob operations (#1928). Only a paired peer's
// signed request reaches them; two instances exchanging entries run in
// test/server/federation. These rows hold what one instance answers a
// request without the protocol header: 400, before its signature is read,
// whoever sends it.

const (
	stPeerPush    = "POST /artifactweave/peer/v1/streams/{stream}/events"
	stPeerPull    = "GET /artifactweave/peer/v1/streams/{stream}/events"
	stPeerBlob    = "GET /artifactweave/peer/v1/blobs/{sha256}"
	stPeerEvents  = "/artifactweave/peer/v1/streams/test:s/events"
	stPeerBlobURL = "/artifactweave/peer/v1/blobs/0000000000000000000000000000000000000000000000000000000000000000"
	stPeerBatch   = `{"origin":"jfrt@x","from_seq":1,"events":[]}`
)

var peerStreamsStatusPart = statusPart{
	generated: map[string]bool{stPeerPush: true, stPeerPull: true, stPeerBlob: true},
	rows: func() []statusCase {
		return []statusCase{
			{stPeerPush, stPeerEvents, "", stPeerBatch, 400},
			{stPeerPush, stPeerEvents, "admin", stPeerBatch, 400},
			{stPeerPull, stPeerEvents, "", "", 400},
			{stPeerBlob, stPeerBlobURL, "", "", 400},
		}
	},
}

var peerStreamsMatrixPart = matrixPart{
	rows: func() []matrixRow {
		return []matrixRow{
			// A user credential means nothing to the peer API, a failing one
			// included.
			{route: stPeerPush, path: stPeerEvents, body: stPeerBatch, who: noCredentials, ok: 400, open: true},
			{route: stPeerPull, path: stPeerEvents, who: noCredentials, ok: 400, open: true},
			{route: stPeerBlob, path: stPeerBlobURL, who: noCredentials, ok: 400, open: true},
		}
	},
}
