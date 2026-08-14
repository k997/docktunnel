// Package instance provides single-instance enforcement via an exclusive
// file lock. DockTunnel assumes one process per tunnel — two instances
// racing the same Cloudflare tunnel configuration will thrash DNS records
// and ingress rules.
package instance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// ErrAlreadyRunning is returned by Acquire when the lock is already held
// by another live process.
var ErrAlreadyRunning = errors.New("another docktunnel instance is already running")

// Lock holds an exclusive flock on a file. Closing it releases the lock.
type Lock struct {
	f *os.File
}

// Acquire takes an exclusive flock on path. The directory is created if
// missing. Returns ErrAlreadyRunning if another process holds the lock.
//
// The lock is released automatically when the process exits (the kernel
// drops the fd and the flock with it) — no signal handler is needed for
// the common case. Use Close() for explicit release during graceful
// shutdown so the next Acquire doesn't have to wait for fd cleanup.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	// O_RDWR even though we don't read: fcntl locks require an fd opened
	// for at least one of read/write matching the lock type, and F_WRLCK
	// needs write.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	// Non-blocking exclusive lock. EAGAIN/EWOULDBLOCK means someone else
	// holds it.
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			other := readHolderPID(f)
			f.Close()
			if other > 0 {
				return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, other)
			}
			return nil, ErrAlreadyRunning
		}
		f.Close()
		return nil, fmt.Errorf("acquire flock: %w", err)
	}

	// Best-effort: stamp our own pid into the file so the next would-be
	// holder can report who's blocking them. Truncate first to avoid
	// leaving stale bytes from a longer previous pid.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, io.SeekStart)
	_, _ = io.WriteString(f, fmt.Sprintf("%d\n", os.Getpid()))

	return &Lock{f: f}, nil
}

// Close releases the lock and closes the underlying file.
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	// Explicit unlock so a concurrent Acquire from a different process
	// doesn't have to wait for the kernel to clean up our fd. Best-effort
	// — Close() will drop the lock anyway.
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	return l.f.Close()
}

// readHolderPID tries to read the pid the current holder stamped. Best-effort.
func readHolderPID(f *os.File) int {
	_, _ = f.Seek(0, io.SeekStart)
	buf := make([]byte, 32)
	n, _ := f.Read(buf)
	if n <= 0 {
		return 0
	}
	pid, err := strconv.Atoi(string(buf[:firstNewlineOrLen(buf[:n])]))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func firstNewlineOrLen(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return len(b)
}

// AcquireWithDefault is a convenience that defaults path to
// /var/lib/docktunnel/instance.lock when the caller passes "".
func AcquireWithDefault(path string) (*Lock, error) {
	if path == "" {
		path = "/var/lib/docktunnel/instance.lock"
	}
	return Acquire(path)
}
