package containertest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// imageAPI is the part of the Docker client that ensureImage uses.
type imageAPI interface {
	ImageInspect(
		ctx context.Context, image string, opts ...client.ImageInspectOption,
	) (client.ImageInspectResult, error)
	ImagePull(
		ctx context.Context, ref string, options client.ImagePullOptions,
	) (client.ImagePullResponse, error)
}

var (
	imageLocksMu sync.Mutex
	imageLocks   = map[string]*sync.Mutex{}
)

// imageLock returns the lock that serialises pulls of image in this process.
func imageLock(image string) *sync.Mutex {
	imageLocksMu.Lock()
	defer imageLocksMu.Unlock()
	l, ok := imageLocks[image]
	if !ok {
		l = new(sync.Mutex)
		imageLocks[image] = l
	}
	return l
}

// requireImage makes one of an emulator's sources present on the engine
// testcontainers uses, and returns it for the module to start.
//
// testcontainers-go pulls a missing image itself, but reads the response to
// its end without decoding it (attemptToPullImage in docker.go, v0.45.0).
// The engine answers a pull with 200 and then streams progress, so a pull
// that fails part-way, say on a registry error while fetching a layer,
// reports that error inside the stream. testcontainers drops it and creates
// the container, which fails with "No such image" and hides the registry's
// answer (#1448, upstream testcontainers/testcontainers-go#3945). Pulling
// here and decoding the stream reports the registry's own error, and lets a
// failed pull move on to the next source; the module's pull is then
// skipped, because its inspect finds the image.
func requireImage(ctx context.Context, sources ...string) (string, error) {
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	return ensureImage(ctx, cli, func(ref string) string { return registryAuth(ctx, ref) }, sources)
}

// registryAuth returns the X-Registry-Auth value testcontainers' own pull
// sends for image: the Docker CLI's stored credentials for its registry,
// base64url-encoded JSON as the Engine API takes them, or none, which makes
// the anonymous pull CI makes.
func registryAuth(ctx context.Context, image string) string {
	_, cfg, err := testcontainers.DockerImageAuth(ctx, image)
	if err != nil {
		return ""
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}

// ensureImage returns the first of sources the engine has, or else pulls
// them in order and returns the first that arrives. Every source carries
// the same digest, so any of them is the same image. One caller at a time
// does this for a given emulator in this process; the others wait and then
// find the image. A failed pull is not remembered, so the next caller
// tries again, as testcontainers' own pull did.
func ensureImage(
	ctx context.Context, api imageAPI, auth func(ref string) string, sources []string,
) (string, error) {
	lock := imageLock(sources[0])
	lock.Lock()
	defer lock.Unlock()
	// Any inspect error, not only "not found", leads to the pulls, whose
	// errors then name the engine's problem.
	for _, ref := range sources {
		if _, err := api.ImageInspect(ctx, ref); err == nil {
			return ref, nil
		}
	}
	errs := make([]error, 0, len(sources))
	for _, ref := range sources {
		err := pull(ctx, api, ref, auth(ref))
		if err == nil {
			return ref, nil
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

// pull pulls ref and checks the engine then has it.
func pull(ctx context.Context, api imageAPI, ref, auth string) error {
	resp, err := api.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	// Wait decodes the stream and returns the first error it reports.
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	// A stream that ends without an error has not yet proved the image is
	// there; if it is not, say so here rather than at container create.
	if _, err := api.ImageInspect(ctx, ref); err != nil {
		return fmt.Errorf("pull %s ended without an error, but the engine does not have it: %w",
			ref, err)
	}
	return nil
}
