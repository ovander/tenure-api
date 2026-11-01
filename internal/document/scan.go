package document

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Verdict is the outcome of an antivirus scan.
type Verdict struct {
	Clean bool
	// Signature names what was found when the file is not clean.
	Signature string
}

// Scanner scans an upload before it reaches the archive (RENT-BE-001 §8b).
// An error means no verdict: the caller refuses the upload (fail closed).
type Scanner interface {
	Scan(ctx context.Context, r io.Reader) (Verdict, error)
	Ping(ctx context.Context) error
}

// Clamd is the Scanner over a ClamAV daemon sidecar, through its INSTREAM
// command on TCP.
type Clamd struct {
	Addr string // host:port
	// Timeout bounds a whole scan; zero means two minutes.
	Timeout time.Duration
}

// ErrScanner reports a scanner that gave no verdict.
var ErrScanner = errors.New("document: antivirus scanner unavailable")

func (c Clamd) dial(ctx context.Context) (net.Conn, error) {
	t := c.Timeout
	if t == 0 {
		t = 2 * time.Minute
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrScanner, err)
	}
	deadline := time.Now().Add(t)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	return conn, nil
}

// Ping implements Scanner.
func (c Clamd) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return fmt.Errorf("%w: %v", ErrScanner, err)
	}
	reply, err := bufio.NewReader(conn).ReadString(0)
	if err != nil || strings.TrimRight(reply, "\x00") != "PONG" {
		return fmt.Errorf("%w: ping answered %q (%v)", ErrScanner, reply, err)
	}
	return nil
}

// Scan implements Scanner: the stream is sent in chunks, then the daemon's
// one-line answer is parsed ("stream: OK" or "stream: <name> FOUND"). Any
// other answer, a size-limit error included, is an error.
func (c Clamd) Scan(ctx context.Context, r io.Reader) (Verdict, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return Verdict{}, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScanner, err)
	}
	buf := make([]byte, 64<<10)
	var size [4]byte
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			binary.BigEndian.PutUint32(size[:], uint32(n))
			if _, err := conn.Write(size[:]); err != nil {
				return Verdict{}, fmt.Errorf("%w: %v", ErrScanner, err)
			}
			if _, err := conn.Write(buf[:n]); err != nil {
				return Verdict{}, fmt.Errorf("%w: %v", ErrScanner, err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Verdict{}, rerr
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScanner, err)
	}
	reply, err := bufio.NewReader(conn).ReadString(0)
	if err != nil && reply == "" {
		return Verdict{}, fmt.Errorf("%w: %v", ErrScanner, err)
	}
	return parseClamd(strings.TrimRight(reply, "\x00\n"))
}

func parseClamd(reply string) (Verdict, error) {
	body := strings.TrimPrefix(reply, "stream: ")
	switch {
	case body == "OK":
		return Verdict{Clean: true}, nil
	case strings.HasSuffix(body, " FOUND"):
		return Verdict{Signature: strings.TrimSuffix(body, " FOUND")}, nil
	}
	return Verdict{}, fmt.Errorf("%w: answer %q", ErrScanner, reply)
}
