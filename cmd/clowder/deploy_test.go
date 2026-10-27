package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoFile reads a file of the repository, which is two directories above the
// package.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// lines returns the lines of text that are not blank and not comments.
func lines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// TestDockerfileKeepsTheImageSmallAndUnprivileged checks what the security
// review of step 17.1 relies on: the image runs as a user that is not root, on
// a distroless base, holds no secret in an ARG or ENV, and copies nothing but
// the binary and the empty data directory into the final stage.
func TestDockerfileKeepsTheImageSmallAndUnprivileged(t *testing.T) {
	dockerfile := lines(repoFile(t, "Dockerfile"))
	var runStage []string
	inRun := false
	for _, l := range dockerfile {
		if strings.HasPrefix(l, "FROM ") {
			inRun = strings.Contains(l, "distroless")
		}
		if inRun {
			runStage = append(runStage, l)
		}
	}
	if len(runStage) == 0 {
		t.Fatal("no distroless run stage")
	}
	text := strings.Join(runStage, "\n")
	if !strings.Contains(runStage[0], ":nonroot") {
		t.Errorf("the run stage does not start from a nonroot image: %s", runStage[0])
	}
	if !regexp.MustCompile(`(?m)^USER nonroot:nonroot$`).MatchString(text) {
		t.Error("the run stage does not end with USER nonroot:nonroot")
	}
	secret := regexp.MustCompile(`(?i)\b(ARG|ENV)\b.*(PASSWORD|SECRET|TOKEN|KEY|DATABASE_URL|REDIS_URL)`)
	for _, l := range dockerfile {
		if secret.MatchString(l) {
			t.Errorf("a secret setting in the Dockerfile: %s", l)
		}
		if strings.HasPrefix(l, "ADD ") {
			t.Errorf("ADD can fetch a URL or unpack an archive; use COPY: %s", l)
		}
	}
	for _, l := range runStage {
		if strings.HasPrefix(l, "COPY ") && !strings.Contains(l, "--from=build") {
			t.Errorf("the run stage copies from the build context: %s", l)
		}
	}
	if !strings.Contains(text, "HEALTHCHECK") || !strings.Contains(text, `"/clowder", "healthcheck"`) {
		t.Error("the run stage has no HEALTHCHECK that runs the binary")
	}
}

// TestDockerignoreKeepsSecretsAndDataOutOfTheBuild checks that the build
// context leaves out the secrets and the data that live next to the source.
func TestDockerignoreKeepsSecretsAndDataOutOfTheBuild(t *testing.T) {
	have := map[string]bool{}
	for _, l := range lines(repoFile(t, ".dockerignore")) {
		have[l] = true
	}
	for _, want := range []string{".git", ".env", ".env.*", "!.env.example", "data/", ".tmp/", "*.db", "*.db-wal", "*.db-shm", ".claude"} {
		if !have[want] {
			t.Errorf(".dockerignore does not list %q", want)
		}
	}
}

// TestComposePublishesOnlyWhatItShould checks the published ports of the
// compose files: PostgreSQL and Redis are never published, the Clowder
// processes are published alone or, with the cluster file, not at all (Caddy
// is), and the database password is required and not written in the file.
func TestComposePublishesOnlyWhatItShould(t *testing.T) {
	compose := repoFile(t, "compose.yaml")
	cluster := repoFile(t, "compose.cluster.yaml")

	// service returns the lines of the service name of a compose file.
	service := func(file, name string) string {
		start := strings.Index(file, "\n  "+name+":\n")
		if start < 0 {
			t.Fatalf("no service %s", name)
		}
		rest := file[start+1:]
		end := regexp.MustCompile(`\n  [a-z0-9]+:\n|\nvolumes:\n`).FindStringIndex(rest[len(name)+4:])
		if end == nil {
			return rest
		}
		return rest[:len(name)+4+end[0]]
	}
	ports := regexp.MustCompile(`(?m)^\s+ports:`)
	for _, name := range []string{"postgres"} {
		if ports.MatchString(service(compose, name)) {
			t.Errorf("compose.yaml publishes %s", name)
		}
	}
	if !ports.MatchString(service(compose, "clowder")) {
		t.Error("compose.yaml does not publish clowder")
	}
	for _, name := range []string{"redis"} {
		if ports.MatchString(service(cluster, name)) {
			t.Errorf("compose.cluster.yaml publishes %s", name)
		}
	}
	for _, name := range []string{"clowder", "clowder2"} {
		if !strings.Contains(service(cluster, name), "ports: !reset []") {
			t.Errorf("compose.cluster.yaml still publishes %s", name)
		}
	}
	if !ports.MatchString(service(cluster, "caddy")) {
		t.Error("compose.cluster.yaml does not publish caddy")
	}
	if !strings.Contains(compose, "CLOWDER_DB_PASSWORD:?") {
		t.Error("compose.yaml does not require CLOWDER_DB_PASSWORD")
	}
	for _, file := range []string{compose, cluster} {
		for _, l := range lines(file) {
			if regexp.MustCompile(`(?i)(password|secret)\S*:\s*[A-Za-z0-9]`).MatchString(l) && !strings.Contains(l, "${") {
				t.Errorf("a password written in a compose file: %s", l)
			}
			if strings.Contains(l, "privileged") || strings.HasPrefix(l, "user: root") || strings.Contains(l, "network_mode: host") {
				t.Errorf("a privileged setting in a compose file: %s", l)
			}
		}
	}
}

// TestCaddyfileOfTheClusterIsClosed checks the proxy of the cluster: its admin
// API is off, it speaks plain HTTP on the one port compose publishes, and it
// does not sit behind another trust rule than the one the compose file sets.
func TestCaddyfileOfTheClusterIsClosed(t *testing.T) {
	caddyfile := repoFile(t, "deploy/Caddyfile.cluster")
	text := strings.Join(lines(caddyfile), "\n")
	for _, want := range []string{"admin off", "auto_https off", ":8080 {", "reverse_proxy clowder:8080 clowder2:8080", "health_uri /healthz", "flush_interval -1"} {
		if !strings.Contains(text, want) {
			t.Errorf("the Caddyfile does not have %q", want)
		}
	}
	if strings.Contains(text, "trusted_proxies") {
		t.Error("the Caddyfile trusts the X-Forwarded-For of its clients")
	}
	// Clowder trusts X-Forwarded-For only in the composition that has the proxy.
	if strings.Contains(repoFile(t, "compose.yaml"), "CLOWDER_TRUST_PROXY: \"true\"") {
		t.Error("compose.yaml trusts X-Forwarded-For without a proxy in front")
	}
	if !strings.Contains(repoFile(t, "compose.cluster.yaml"), `CLOWDER_TRUST_PROXY: "true"`) {
		t.Error("compose.cluster.yaml does not turn on the proxy headers behind Caddy")
	}
}
