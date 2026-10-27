//go:build clowder_browser

package browsertest

import (
	"context"
	"os"
	"path/filepath"

	"github.com/rm4n0s/errors"
)

// Cluster is several clowder servers that share one PostgreSQL database, one
// Redis and one data directory, the way the processes of compose.cluster.yaml
// do (SPEC D41, §8.1). Make it with StartCluster and stop it with Stop.
type Cluster struct {
	// Servers are the servers, in the order they started. They have different
	// URLs and the same DataDir.
	Servers []*Server

	tmpDir string
}

// StartCluster builds clowder (unless opts.Binary is set), makes the demo data
// once in the database of opts.DatabaseURL and in the data directory, then
// starts n servers on them, each on a free port of its own and each with
// opts.RedisURL. opts.DatabaseURL and opts.RedisURL are required. It returns
// once every server has logged the address it listens on. On any error
// nothing is left running or on disk. The caller must call Stop on success.
// Options.SkipDemo and Options.DataDir work as in StartServer: the demo data
// is made unless SkipDemo is set, and a given DataDir is not removed by Stop.
func StartCluster(ctx context.Context, opts Options, n int) (*Cluster, error) {
	if n < 1 || opts.DatabaseURL == "" || opts.RedisURL == "" {
		return nil, errors.New(tagClusterOptionsInvalid, "a cluster needs at least one server, a database URL and a Redis URL",
			"servers", n, "database", opts.DatabaseURL != "", "redis", opts.RedisURL != "")
	}
	tmpDir, err := os.MkdirTemp("", "clowder-browsertest-")
	if err != nil {
		return nil, errors.NewErr(tagTempDirFailed, err)
	}
	c := &Cluster{tmpDir: tmpDir}
	if err := c.start(ctx, opts, n); err != nil {
		stopErr := c.Stop()
		if stopErr != nil {
			return nil, errors.NewErr(tagClusterStartFailed, err).SetMetadata("stop", stopErr.Error())
		}
		return nil, err
	}
	return c, nil
}

// start builds the binary, makes the demo data and launches the servers.
func (c *Cluster) start(ctx context.Context, opts Options, n int) error {
	bin, err := buildBinary(ctx, opts, c.tmpDir)
	if err != nil {
		return err
	}
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(c.tmpDir, "data")
	}
	if _, err := prepare(ctx, opts, bin, dataDir); err != nil {
		return err
	}
	for range n {
		var srv *Server
		for attempt := 1; ; attempt++ {
			var bindFailed bool
			srv, bindFailed, err = launch(ctx, opts, bin, dataDir)
			if err == nil {
				break
			}
			if !bindFailed || attempt >= startAttempts {
				return err
			}
		}
		srv.DataDir = dataDir
		c.Servers = append(c.Servers, srv)
	}
	return nil
}

// Stop stops every server (the last one first) and removes the temporary
// directory. It always reaps the processes and may be called more than once.
// The error is the first one: a server that had to be killed or did not exit
// cleanly, or a directory that could not be removed.
func (c *Cluster) Stop() error {
	var first error
	for i := len(c.Servers) - 1; i >= 0; i-- {
		if err := c.Servers[i].Stop(); err != nil && first == nil {
			first = err
		}
	}
	if err := os.RemoveAll(c.tmpDir); err != nil && first == nil {
		first = errors.NewErr(tagTempDirFailed, err).SetMetadata("dir", c.tmpDir)
	}
	return first
}

const (
	tagClusterOptionsInvalid = "ClusterOptionsInvalid"
	tagClusterStartFailed    = "ClusterStartFailed"
)
