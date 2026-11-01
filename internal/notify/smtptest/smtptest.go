// Package smtptest is an in-process SMTP server that records the messages it
// receives, for tests. It offers no TLS and accepts any sender.
package smtptest

import (
	"bufio"
	"net"
	"strings"
	"sync"
)

// Mail is a received message.
type Mail struct {
	From string
	To   []string
	Data string
}

// Server records messages.
type Server struct {
	Addr string
	ln   net.Listener
	mu   sync.Mutex
	mail []Mail
	// FailRcpt makes RCPT fail for this address, to test delivery errors.
	FailRcpt string
}

// Start listens on a loopback port.
func Start() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{Addr: ln.Addr().String(), ln: ln}
	go s.serve()
	return s, nil
}

// Close stops the server.
func (s *Server) Close() error { return s.ln.Close() }

// Messages returns the received messages.
func (s *Server) Messages() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mail...)
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	w := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	w("220 smtptest ready")
	var m Mail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250-smtptest")
			w("250 8BITMIME")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m = Mail{From: strings.TrimSpace(line[10:])}
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			to := strings.Trim(strings.TrimSpace(line[8:]), "<>")
			if s.FailRcpt != "" && to == s.FailRcpt {
				w("550 no such user")
				continue
			}
			m.To = append(m.To, to)
			w("250 ok")
		case cmd == "DATA":
			w("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			m.Data = b.String()
			s.mu.Lock()
			s.mail = append(s.mail, m)
			s.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		case cmd == "RSET", cmd == "NOOP":
			w("250 ok")
		default:
			w("502 not implemented")
		}
	}
}
