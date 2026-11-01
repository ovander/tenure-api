package document

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestSniff(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	cases := []struct {
		name, file, wantType, wantExt string
		head                          []byte
		ok                            bool
	}{
		{"pdf", "bail.pdf", "application/pdf", ".pdf", []byte("%PDF-1.7\n%âãÏÓ"), true},
		{"pdf with a wrong extension", "bail.exe", "application/pdf", ".pdf", []byte("%PDF-1.4\n"), true},
		{"jpeg", "photo.jpeg", "image/jpeg", ".jpg", []byte("\xff\xd8\xff\xe0\x00\x10JFIF"), true},
		{"png", "photo.png", "image/png", ".png", png, true},
		{"csv", "releve.CSV", "text/csv", ".csv", []byte("date;montant\n2025-01-05;850,00\n"), true},
		{"ofx", "releve.ofx", "application/x-ofx", ".ofx", []byte("OFXHEADER:100\nDATA:OFXSGML\n"), true},
		{"text under another extension", "notes.txt", "", "", []byte("hello"), false},
		{"declared pdf that is html", "bail.pdf", "", "", []byte("<html><script>alert(1)</script>"), false},
		{"executable", "setup.pdf", "", "", []byte("MZ\x90\x00\x03\x00\x00\x00"), false},
		{"zip", "archive.csv", "", "", []byte("PK\x03\x04\x14\x00"), false},
	}
	for _, c := range cases {
		gotType, gotExt, ok := sniff(c.head, c.file)
		if ok != c.ok || gotType != c.wantType || gotExt != c.wantExt {
			t.Errorf("%s: got %q %q %v", c.name, gotType, gotExt, ok)
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"bail signé.pdf":           "bail signé.pdf",
		"../../etc/passwd":         "passwd",
		`C:\Users\x\scan.pdf`:      "scan.pdf",
		"a\x00b\nc.pdf":            "abc.pdf",
		"":                         "",
		strings.Repeat("é", 150):   strings.Repeat("é", 100),
		"/":                        "",
		"dir/":                     "dir",
		"évidemment " + "\x7f.pdf": "évidemment .pdf",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestParseClamd(t *testing.T) {
	v, err := parseClamd("stream: OK")
	if err != nil || !v.Clean {
		t.Errorf("OK: %+v %v", v, err)
	}
	v, err = parseClamd("stream: Win.Test.EICAR_HDB-1 FOUND")
	if err != nil || v.Clean || v.Signature != "Win.Test.EICAR_HDB-1" {
		t.Errorf("FOUND: %+v %v", v, err)
	}
	for _, bad := range []string{"INSTREAM size limit exceeded. ERROR", "", "stream: lstat() failed. ERROR"} {
		if _, err := parseClamd(bad); !errors.Is(err, ErrScanner) {
			t.Errorf("%q: %v; want ErrScanner", bad, err)
		}
	}
}

// fakeClamd answers zINSTREAM like clamd: FOUND when the stream holds the
// marker, OK otherwise; zPING with PONG.
func fakeClamd(t *testing.T, marker string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				r := bufio.NewReader(c)
				cmd, _ := r.ReadString(0)
				switch cmd {
				case "zPING\x00":
					_, _ = c.Write([]byte("PONG\x00"))
				case "zINSTREAM\x00":
					var got bytes.Buffer
					for {
						var size [4]byte
						if _, err := io.ReadFull(r, size[:]); err != nil {
							return
						}
						n := binary.BigEndian.Uint32(size[:])
						if n == 0 {
							break
						}
						if _, err := io.CopyN(&got, r, int64(n)); err != nil {
							return
						}
					}
					if bytes.Contains(got.Bytes(), []byte(marker)) {
						_, _ = c.Write([]byte("stream: Fake.Marker FOUND\x00"))
						return
					}
					_, _ = c.Write([]byte("stream: OK\x00"))
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestClamdScan(t *testing.T) {
	c := Clamd{Addr: fakeClamd(t, "MARKER")}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), 200<<10)
	v, err := c.Scan(ctx, bytes.NewReader(big))
	if err != nil || !v.Clean {
		t.Fatalf("clean file: %+v %v", v, err)
	}
	v, err = c.Scan(ctx, bytes.NewReader(append(big, []byte("MARKER")...)))
	if err != nil || v.Clean || v.Signature != "Fake.Marker" {
		t.Fatalf("infected file: %+v %v", v, err)
	}
	down := Clamd{Addr: "127.0.0.1:1"}
	if _, err := down.Scan(ctx, bytes.NewReader(big)); !errors.Is(err, ErrScanner) {
		t.Errorf("no daemon: %v; want ErrScanner (fail closed)", err)
	}
}
