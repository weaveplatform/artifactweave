package sql

import (
	"context"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// purposeInstanceKey seals the instance's Ed25519 private key; the row id is
// the instance id, so a key copied to another identity fails to open.
const purposeInstanceKey = "instance.private_key"

// Instance implements store.Store.
func (s *Store) Instance() store.InstanceStore { return instanceStore{s} }

type instanceStore struct{ s *Store }

const instanceCols = "instance_id, public_key, private_key, namespace_id, protocol_version"

// args returns the column values of id with its private key sealed.
func (i instanceStore) args(id types.InstanceIdentity) ([]any, error) {
	sealed, err := i.s.Seal(purposeInstanceKey, id.InstanceID, string(id.PrivateKey))
	if err != nil {
		return nil, err
	}
	return []any{
		id.InstanceID,
		[]byte(id.PublicKey),
		sealed,
		id.NamespaceID,
		id.ProtocolVersion,
	}, nil
}

func (i instanceStore) Init(ctx context.Context, id types.InstanceIdentity) error {
	args, err := i.args(id)
	if err != nil {
		return err
	}
	_, err = i.s.Exec(ctx, "init instance",
		"INSERT INTO instance (id, "+instanceCols+") VALUES (1, ?, ?, ?, ?, ?)", args...)
	return err
}

func (i instanceStore) Get(ctx context.Context) (types.InstanceIdentity, error) {
	var (
		out         types.InstanceIdentity
		pub, sealed []byte
	)
	err := i.s.Q(ctx).QueryRowContext(ctx, "SELECT "+instanceCols+" FROM instance WHERE id = 1").
		Scan(&out.InstanceID, &pub, &sealed, &out.NamespaceID, &out.ProtocolVersion)
	if err != nil {
		return out, i.s.Wrap("get instance", err)
	}
	if len(pub) > 0 {
		out.PublicKey = pub
	}
	priv, err := i.s.Unseal(purposeInstanceKey, out.InstanceID, sealed)
	if err != nil {
		return types.InstanceIdentity{}, err
	}
	if priv != "" {
		out.PrivateKey = []byte(priv)
	}
	return out, nil
}

func (i instanceStore) Update(ctx context.Context, id types.InstanceIdentity) error {
	args, err := i.args(id)
	if err != nil {
		return err
	}
	return i.s.ExecOne(
		ctx,
		"update instance",
		"UPDATE instance SET instance_id = ?, public_key = ?, "+
			"private_key = ?, namespace_id = ?, protocol_version = ? WHERE id = 1",
		args...)
}
