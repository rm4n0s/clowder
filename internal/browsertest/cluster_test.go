//go:build clowder_browser

package browsertest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

// argLoggingBinary is a fake clowder that appends its arguments to log, one
// line per run, answers demo and invite like fakeBinary, and serves like
// serve. Its demo makes the data directory, as the real one does.
func argLoggingBinary(t *testing.T, log, serve string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clowder")
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\n" +
		"if [ \"$1\" = demo ]; then mkdir -p \"$3\"; exit 0; fi\n" +
		"if [ \"$1\" = invite ]; then echo http://127.0.0.1/invite/fake-token; exit 0; fi\n" + serve + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// readLines returns the lines of the file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestStartServerWithDatabaseRedisAndDataDir(t *testing.T) {
	const dbURL = "postgres://clowder:hunter2@db:5432/clowder"
	tests := []struct {
		name      string
		opts      func(dataDir string) Options
		wantLines func(dataDir string) []string // the beginning of each logged run
	}{
		{
			name: "demo then serve, on the database and the Redis",
			opts: func(dataDir string) Options {
				return Options{DataDir: dataDir, DatabaseURL: dbURL, RedisURL: "redis://redis:6379/0", InviteMember: true}
			},
			wantLines: func(d string) []string {
				return []string{
					"demo -data " + d + " -database-url " + dbURL,
					"invite -member -data " + d + " -base-url http://127.0.0.1 -database-url " + dbURL,
					"serve -addr ",
				}
			},
		},
		{
			name: "skip the demo",
			opts: func(dataDir string) Options {
				return Options{DataDir: dataDir, DatabaseURL: dbURL, SkipDemo: true}
			},
			wantLines: func(string) []string { return []string{"serve -addr "} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "shared")
			log := filepath.Join(t.TempDir(), "args")
			opts := tt.opts(dataDir)
			opts.Binary = argLoggingBinary(t, log, "trap 'exit 0' INT; "+listenLine+"; while true; do sleep 0.05; done")
			srv, err := StartServer(context.Background(), opts)
			if err != nil {
				t.Fatalf("StartServer: %+v", err)
			}
			if srv.DataDir != dataDir {
				t.Errorf("DataDir = %q, want %q", srv.DataDir, dataDir)
			}
			if err := srv.Stop(); err != nil {
				t.Errorf("Stop: %v", err)
			}
			if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
				t.Errorf("the data directory of the caller was removed or not made: %v", err)
			}
			if _, err := os.Stat(srv.tmpDir); !os.IsNotExist(err) {
				t.Errorf("the temporary directory was not removed: %v", err)
			}
			lines := readLines(t, log)
			want := tt.wantLines(dataDir)
			if len(lines) != len(want) {
				t.Fatalf("the binary ran %d times (%q), want %d", len(lines), lines, len(want))
			}
			for i := range want {
				if !strings.HasPrefix(lines[i], want[i]) {
					t.Errorf("run %d = %q, want it to start with %q", i, lines[i], want[i])
				}
			}
			serve := lines[len(lines)-1]
			if !strings.Contains(serve, "-database-url "+dbURL) {
				t.Errorf("serve = %q, want the database URL", serve)
			}
			if strings.Contains(opts.RedisURL, "redis") != strings.Contains(serve, "-redis-url "+opts.RedisURL) {
				t.Errorf("serve = %q, redis URL given: %q", serve, opts.RedisURL)
			}
		})
	}
}

func TestStartServerDatabaseURLNeverInErrors(t *testing.T) {
	const dbURL = "postgres://clowder:hunter2@db:5432/clowder"
	bad := filepath.Join(t.TempDir(), "clowder")
	script := "#!/bin/sh\necho \"cannot open " + dbURL + "\" >&2\nexit 2\n"
	if err := os.WriteFile(bad, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []Options{
		{Binary: bad, DatabaseURL: dbURL},
		{Binary: bad, DatabaseURL: dbURL, SkipDemo: true, InviteMember: true},
	} {
		_, err := StartServer(context.Background(), opts)
		appErr, ok := errors.FromError(err)
		if !ok {
			t.Fatalf("expected *errors.Error, got %T: %v", err, err)
		}
		text := appErr.Message
		for _, v := range appErr.Metadata {
			if s, ok := v.(string); ok {
				text += " " + s
			}
		}
		if strings.Contains(text, "hunter2") {
			t.Errorf("the error holds the password: %s", text)
		}
		if !strings.Contains(text, "xxxxx") {
			t.Errorf("the error does not show the redacted URL: %s", text)
		}
	}
}

func TestStartCluster(t *testing.T) {
	const (
		dbURL    = "postgres://clowder:hunter2@db:5432/clowder"
		redisURL = "redis://redis:6379/0"
	)
	dataDir := filepath.Join(t.TempDir(), "shared")
	log := filepath.Join(t.TempDir(), "args")
	opts := Options{
		Binary:      argLoggingBinary(t, log, "trap 'exit 0' INT; "+listenLine+"; while true; do sleep 0.05; done"),
		DatabaseURL: dbURL, RedisURL: redisURL, DataDir: dataDir,
	}
	c, err := StartCluster(context.Background(), opts, 3)
	if err != nil {
		t.Fatalf("StartCluster: %+v", err)
	}
	if len(c.Servers) != 3 {
		t.Fatalf("%d servers, want 3", len(c.Servers))
	}
	seen := map[string]bool{}
	pids := make([]int, 0, 3)
	for _, srv := range c.Servers {
		if seen[srv.URL] {
			t.Errorf("two servers share the URL %s", srv.URL)
		}
		seen[srv.URL] = true
		if srv.DataDir != dataDir {
			t.Errorf("DataDir = %q, want %q", srv.DataDir, dataDir)
		}
		pids = append(pids, srv.cmd.Process.Pid)
	}
	first := c.Stop()
	if again := c.Stop(); (again != nil) != (first != nil) {
		t.Errorf("first Stop = %v, second Stop = %v", first, again)
	}
	for _, pid := range pids {
		gone := false
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if syscall.Kill(-pid, 0) != nil {
				gone = true
				break
			}
		}
		if !gone {
			t.Errorf("process group %d still exists after Stop", pid)
		}
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("the data directory of the caller was removed: %v", err)
	}
	if _, err := os.Stat(c.tmpDir); !os.IsNotExist(err) {
		t.Errorf("the temporary directory was not removed: %v", err)
	}
	lines := readLines(t, log)
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "demo -data "+dataDir+" -database-url "+dbURL) {
		t.Fatalf("runs = %q, want the demo once and three serves", lines)
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, "serve -addr ") || !strings.Contains(line, "-data "+dataDir) ||
			!strings.Contains(line, "-database-url "+dbURL) || !strings.Contains(line, "-redis-url "+redisURL) {
			t.Errorf("serve = %q", line)
		}
	}
}

func TestStartClusterErrors(t *testing.T) {
	const (
		dbURL    = "postgres://clowder:hunter2@db:5432/clowder"
		redisURL = "redis://redis:6379/0"
	)
	// failSecond serves once and then exits before it listens.
	failSecond := func(t *testing.T) Options {
		count := filepath.Join(t.TempDir(), "count")
		serve := `trap 'exit 0' INT; if [ -e '` + count + `' ]; then echo boom >&2; exit 3; fi; : > '` + count + `'; ` + listenLine +
			`; while true; do sleep 0.05; done`
		return Options{Binary: fakeBinary(t, serve), DatabaseURL: dbURL, RedisURL: redisURL}
	}
	tests := []struct {
		name      string
		opts      func(t *testing.T) Options
		servers   int
		wantTag   string
		wantRoute string
	}{
		{"no database", func(*testing.T) Options { return Options{RedisURL: redisURL} }, 2, tagClusterOptionsInvalid, "StartCluster.ClusterOptionsInvalid"},
		{"no Redis", func(*testing.T) Options { return Options{DatabaseURL: dbURL} }, 2, tagClusterOptionsInvalid, "StartCluster.ClusterOptionsInvalid"},
		{"no server", func(*testing.T) Options { return Options{DatabaseURL: dbURL, RedisURL: redisURL} }, 0, tagClusterOptionsInvalid, "StartCluster.ClusterOptionsInvalid"},
		{"a server exits before it listens", failSecond, 2, tagServerExitedEarly, "StartCluster->Cluster.start->launch.ServerExitedEarly"},
		{"the build fails", func(t *testing.T) Options {
			return Options{ModuleRoot: t.TempDir(), DatabaseURL: dbURL, RedisURL: redisURL}
		}, 2, tagServerBuildFailed, "StartCluster->Cluster.start->buildBinary.ServerBuildFailed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := StartCluster(context.Background(), tt.opts(t), tt.servers)
			if err == nil {
				_ = c.Stop()
				t.Fatal("StartCluster succeeded")
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute(tt.wantRoute) {
				t.Errorf("tag = %q, route = %s; want %q and %s", appErr.Tag, appErr.Route(), tt.wantTag, tt.wantRoute)
			}
		})
	}
}
