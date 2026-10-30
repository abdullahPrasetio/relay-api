package monitor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestCommandRunnerParsesCoreContract(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "stdin.json")
	script := writeScript(t, "cat > "+captured+`
printf '%s' '{"response":{"status":201,"headers":[{"key":"content-type","value":"application/json"}],"body":"{\"ok\":true}","timeMs":42},"testResults":[{"name":"status is 201","passed":true},{"name":"body ok","passed":false,"error":"mismatch"}],"error":null}'`)

	runner := &CommandRunner{Args: []string{script}, Dir: filepath.Dir(script), Timeout: 5 * time.Second}
	job := Job{
		Workspace:     json.RawMessage(`{"id":"ws_1","name":"Team"}`),
		RequestID:     "req_1",
		EnvironmentID: "env_1",
	}
	result, err := runner.Run(context.Background(), job)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", result.StatusCode)
	}
	if result.DurationMS != 42 {
		t.Fatalf("duration = %d, want 42", result.DurationMS)
	}
	if result.AssertionFailures != 1 {
		t.Fatalf("assertion failures = %d, want 1", result.AssertionFailures)
	}
	if result.Error != "" {
		t.Fatalf("error = %q, want empty", result.Error)
	}
	if result.OK() {
		t.Fatal("run with a failed assertion reported OK")
	}

	var sent Job
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("stdin is not JSON: %v", err)
	}
	if sent.RequestID != "req_1" || sent.EnvironmentID != "env_1" {
		t.Fatalf("unexpected stdin job: %+v", sent)
	}
	if !strings.Contains(string(sent.Workspace), `"ws_1"`) {
		t.Fatalf("stdin workspace = %s", sent.Workspace)
	}
}

func TestCommandRunnerTransportError(t *testing.T) {
	script := writeScript(t, `printf '%s' '{"response":{"status":null,"headers":[],"body":"","timeMs":12},"testResults":[],"error":"connect ECONNREFUSED"}'`)
	runner := &CommandRunner{Args: []string{script}, Timeout: 5 * time.Second}
	result, err := runner.Run(context.Background(), Job{Workspace: json.RawMessage(`{}`), RequestID: "req_1"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.OK() {
		t.Fatal("transport failure reported OK")
	}
	if result.Error != "connect ECONNREFUSED" || result.StatusCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCommandRunnerUnavailable(t *testing.T) {
	runner := &CommandRunner{Args: []string{"/nonexistent/relay-core"}, Timeout: time.Second}
	if _, err := runner.Run(context.Background(), Job{Workspace: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("Run succeeded with a missing executable")
	} else if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v, want an unavailable message", err)
	}

	// Default-style node bundle check catches a missing dist-core file.
	runner = &CommandRunner{Args: []string{"node", "dist-core/relay-core.mjs"}, Dir: t.TempDir()}
	_, err := runner.Run(context.Background(), Job{Workspace: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "core bundle") {
		t.Fatalf("error = %v, want a missing core bundle error", err)
	}
}

func TestCommandRunnerInvalidJSON(t *testing.T) {
	script := writeScript(t, `printf '%s' 'not json'`)
	runner := &CommandRunner{Args: []string{script}, Timeout: 5 * time.Second}
	if _, err := runner.Run(context.Background(), Job{Workspace: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("Run accepted invalid JSON")
	}
}

func TestCommandRunnerParsesJSONOnExitError(t *testing.T) {
	script := writeScript(t, `printf '%s' '{"response":null,"testResults":[],"error":"request not found"}'; exit 2`)
	runner := &CommandRunner{Args: []string{script}, Timeout: 5 * time.Second}
	result, err := runner.Run(context.Background(), Job{Workspace: json.RawMessage(`{}`), RequestID: "missing"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.OK() || result.Error != "request not found" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
