package containertest

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// requireImage makes image present on the engine testcontainers uses,
// before an emulator module creates a container from it.
//
// testcontainers-go pulls a missing image itself, but reads the response to
// its end without decoding it (attemptToPullImage in docker.go, v0.45.0).
// The engine answers a pull with 200 and then streams progress, so a pull
// that fails part-way, say on a registry error while fetching a layer,
// reports that error inside the stream. testcontainers drops it and creates
// the container, which fails with "No such image" and hides the registry's
// answer (#1448, upstream testcontainers/testcontainers-go#3945). Pulling
// here and decoding the stream reports the registry's own error; the
// module's pull is then skipped, because its inspect finds the image.
func requireImage(ctx context.Context, image string) error {
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	return ensureImage(ctx, cli, image, registryAuth(ctx, image))
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

// ensureImage pulls image unless the engine has it, then checks the engine
// has it. One caller at a time pulls a given image in this process; the
// others wait and then find it. A failed pull is not remembered, so the
// next caller pulls again, as testcontainers' own pull did.
func ensureImage(ctx context.Context, api imageAPI, image, auth string) error {
	lock := imageLock(image)
	lock.Lock()
	defer lock.Unlock()
	// Every emulator image is pinned by digest, so finding the reference
	// finds those exact bytes. Any inspect error, not only "not found",
	// leads to the pull, whose error then names the engine's problem.
	if _, err := api.ImageInspect(ctx, image); err == nil {
		return nil
	}
	pull, err := api.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	// Wait decodes the stream and returns the first error it reports.
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	// A stream that ends without an error has not yet proved the image is
	// there; if it is not, say so here rather than at container create.
	if _, err := api.ImageInspect(ctx, image); err != nil {
		return fmt.Errorf("pull %s ended without an error, but the engine does not have it: %w",
			image, err)
	}
	return nil
}
