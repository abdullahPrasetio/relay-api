package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Job is the JSON document sent to the core CLI on stdin.
type Job struct {
	Workspace     json.RawMessage `json:"workspace"`
	RequestID     string          `json:"requestId"`
	EnvironmentID string          `json:"environmentId,omitempty"`
}

// Result is the normalized outcome of one monitor execution.
type Result struct {
	StatusCode        int
	DurationMS        int
	AssertionFailures int
	Error             string
}

// OK reports whether the run is considered healthy: the request executed, the
// HTTP status is not a client/server error, and every test passed.
func (r Result) OK() bool {
	return r.Error == "" &&
		r.AssertionFailures == 0 &&
		r.StatusCode >= 100 && r.StatusCode < 400
}

// Runner executes one monitor job. Implementations must never fake success:
// an unavailable or crashing backend is an error, not a passing run.
type Runner interface {
	Run(ctx context.Context, job Job) (Result, error)
}

// CommandRunner executes the Node core bundle as a subprocess.
type CommandRunner struct {
	Args    []string
	Dir     string
	Timeout time.Duration
}

// coreOutput mirrors the CLI contract implemented by packages/core/src/cli.js.
type coreOutput struct {
	Response *struct {
		Status  *int `json:"status"`
		Headers []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"headers"`
		Body   string `json:"body"`
		TimeMS int    `json:"timeMs"`
	} `json:"response"`
	TestResults []struct {
		Name   string `json:"name"`
		Passed bool   `json:"passed"`
		Error  string `json:"error,omitempty"`
	} `json:"testResults"`
	Error *string `json:"error"`
}

// NewCommandRunnerFromEnv builds the runner from MONITOR_RUNNER_CMD and
// RELAY_CORE_PATH. The default command is `node dist-core/relay-core.mjs`
// executed inside RELAY_CORE_PATH. A missing bundle surfaces as a per-run
// error; it never turns into a passing run.
func NewCommandRunnerFromEnv() *CommandRunner {
	dir := strings.TrimSpace(os.Getenv("RELAY_CORE_PATH"))
	if dir == "" {
		if cwd, err := os.Getwd(); err == nil {
			dir = cwd
		}
	}

	raw := strings.TrimSpace(os.Getenv("MONITOR_RUNNER_CMD"))
	args := strings.Fields(raw)
	if len(args) == 0 {
		args = []string{"node", "dist-core/relay-core.mjs"}
	}

	timeout := 60 * time.Second
	if rawTimeout := strings.TrimSpace(os.Getenv("MONITOR_RUNNER_TIMEOUT")); rawTimeout != "" {
		if parsed, err := time.ParseDuration(rawTimeout); err == nil && parsed > 0 {
			timeout = parsed
		}
	}

	return &CommandRunner{Args: args, Dir: dir, Timeout: timeout}
}

// Run executes the command with the job on stdin and parses its JSON stdout.
func (r *CommandRunner) Run(ctx context.Context, job Job) (Result, error) {
	if len(r.Args) == 0 {
		return Result{}, errors.New("monitor runner unavailable: no command configured")
	}
	if err := r.checkAvailable(); err != nil {
		return Result{}, err
	}

	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	payload, err := json.Marshal(job)
	if err != nil {
		return Result{}, fmt.Errorf("encode monitor job: %w", err)
	}

	// #nosec G204 -- the command comes from operator configuration
	// (MONITOR_RUNNER_CMD), never from request/user input, and is executed
	// without a shell.
	cmd := exec.CommandContext(ctx, r.Args[0], r.Args[1:]...)
	cmd.Dir = r.Dir
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = runnerEnv()

	runErr := cmd.Run()
	result, parseErr := parseCoreOutput(stdout.Bytes())
	if parseErr == nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Error = fmt.Sprintf("monitor runner timed out after %s", r.Timeout)
		} else if runErr != nil && result.Error == "" {
			result.Error = exitError(runErr, stderr.String())
		}
		return result, nil
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("monitor runner timed out after %s", r.Timeout)
	}
	if runErr != nil {
		return Result{}, fmt.Errorf("monitor runner failed: %s", exitError(runErr, stderr.String()))
	}
	return Result{}, fmt.Errorf("monitor runner produced invalid JSON: %w", parseErr)
}

func (r *CommandRunner) checkAvailable() error {
	bin := r.Args[0]
	path, err := exec.LookPath(bin)
	if err != nil {
		if !strings.ContainsRune(bin, filepath.Separator) {
			return fmt.Errorf("monitor runner unavailable: executable %q not found: %w", bin, err)
		}
		path = bin
	}
	if strings.ContainsRune(bin, filepath.Separator) {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("monitor runner unavailable: %w", err)
		}
	}

	// `node dist-core/relay-core.mjs` is resolved against Dir by Node itself.
	// Check the script explicitly so a missing bundle gives an actionable error
	// instead of Node's module-not-found text.
	if len(r.Args) > 1 && !strings.HasPrefix(r.Args[1], "-") && filepath.Ext(r.Args[1]) != "" {
		script := r.Args[1]
		if !filepath.IsAbs(script) {
			script = filepath.Join(r.Dir, script)
		}
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("monitor runner unavailable: core bundle %q not found (set RELAY_CORE_PATH or MONITOR_RUNNER_CMD): %w", script, err)
		}
	}
	return nil
}

func parseCoreOutput(stdout []byte) (Result, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return Result{}, errors.New("empty stdout")
	}
	var out coreOutput
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return Result{}, err
	}

	result := Result{}
	for _, test := range out.TestResults {
		if !test.Passed {
			result.AssertionFailures++
		}
	}
	if out.Error != nil {
		result.Error = strings.TrimSpace(*out.Error)
	}
	if out.Response == nil {
		if result.Error == "" {
			result.Error = "monitor runner returned no response"
		}
		return result, nil
	}
	if out.Response.Status != nil {
		result.StatusCode = *out.Response.Status
	}
	result.DurationMS = out.Response.TimeMS
	if out.Response.Status == nil && result.Error == "" {
		result.Error = "monitor request was not sent"
	}
	return result, nil
}

// runnerEnv passes only what Node needs to run, never the API's secrets
// (DB_PASSWORD, JWT_SECRET, provider credentials).
func runnerEnv() []string {
	env := []string{"NODE_ENV=production"}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func exitError(runErr error, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if len(stderr) > 4096 {
		stderr = stderr[:4096] + "…"
	}
	if stderr == "" {
		return runErr.Error()
	}
	// Static stderr only by contract, but avoid unbounded/duplicate text.
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return fmt.Sprintf("runner exited with status %d: %s", exitErr.ExitCode(), stderr)
	}
	return fmt.Sprintf("%v: %s", runErr, stderr)
}

var _ Runner = (*CommandRunner)(nil)

// UnavailableRunner fails every run with reason. It keeps the worker alive so
// failures are recorded (and alerted) instead of silently skipping monitors.
type UnavailableRunner struct {
	Reason string
}

func (u *UnavailableRunner) Run(context.Context, Job) (Result, error) {
	return Result{}, fmt.Errorf("monitor runner unavailable: %s", u.Reason)
}
