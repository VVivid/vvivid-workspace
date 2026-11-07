//go:build container

package tests

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/examples/agent"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/mcpserver"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const helperImage = "golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c"

func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host=unix:///var/run/docker.sock"}, args...)...)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
			continue
		}
		cmd.Env = append(cmd.Env, variable)
	}
	return cmd
}

func docker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	output, err := dockerCommand(ctx, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("managed Docker %s failed: %v: %s", args[0], err, output)
	}
	return output
}

func TestProductionContainerPersistenceAndAuthenticatedMCP(t *testing.T) {
	image := os.Getenv("MEMORY_TEST_IMAGE")
	if image == "" {
		t.Fatal("container test requires MEMORY_TEST_IMAGE from make container-test")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "artificial-memory-test-" + hex.EncodeToString(suffix[:])
	volume := prefix + "-data"
	names := []string{prefix + "-prepare", prefix + "-contents", prefix + "-first", prefix + "-second"}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, name := range names {
			// Names are random and owned exclusively by this test. No pruning or
			// host-volume deletion, including on failure, is permitted here.
			dockerCommand(ctx, "rm", "-f", name).Run()
		}
		if output, err := dockerCommand(ctx, "volume", "rm", volume).CombinedOutput(); err != nil {
			t.Errorf("remove owned temporary volume: %v: %s", err, output)
		}
	})
	docker(t, "volume", "create", "--label", "artificial-memory.synthetic-test=true", volume)
	fixture, err := agent.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	public, err := fixture.PublicJWKS()
	if err != nil {
		t.Fatal(err)
	}
	jwks, subject := filepath.Join(private, "public.jwks"), filepath.Join(private, "subject")
	if err := os.WriteFile(jwks, public, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(jwks, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subject, []byte(fixture.Subject), 0600); err != nil {
		t.Fatal(err)
	}
	// Root is limited to preparing this owned test volume and copying a
	// synthetic 0600 subject. Every production app/admin container uses the
	// image's DEFAULT nonroot user. The volume root models an fsGroup mount;
	// the app itself creates its private 0700 sqlite subdirectory.
	docker(t, "run", "--rm", "--name", names[0], "--pull=never", "--user=0:0", "--read-only", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--cap-add=FOWNER", "--security-opt=no-new-privileges", "--network=none",
		"--mount", "type=volume,src="+volume+",dst=/data", "--mount", "type=bind,src="+subject+",dst=/fixture-subject,readonly", helperImage, "sh", "-ec",
		"chown 65532:65532 /data; chmod 2770 /data; cp /fixture-subject /data/subject; chown 65532:65532 /data/subject; chmod 0600 /data/subject")
	appArgs := []string{"--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--mount", "type=volume,src=" + volume + ",dst=/data"}
	enroll := append([]string{"run", "--rm", "--network=none"}, appArgs...)
	enroll = append(enroll, image, "admin", "enroll", "--database", "/data/sqlite/memory.db", "--issuer", agent.IdentityIssuer, "--subject-file", "/data/subject")
	firstEnrollment := docker(t, enroll...)
	if bytes.Contains(firstEnrollment, []byte(fixture.Subject)) {
		t.Fatal("container admin exposed a subject")
	}
	inspectProductionImage(t, image, names[1])
	start := func(name string) string {
		t.Helper()
		args := append([]string{"run", "-d", "--name", name, "--publish", "127.0.0.1::8080"}, appArgs...)
		for _, env := range []string{
			"MEMORY_BACKEND_ORIGIN=" + agent.BackendOrigin, "MEMORY_GATEWAY_ISSUER=" + agent.GatewayIssuer,
			"MEMORY_AUDIENCE=" + agent.Audience, "MEMORY_IDENTITY_ISSUER=" + agent.IdentityIssuer,
			"MEMORY_GATEWAY_JWKS_FILE=/etc/gateway-public.jwks", "MEMORY_ALLOWED_CLIENT_IDS=" + agent.ClientID,
		} {
			args = append(args, "--env", env)
		}
		args = append(args, "--mount", "type=bind,src="+jwks+",dst=/etc/gateway-public.jwks,readonly", image)
		docker(t, args...)
		var inspected []struct {
			Config     struct{ User string }
			HostConfig struct {
				ReadonlyRootfs bool
				CapDrop        []string
				SecurityOpt    []string
			}
			NetworkSettings struct {
				Ports map[string][]struct{ HostIP, HostPort string }
			}
		}
		if err := json.Unmarshal(docker(t, "inspect", name), &inspected); err != nil || len(inspected) != 1 {
			t.Fatal("cannot inspect real application container")
		}
		app := inspected[0]
		if app.Config.User != "65532:65532" || !app.HostConfig.ReadonlyRootfs || !reflect.DeepEqual(app.HostConfig.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(app.HostConfig.SecurityOpt, []string{"no-new-privileges"}) {
			t.Fatal("application container did not run with required security settings")
		}
		ports := app.NetworkSettings.Ports["8080/tcp"]
		if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" {
			t.Fatal("container test endpoint was not restricted to loopback")
		}
		endpoint := "http://127.0.0.1:" + ports[0].HostPort
		client := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
		deadline := time.After(15 * time.Second)
		for {
			response, err := client.Get(endpoint + "/health/ready")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					break
				}
			}
			select {
			case <-deadline:
				t.Fatalf("real application readiness failed: %s", docker(t, "logs", name))
			case <-time.After(50 * time.Millisecond):
			}
		}
		docker(t, "exec", name, "/artificial-memory", "healthcheck", "live")
		docker(t, "exec", name, "/artificial-memory", "healthcheck", "ready")
		return endpoint
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	endpoint := start(names[2])
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "memory.internal:8080"
	response, err := (&http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous real-container MCP accepted: %d", response.StatusCode)
	}
	session, err := fixture.Connect(ctx, endpoint, "memory:read memory:write")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := agent.RunScenario(ctx, session, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := fixture.Connect(ctx, endpoint, "memory:read")
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	denied, err := readOnly.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: result.OriginalRequest})
	if err != nil || !denied.IsError || len(denied.Content) != 1 || !strings.Contains(denied.Content[0].(*mcp.TextContent).Text, `"forbidden"`) {
		t.Fatal("read-only real-container writer replay was not forbidden")
	}
	query := memory.Query{AllSpaces: true, Limit: 1, ContextLimit: 1, LinkLimit: 1}
	first, err := agent.Call[memory.Page](ctx, session, "retrieve", query)
	if err != nil || first.NextCursor == "" {
		t.Fatal("container fixture did not create a continuation")
	}
	query.Cursor = first.NextCursor
	expected, err := agent.Call[memory.Page](ctx, session, "retrieve", query)
	if err != nil {
		t.Fatal(err)
	}
	session.Close()
	readOnly.Close()
	docker(t, "stop", "--time=15", names[2])
	var state []struct{ State struct{ ExitCode int } }
	if err := json.Unmarshal(docker(t, "inspect", names[2]), &state); err != nil || len(state) != 1 || state[0].State.ExitCode != 0 {
		t.Fatal("SIGTERM did not stop the production container gracefully")
	}
	if log := string(docker(t, "logs", names[2])); !strings.Contains(log, "ArtificialMemory stopped.") || strings.Contains(log, fixture.Subject) || strings.Contains(log, "9007199254740993") {
		t.Fatal("container lifecycle logs missing or exposed private data")
	}
	docker(t, "rm", names[2])
	// Recreate a NEW container, using exactly the same private named volume.
	endpoint = start(names[3])
	session, err = fixture.Connect(ctx, endpoint, "memory:read memory:write")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	replay, err := agent.Call[mcpserver.RememberReceipt](ctx, session, "remember", result.OriginalRequest)
	if err != nil || !reflect.DeepEqual(replay, result.Original) {
		t.Fatal("container recreation lost the original durable receipt")
	}
	continued, err := agent.Call[memory.Page](ctx, session, "retrieve", query)
	if err != nil || !reflect.DeepEqual(expected, continued) {
		t.Fatal("container recreation lost the persisted cursor/history")
	}
	history, err := agent.Call[memory.Page](ctx, session, "retrieve", memory.Query{AllSpaces: true})
	if err != nil || !history.Complete || len(history.Matches) != 5 || len(history.Relationships) != 3 {
		t.Fatal("container recreation lost or duplicated synthetic history")
	}
	if !bytes.Equal(firstEnrollment, docker(t, enroll...)) {
		t.Fatal("container recreation changed enrolled account identity")
	}
	session.Close()
	docker(t, "stop", "--time=15", names[3])
	// Read only metadata in the owned volume; no memory payloads are copied.
	metadata := docker(t, "run", "--rm", "--pull=never", "--user=65532:65532", "--read-only", "--cap-drop=ALL", "--network=none", "--security-opt=no-new-privileges", "--mount", "type=volume,src="+volume+",dst=/data,readonly", helperImage, "stat", "-c", "%a %u:%g", "/data/sqlite", "/data/sqlite/memory.db")
	if string(metadata) != "700 65532:65532\n600 65532:65532\n" {
		t.Fatalf("private storage modes changed across restart: %s", metadata)
	}
	t.Log("real nonroot readonly container: signed SDK scenario, anonymous/read-only rejection, SIGTERM, volume recreation, stable enrollment/receipt/cursor/history and private file modes passed")
}

func inspectProductionImage(t *testing.T, image, name string) {
	t.Helper()
	docker(t, "create", "--name", name, image, "healthcheck", "live")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := dockerCommand(ctx, "export", name)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	defer func() {
		cancel()
		cmd.Wait()
	}()
	archive := tar.NewReader(pipe)
	found := false
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		path := strings.TrimPrefix(header.Name, "./")
		for _, forbidden := range []string{"src/", ".git/", "examples/", "tests/", "run/secrets/", "usr/local/go/"} {
			if strings.HasPrefix(path, forbidden) {
				t.Fatalf("image contains excluded build source/fixtures: %s", path)
			}
		}
		if path == "artificial-memory" {
			binary, err := io.ReadAll(archive)
			if err != nil || bytes.Contains(binary, []byte("synthetic-agent-owner")) || bytes.Contains(binary, []byte("SYNTHETIC EXAMPLE ONLY")) {
				t.Fatal("production binary contains the example signer/scenario")
			}
			found = true
		}
		if strings.Contains(path, "synthetic") || strings.Contains(path, "fixture") || strings.Contains(path, "build-ca") {
			t.Fatalf("image contains synthetic fixture or session CA: %s", path)
		}
	}
	if !found {
		t.Fatal("production filesystem does not contain the application binary")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(fmt.Errorf("inspect production filesystem: %w", err))
	}
}
