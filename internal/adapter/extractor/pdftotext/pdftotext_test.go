package pdftotext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperTimeout is the extractor timeout for a case that must reach the child.
// The child is this test binary, and it needs close to a second to start under
// the race detector. Only a case about time uses a short timeout.
const helperTimeout = 30 * time.Second

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "-bbox-layout" {
		runHelperProcess()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runHelperProcess() {
	if os.Args[2] != "-" || os.Args[3] != "-" {
		os.Exit(97)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(96)
	}
	command := string(input)
	switch {
	case command == "SUCCESS":
		_, _ = io.WriteString(os.Stdout, "<html><body><doc/></body></html>")
	case command == "ENV":
		_, _ = io.WriteString(os.Stdout, strings.Join(os.Environ(), "\n"))
	case strings.HasPrefix(command, "OUTPUT:"):
		n, err := strconv.Atoi(strings.TrimPrefix(command, "OUTPUT:"))
		if err != nil {
			os.Exit(95)
		}
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", n))
	case command == "WAIT":
		time.Sleep(5 * time.Second)
	case command == "FLOOD":
		chunk := strings.Repeat("y", 4096)
		for {
			if _, err := io.WriteString(os.Stdout, chunk); err != nil {
				os.Exit(94)
			}
		}
	case command == "BOTH":
		chunk := strings.Repeat("z", 4096)
		for i := 0; i < 64; i++ {
			_, _ = io.WriteString(os.Stderr, chunk)
			_, _ = io.WriteString(os.Stdout, chunk)
		}
	case command == "FAIL":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("PRIVATE-PDF-TEXT", 16_384))
		os.Exit(23)
	default:
		_, _ = os.Stdout.Write(input)
	}
}

func TestExtractSuccessUsesExactArguments(t *testing.T) {
	e := helperExtractor(t, helperTimeout, 1<<10, 1<<10)
	got, err := e.Extract(context.Background(), []byte("SUCCESS"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if want := "<html><body><doc/></body></html>"; string(got) != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestExtractDoesNotUseAShell(t *testing.T) {
	e := helperExtractor(t, helperTimeout, 1<<10, 1<<10)
	created := filepath.Join(t.TempDir(), "shell-ran")
	pdf := []byte(fmt.Sprintf("$(touch %s)", created))

	got, err := e.Extract(context.Background(), pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(got) != string(pdf) {
		t.Fatalf("output = %q, want input unchanged", got)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shell payload created %s", created)
	}
}

func TestExtractUsesControlledEnvironment(t *testing.T) {
	t.Setenv("BILLYCORE_TOKEN", "must-not-reach-child")
	t.Setenv("PDF_PRIVATE_VALUE", "must-not-reach-child")
	e := helperExtractor(t, helperTimeout, 1<<10, 1<<10)

	got, err := e.Extract(context.Background(), []byte("ENV"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if want := strings.Join(processEnvironment, "\n"); string(got) != want {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

func TestExtractTimesOut(t *testing.T) {
	e := helperExtractor(t, 20*time.Millisecond, 1<<10, 1<<10)
	_, err := e.Extract(context.Background(), []byte("WAIT"))
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Extract error = %v, want ErrTimedOut", err)
	}
}

func TestExtractRespectsCallerCancellation(t *testing.T) {
	e := helperExtractor(t, helperTimeout, 1<<10, 1<<10)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	time.AfterFunc(20*time.Millisecond, cancel)

	_, err := e.Extract(ctx, []byte("WAIT"))
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract error = %v, want caller cancellation", err)
	}
}

func TestExtractAcceptsOutputExactlyAtLimit(t *testing.T) {
	const limit = 64
	e := helperExtractor(t, helperTimeout, limit, 1<<10)
	got, err := e.Extract(context.Background(), []byte("OUTPUT:64"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(got) != limit {
		t.Fatalf("output length = %d, want %d", len(got), limit)
	}
}

func TestExtractRejectsLimitPlusOneByte(t *testing.T) {
	const limit = 64
	e := helperExtractor(t, helperTimeout, limit, 1<<10)
	_, err := e.Extract(context.Background(), []byte("OUTPUT:65"))
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("Extract error = %v, want ErrOutputTooLarge", err)
	}
}

func TestExtractBoundsAndRedactsStderrOnNonZeroExit(t *testing.T) {
	e := helperExtractor(t, helperTimeout, 1<<10, 32)
	_, err := e.Extract(context.Background(), []byte("FAIL"))
	if !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("Extract error = %v, want ErrProcessFailed", err)
	}
	if strings.Contains(err.Error(), "PRIVATE-PDF-TEXT") || len(err.Error()) > 160 {
		t.Fatalf("error exposes stderr: %q", err)
	}
	if !strings.Contains(err.Error(), "exit code 23") || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("error lacks bounded diagnostic: %q", err)
	}
}

func TestExtractReportsMissingExecutable(t *testing.T) {
	e := New()
	e.resolve = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err := e.Extract(context.Background(), []byte("SUCCESS"))
	if !errors.Is(err, ErrExecutableNotFound) || !strings.Contains(err.Error(), "install Poppler") {
		t.Fatalf("Extract error = %v, want actionable missing executable error", err)
	}
}

func TestExtractSeparatesMalformedExecution(t *testing.T) {
	e := New()
	e.resolve = func(string) (string, error) { return t.TempDir(), nil }
	_, err := e.Extract(context.Background(), []byte("SUCCESS"))
	if !errors.Is(err, ErrExecution) {
		t.Fatalf("Extract error = %v, want ErrExecution", err)
	}
}

func helperExtractor(t *testing.T, timeout time.Duration, outputLimit, stderrLimit int) *Extractor {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	e := New()
	e.timeout = timeout
	e.outputLimit = outputLimit
	e.stderrLimit = stderrLimit
	e.resolve = func(name string) (string, error) {
		if name != executableName {
			t.Fatalf("resolved executable = %q, want %q", name, executableName)
		}
		return executable, nil
	}
	return e
}

// A serial reader deadlocks here: both pipes fill past their kernel buffer
// before the child exits.
func TestExtractReadsStdoutAndStderrConcurrently(t *testing.T) {
	const written = 64 * 4096
	e := helperExtractor(t, helperTimeout, written, 4096)

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := e.Extract(context.Background(), []byte("BOTH"))
		if err != nil {
			t.Errorf("Extract: %v", err)
			return
		}
		if len(got) != written {
			t.Errorf("output length = %d, want %d", len(got), written)
		}
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Extract deadlocked on the child output pipes")
	}
}

func TestExtractStopsTheChildOnOverflow(t *testing.T) {
	e := helperExtractor(t, helperTimeout, 4096, 1<<10)

	start := time.Now()
	_, err := e.Extract(context.Background(), []byte("FLOOD"))
	elapsed := time.Since(start)

	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("Extract error = %v, want ErrOutputTooLarge", err)
	}
	if elapsed >= 10*time.Second {
		t.Fatalf("Extract waited %s; it must stop the child at the limit", elapsed)
	}
}

// D7 and D54: the artifact is immutable and stays in memory. Extraction reads
// stdin, so it writes no temporary copy of the PDF to disk.
func TestExtractWritesNoTemporaryFileAndKeepsTheInput(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	e := helperExtractor(t, helperTimeout, 1<<10, 1<<10)

	pdf := []byte("SUCCESS")
	original := append([]byte(nil), pdf...)
	if _, err := e.Extract(context.Background(), pdf); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if !bytes.Equal(pdf, original) {
		t.Fatalf("input = %q, want it unchanged", pdf)
	}
	entries, err := os.ReadDir(temporary)
	if err != nil {
		t.Fatalf("read temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directory holds %d entries, want none", len(entries))
	}
}
