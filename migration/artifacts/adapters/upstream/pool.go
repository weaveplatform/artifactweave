package upstream

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
)

type pooled struct {
	fingerprint [32]byte
	client      *Client
}

// Pool reuses a repository's connections until its configuration or resolved
// credential revision changes. Remove closes a deleted repository's idle clients;
// Close drains all idle clients during application shutdown. Active requests are
// owned by their caller contexts and may finish against the previous revision.
type Pool struct {
	mu       sync.Mutex
	clients  map[string]pooled
	isClosed bool
}

// Client returns the pooled client of repository c, or builds one with
// NewClient when c's configuration or opts.Revision changed since the last,
// closing the client it replaces. It answers remote.ErrClosed after Close,
// and NewClient's errors. Safe for concurrent use.
func (p *Pool) Client(c repo.Config, opts remote.Options) (*Client, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode remote configuration: %w", err)
	}
	// Only the digest is retained. Neither cleartext secrets nor credential
	// revisions are suitable for metrics, identifiers, or error messages.
	fingerprint := sha256.Sum256(append(b, []byte(opts.Revision)...))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isClosed {
		return nil, remote.ErrClosed
	}
	if old, ok := p.clients[c.Key]; ok && old.fingerprint == fingerprint {
		return old.client, nil
	}
	client, err := NewClient(c, opts)
	if err != nil {
		return nil, err
	}
	if old, ok := p.clients[c.Key]; ok {
		old.client.Close()
	}
	if p.clients == nil {
		p.clients = make(map[string]pooled)
	}
	p.clients[c.Key] = pooled{fingerprint, client}
	return client, nil
}

// Connection returns Client(c, opts) as the Upstream port's Connection, with
// Client's errors, remote.ErrClosed after Close among them. On error it
// returns a literal nil, never a nil *Client inside the interface, which
// would compare unequal to nil.
func (p *Pool) Connection(c repo.Config, opts remote.Options) (remote.Connection, error) {
	client, err := p.Client(c, opts)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Remove closes and forgets the client of the repository named key, for a
// deleted repository; a later Client builds a new one.
func (p *Pool) Remove(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.clients[key]; ok {
		old.client.Close()
		delete(p.clients, key)
	}
}

// Close closes every pooled client, for application shutdown; every later
// Client answers remote.ErrClosed.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isClosed = true
	for key, old := range p.clients {
		old.client.Close()
		delete(p.clients, key)
	}
}
