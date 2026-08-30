package pdftotext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
)

const (
	DefaultTimeout     = 10 * time.Second
	DefaultOutputLimit = 16 << 20
	DefaultStderrLimit = 16 << 10
	executableName     = "pdftotext"

	// waitDelay bounds the wait for the output pipes after the process stops.
	// A child that leaves a pipe open cannot hold the caller (D51).
	waitDelay = 2 * time.Second
)

var (
	ErrExecutableNotFound = errors.New("pdftotext: executable not found; install Poppler and ensure pdftotext is on PATH")
	ErrOutputTooLarge     = errors.New("pdftotext: coordinate output exceeds the configured limit")
	ErrTimedOut           = errors.New("pdftotext: extraction timed out")
	ErrCanceled           = errors.New("pdftotext: extraction canceled by the caller")
	ErrProcessFailed      = errors.New("pdftotext: process exited unsuccessfully")
	ErrExecution          = errors.New("pdftotext: process could not be executed")
)

// processEnvironment is the complete environment of the child. BillyCore holds
// a bearer token and OAuth credentials in its own environment, and hostile
// input must not reach them (SECURITY.md §10, §11).
var processEnvironment = []string{
	"LANG=C",
	"LC_ALL=C",
	"TZ=UTC",
}

type resolver func(string) (string, error)

// Extractor runs pdftotext with a fixed argument and environment contract.
type Extractor struct {
	timeout     time.Duration
	outputLimit int
	stderrLimit int
	resolve     resolver
}

func New() *Extractor {
	return &Extractor{
		timeout:     DefaultTimeout,
		outputLimit: DefaultOutputLimit,
		stderrLimit: DefaultStderrLimit,
		resolve:     exec.LookPath,
	}
}

// Extract returns coordinate XHTML/XML. It reads the PDF from stdin, so it
// creates no temporary file and does not change the input bytes (D7, D54).
func (e *Extractor) Extract(ctx context.Context, pdf []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCanceled, err)
	}

	executable, err := e.resolve(executableName)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrExecutableNotFound
		}
		return nil, fmt.Errorf("%w: executable resolution failed", ErrExecution)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("%w: executable path is invalid", ErrExecution)
	}

	runCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// Overflow stops the child immediately. Without this, a PDF that expands
	// without end keeps a process alive until the timeout.
	stdout := newLimitedBuffer(e.outputLimit, cancel)
	stderr := newLimitedBuffer(e.stderrLimit, func() {})

	// The arguments are passed one by one and no shell reads them. The two
	// dashes are the stdin input file and the stdout output file.
	cmd := exec.CommandContext(runCtx, executable, "-bbox-layout", "-", "-")
	cmd.Env = append([]string(nil), processEnvironment...)
	cmd.Stdin = bytes.NewReader(pdf)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	// os/exec copies each pipe in its own goroutine, so a child that writes a
	// large diagnostic and a large document at the same time cannot deadlock.
	err = cmd.Run()

	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%w: %w", ErrCanceled, ctx.Err())
	case stdout.overflow:
		return nil, ErrOutputTooLarge
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return nil, ErrTimedOut
	case err == nil:
		return stdout.bytes(), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, processError(exitErr.ExitCode(), stderr)
	}
	return nil, fmt.Errorf("%w: process did not start or finish correctly", ErrExecution)
}

// processError names the exit code and the shape of stderr. The text of stderr
// stays out, because a pdftotext diagnostic can quote the document (D51).
func processError(exitCode int, stderr *limitedBuffer) error {
	diagnostic := "stderr was empty"
	if stderr.overflow {
		diagnostic = "stderr was redacted and truncated"
	} else if len(stderr.data) > 0 {
		diagnostic = "stderr was redacted"
	}
	return fmt.Errorf("%w: exit code %d; %s", ErrProcessFailed, exitCode, diagnostic)
}

// limitedBuffer keeps at most limit bytes. It reports every byte as written,
// so the child sees no error and stops through onOverflow instead.
type limitedBuffer struct {
	data       []byte
	limit      int
	overflow   bool
	onOverflow func()
}

func newLimitedBuffer(limit int, onOverflow func()) *limitedBuffer {
	return &limitedBuffer{limit: limit, onOverflow: onOverflow}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	available := b.limit - len(b.data)
	if available > len(p) {
		available = len(p)
	}
	if available > 0 {
		b.data = append(b.data, p[:available]...)
	}
	if available < len(p) && !b.overflow {
		b.overflow = true
		b.onOverflow()
	}
	return len(p), nil
}

func (b *limitedBuffer) bytes() []byte {
	return append([]byte(nil), b.data...)
}

var _ app.PDFCoordinateExtractor = (*Extractor)(nil)
