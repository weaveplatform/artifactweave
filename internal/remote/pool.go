package remote

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/weaveplatform/artifactweave/pkg/repo"
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

func (p *Pool) Client(c repo.Config, opts Options) (*Client, error) {
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
		return nil, ErrClosed
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

func (p *Pool) Remove(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.clients[key]; ok {
		old.client.Close()
		delete(p.clients, key)
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isClosed = true
	for key, old := range p.clients {
		old.client.Close()
		delete(p.clients, key)
	}
}
