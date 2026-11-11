package sanitize

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// exifPool keeps exiftool processes in -stay_open mode so a call skips
// perl's startup and module loading, most of an image's sanitize time.
// exiftool treats every -execute as a separate command line with a fresh
// ExifTool object, so options don't carry over between calls.
type exifPool struct {
	path, dir string
	maxIdle   int

	mu     sync.Mutex
	idle   []*exifWorker
	closed bool
}

// exifWorkerUses recycles a worker after this many commands.
const exifWorkerUses = 500

var errRunnerClosed = errors.New("runner closed")

func (p *exifPool) get() (*exifWorker, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errRunnerClosed
	}
	if n := len(p.idle); n > 0 {
		w := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return w, nil
	}
	p.mu.Unlock()
	return p.start()
}

func (p *exifPool) put(w *exifWorker) {
	p.mu.Lock()
	if !p.closed && w.uses < exifWorkerUses && len(p.idle) < p.maxIdle {
		p.idle = append(p.idle, w)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	w.kill()
}

func (p *exifPool) close() {
	p.mu.Lock()
	idle := p.idle
	p.idle, p.closed = nil, true
	p.mu.Unlock()
	for _, w := range idle {
		w.kill()
	}
}

func (p *exifPool) start() (*exifWorker, error) {
	cmd := exec.Command(p.path, "-config", "", "-stay_open", "True", "-@", "-")
	cmd.Env = []string{}
	cmd.Dir = p.dir
	cmd.SysProcAttr = workerSysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &exifWorker{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: bufio.NewReader(stderr)}, nil
}

type exifWorker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bufio.Reader
	uses   int
}

type exifResult struct {
	stdout []byte
	stderr string
	status int
}

// exec runs one command. On error or cancellation the worker is unusable.
func (w *exifWorker) exec(ctx context.Context, args []string) (exifResult, error) {
	w.uses++
	// A random id keeps file contents from forging the end-of-output
	// markers; -execute only accepts a number.
	var idb [8]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return exifResult{}, err
	}
	id := strconv.FormatUint(binary.LittleEndian.Uint64(idb[:]), 10)
	var b strings.Builder
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "-echo4\n{ready%s:${status}}\n-execute%s\n", id, id)

	var res exifResult
	done := make(chan error, 1)
	go func() { done <- w.exchange(b.String(), id, &res) }()
	select {
	case err := <-done:
		return res, err
	case <-ctx.Done():
		_ = w.cmd.Process.Kill()
		<-done
		return exifResult{}, ctx.Err()
	}
}

func (w *exifWorker) exchange(command, id string, res *exifResult) error {
	if _, err := io.WriteString(w.stdin, command); err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		var err error
		res.stderr, res.status, err = readStderr(w.stderr, "{ready"+id+":")
		errc <- err
	}()
	var err error
	res.stdout, err = readStdout(w.stdout, "{ready"+id+"}\n")
	if serr := <-errc; err == nil {
		err = serr
	}
	return err
}

// readStdout reads up to the line marking the end of the command's output.
func readStdout(r *bufio.Reader, marker string) ([]byte, error) {
	var out []byte
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return out, err
		}
		if string(line) == marker {
			return out, nil
		}
		out = append(out, line...)
	}
}

// readStderr reads up to the -echo4 line carrying the exit status.
func readStderr(r *bufio.Reader, prefix string) (string, int, error) {
	var out strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return out.String(), 0, err
		}
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			status, err := strconv.Atoi(strings.TrimSuffix(rest, "}\n"))
			return out.String(), status, err
		}
		out.WriteString(line)
	}
}

func (w *exifWorker) kill() {
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
}

// validArg reports whether exiftool reads the argument back unchanged from
// its argument file: one line, not blank, no leading white space and no
// comment marker.
func validArg(a string) bool {
	return a != "" && !strings.ContainsAny(a, "\r\n") && !strings.HasPrefix(a, "#") &&
		strings.TrimLeft(a, " \t") == a
}
