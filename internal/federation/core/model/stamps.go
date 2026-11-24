package model

// ItemStamp is what a member knows of one item of a federated repository
// for the conflict rule (decision 0021): the stamp of the change that
// left the item as it is here, whether that change deleted it, and its
// content. A deleted item's stamp is its tombstone, kept for the outbox
// retention window so that an older change arriving late cannot bring the
// item back.
type ItemStamp struct {
	RepoKey string
	// Path is repository-relative, without a leading slash.
	Path string
	Stamp
	Deleted bool
	Folder  bool
	SHA256  string
	Size    int64
	// Pending is the change of a content entry that won while its blob
	// could not be fetched: the entry's payload, which applies once the
	// blob arrives. Until then the item stays as it was.
	Pending []byte
}

// Live reports whether the item holds the content SHA256 here: not
// deleted, and not waiting for its blob.
func (s ItemStamp) Live() bool { return !s.Deleted && len(s.Pending) == 0 }
