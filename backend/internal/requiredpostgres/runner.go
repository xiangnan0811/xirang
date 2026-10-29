package requiredpostgres

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var requiredEnvironment = []string{
	"REQUIRE_POSTGRES_MIGRATION_TEST", "REQUIRE_POSTGRES_RECOVERY_TEST", "REQUIRE_POSTGRES_CATALOG_TEST", "REQUIRE_POSTGRES_SEARCH_TEST", "REQUIRE_POSTGRES_OVERLAY_TEST", "REQUIRE_POSTGRES_CONTENT_TEST", "REQUIRE_POSTGRES_PROCESSING_TEST", "REQUIRE_POSTGRES_EXPORT_TEST", "REQUIRE_POSTGRES_GENERAL_TEST", "REQUIRE_POSTGRES_TASK_TERMINAL_TEST", "REQUIRE_POSTGRES_ESCALATION_PREVIEW_TEST", "REQUIRE_POSTGRES_LEASE_TEST", "REQUIRE_POSTGRES_REPOSITORY_TEST", "REQUIRE_POSTGRES_PRODUCER_RETENTION_TEST",
}

func commandEnv() []string {
	env := []string{}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if key == "GOFLAGS" || contains(requiredEnvironment, key) {
			continue
		}
		env = append(env, e)
	}
	for _, key := range requiredEnvironment {
		env = append(env, key+"=1")
	}
	return env
}
func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func ValidateNative(inv Inventory) error {
	declarations := map[string][]string{}
	for _, group := range inv.Groups {
		for _, test := range group.Tests {
			declarations[group.Package] = append(declarations[group.Package], test.Name)
		}
	}
	for _, exclusion := range inv.Exclusions {
		declarations[exclusion.Package] = append(declarations[exclusion.Package], exclusion.Name)
	}
	packages := make([]string, 0, len(declarations))
	for pkg := range declarations {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	for _, pkg := range packages {
		cmd := exec.Command("go", "test", pkg, "-list", "^Test", "-count=1")
		cmd.Env = commandEnv()
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("native test listing failed for %s", pkg)
		}
		names := map[string]bool{}
		for _, line := range strings.Split(string(output), "\n") {
			if testName.MatchString(line) {
				names[line] = true
			}
		}
		for _, name := range declarations[pkg] {
			if !names[name] {
				return fmt.Errorf("native test missing: %s/%s", pkg, name)
			}
		}
	}
	return nil
}

type event struct {
	Action  string
	Package string
	Test    string
}
type state struct{ run, pass, fail, skip bool }
type evidence struct {
	Package         string   `json:"package"`
	Exit            int      `json:"exit"`
	PackagePass     bool     `json:"package_pass"`
	Tests           int      `json:"tests"`
	TestsRun        int      `json:"tests_run"`
	TestsPass       int      `json:"tests_pass"`
	Required        int      `json:"required"`
	RequiredRun     int      `json:"required_run"`
	RequiredPass    int      `json:"required_pass"`
	RequiredMissing int      `json:"required_missing"`
	RequiredSkipped int      `json:"required_skipped"`
	RequiredFailed  int      `json:"required_failed"`
	OptionalSkips   int      `json:"optional_skips"`
	Errors          []string `json:"errors"`
}

func parseEvents(r io.Reader, group Group) evidence {
	result := evidence{Package: group.Package, Errors: []string{}}
	states := map[string]*state{}
	pkg := "xirang/backend/" + strings.TrimPrefix(group.Package, "./")
	selected := map[string]bool{}
	for _, t := range group.Tests {
		selected[t.Name] = true
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			result.Errors = append(result.Errors, "invalid JSON event")
			continue
		}
		if e.Package != pkg {
			result.Errors = append(result.Errors, "unexpected package identity")
			continue
		}
		if e.Test == "" {
			if e.Action == "pass" {
				result.PackagePass = true
			}
			if e.Action == "fail" {
				result.Errors = append(result.Errors, "package failed")
			}
			continue
		}
		top, _, _ := strings.Cut(e.Test, "/")
		if !selected[top] {
			result.Errors = append(result.Errors, "unexpected test identity")
			continue
		}
		s := states[e.Test]
		if s == nil {
			s = &state{}
			states[e.Test] = s
		}
		switch e.Action {
		case "run":
			if s.run || s.pass || s.fail || s.skip {
				result.Errors = append(result.Errors, "invalid run sequence: "+e.Test)
			}
			s.run = true
		case "pass", "fail", "skip":
			if !s.run || s.pass || s.fail || s.skip {
				result.Errors = append(result.Errors, "invalid terminal sequence: "+e.Test)
			}
			switch e.Action {
			case "pass":
				s.pass = true
			case "fail":
				s.fail = true
			case "skip":
				s.skip = true
			}
		case "output", "pause", "cont":
		default:
			result.Errors = append(result.Errors, "invalid test event action")
		}
	}
	if scanner.Err() != nil {
		result.Errors = append(result.Errors, "event line exceeds limit or could not be read")
	}
	if !result.PackagePass {
		result.Errors = append(result.Errors, "missing package pass")
	}
	required := []string{}
	for _, t := range group.Tests {
		result.Tests++
		required = append(required, t.Name)
		s := states[t.Name]
		if s != nil {
			if s.run {
				result.TestsRun++
			}
			if s.pass && !s.fail && !s.skip {
				result.TestsPass++
			}
		}
		for _, p := range t.RequiredPaths {
			required = append(required, p)
			result.Required++
			s := states[p]
			if s == nil || !s.run || (!s.pass && !s.fail && !s.skip) {
				result.RequiredMissing++
			}
			if s != nil {
				if s.run {
					result.RequiredRun++
				}
				if s.pass && !s.fail && !s.skip {
					result.RequiredPass++
				}
			}
		}
	}
	for _, p := range required {
		s := states[p]
		if s == nil || !s.run || !s.pass || s.skip || s.fail {
			result.Errors = append(result.Errors, "required test missing or failed: "+p)
		}
	}
	for name, s := range states {
		if !s.skip && !s.fail {
			continue
		}
		under := false
		for _, t := range group.Tests {
			for _, p := range t.RequiredPaths {
				if name == p || strings.HasPrefix(name, p+"/") {
					under = true
				}
			}
		}
		if under {
			if s.skip {
				result.RequiredSkipped++
			}
			if s.fail {
				result.RequiredFailed++
			}
			result.Errors = append(result.Errors, "required descendant skipped or failed: "+name)
		} else if s.skip {
			result.OptionalSkips++
		} else {
			result.Errors = append(result.Errors, "test failed: "+name)
		}
	}
	sort.Strings(result.Errors)
	return result
}

func selector(g Group) string {
	names := make([]string, 0, len(g.Tests))
	for _, t := range g.Tests {
		names = append(names, regexp.QuoteMeta(t.Name))
	}
	return "^(" + strings.Join(names, "|") + ")$"
}

// eventWriter buffers at most one bounded line and sanitizes the untrusted
// output field before persisting. Malformed lines remain malformed so the
// protocol parser fails closed without recording their potentially secret text.
type eventWriter struct {
	writer  io.Writer
	pending []byte
	secrets []string
	failed  bool
}

func (w *eventWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) != 0 {
		i := bytes.IndexByte(b, '\n')
		part := b
		if i >= 0 {
			part = b[:i]
			b = b[i+1:]
		} else {
			b = nil
		}
		if len(w.pending)+len(part) > 4*1024*1024 {
			w.failed = true
			return 0, fmt.Errorf("event line exceeds limit")
		}
		w.pending = append(w.pending, part...)
		if i < 0 {
			break
		}
		var e map[string]json.RawMessage
		if json.Unmarshal(w.pending, &e) != nil || e == nil {
			if _, err := io.WriteString(w.writer, "invalid event\n"); err != nil {
				return 0, err
			}
		} else {
			for key, value := range e {
				if key != "Time" && key != "Action" && key != "Package" && key != "Test" && key != "Elapsed" && key != "Output" {
					delete(e, key)
					continue
				}
				if key == "Output" {
					e[key] = json.RawMessage(`"[redacted]"`)
					continue
				}
				var text string
				if json.Unmarshal(value, &text) == nil {
					for _, secret := range w.secrets {
						if secret != "" {
							text = strings.ReplaceAll(text, secret, "[redacted]")
						}
					}
					e[key], _ = json.Marshal(text)
				}
			}
			encoded, err := json.Marshal(e)
			if err != nil {
				return 0, err
			}
			encoded = append(encoded, '\n')
			if _, err := w.writer.Write(encoded); err != nil {
				return 0, err
			}
		}
		w.pending = w.pending[:0]
	}
	return n, nil
}
func (w *eventWriter) finish() error {
	if len(w.pending) > 0 {
		w.failed = true
		_, err := io.WriteString(w.writer, "invalid event\n")
		return err
	}
	return nil
}

// Keep stderr diagnostics but remove the DSN and URL password before writing.
// Line framing prevents a secret split across writes from escaping redaction.
type stderrWriter struct {
	writer  io.Writer
	pending []byte
	secrets []string
	failed  bool
}

func (w *stderrWriter) flush() error {
	line := string(w.pending)
	for _, secret := range w.secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[redacted]")
		}
	}
	_, err := io.WriteString(w.writer, line)
	w.pending = w.pending[:0]
	return err
}
func (w *stderrWriter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		part := b
		if i >= 0 {
			part = b[:i+1]
			b = b[i+1:]
		} else {
			b = nil
		}
		if len(w.pending)+len(part) > 4*1024*1024 {
			w.failed = true
			return 0, fmt.Errorf("stderr line exceeds limit")
		}
		w.pending = append(w.pending, part...)
		if i >= 0 {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func Run(inv Inventory, evidenceDir string) error {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("TEST_POSTGRES_DSN is required")
	}
	override, present := os.LookupEnv("POSTGRES_TEST_TIMEOUT")
	if present {
		d, err := time.ParseDuration(override)
		if err != nil || d <= 0 {
			return fmt.Errorf("POSTGRES_TEST_TIMEOUT must be a positive duration")
		}
	}
	if evidenceDir == "" {
		var err error
		evidenceDir, err = os.MkdirTemp("", "required-postgres-")
		if err != nil {
			return err
		}
		fmt.Printf("evidence: %s\n", evidenceDir)
	}
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		return err
	}
	failed := false
	for i, g := range inv.Groups {
		prefix := filepath.Join(evidenceDir, fmt.Sprintf("%03d", i+1))
		out, err := os.OpenFile(prefix+".jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		stderr, err := os.OpenFile(prefix+".stderr", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.Join(err, out.Close())
		}
		timeout := g.Timeout
		if override != "" {
			timeout = override
		}
		secrets := []string{dsn}
		if u, err := url.Parse(dsn); err == nil && u.User != nil {
			if password, ok := u.User.Password(); ok {
				secrets = append(secrets, password)
			}
		}
		ew := &eventWriter{writer: out, secrets: secrets}
		sw := &stderrWriter{writer: stderr, secrets: secrets}
		cmd := exec.Command("go", "test", g.Package, "-run", selector(g), "-json", "-count=1", "-timeout="+timeout)
		cmd.Env = commandEnv()
		cmd.Stdout = ew
		cmd.Stderr = sw
		runErr := cmd.Run()
		finishErr := ew.finish()
		stderrFinishErr := sw.flush()
		closeErr := out.Close()
		stderrErr := stderr.Close()
		f, err := os.Open(prefix + ".jsonl")
		if err != nil {
			return err
		}
		result := parseEvents(f, g)
		if err := f.Close(); err != nil {
			result.Errors = append(result.Errors, "closing evidence reader failed")
		}
		result.Exit = 0
		if runErr != nil {
			result.Exit = -1
			if e, ok := runErr.(*exec.ExitError); ok {
				result.Exit = e.ExitCode()
			}
			result.Errors = append(result.Errors, "test process failed")
		}
		if finishErr != nil || stderrFinishErr != nil || ew.failed || sw.failed || closeErr != nil || stderrErr != nil {
			result.Errors = append(result.Errors, "writing evidence failed")
		}
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(prefix+".summary.json", b, 0600); err != nil {
			return err
		}
		fmt.Printf("%s: tests=%d required=%d optional_skips=%d errors=%d\n", g.Package, result.Tests, result.Required, result.OptionalSkips, len(result.Errors))
		for _, problem := range result.Errors {
			fmt.Fprintf(os.Stderr, "%s: %s\n", g.Package, problem)
		}
		if len(result.Errors) != 0 {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("required PostgreSQL tests failed")
	}
	return nil
}
