// Package notify sends transactional e-mail (RENT-SPEC-006, RENT-BE-001 §6):
// in the recipient's language (French by default, English when set on the
// owner or tenant), with the document attached. The document itself is always
// French.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Attachment is a file attached to a message.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

// Message is an e-mail.
type Message struct {
	To          string
	Subject     string
	Text        string
	Attachments []Attachment
}

// Mailer sends messages.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends through an SMTP server. STARTTLS is required unless the host is
// a loopback address (local development).
type SMTP struct {
	Addr     string
	Username string
	Password string
	From     string
	// Now stamps the Date header; tests replace it.
	Now func() time.Time
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send implements Mailer.
func (s SMTP) Send(ctx context.Context, m Message) error {
	if !strings.Contains(m.To, "@") || strings.ContainsAny(m.To, "\r\n") {
		return fmt.Errorf("notify: invalid recipient")
	}
	body, err := s.build(m)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("notify: dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("notify: smtp: %w", err)
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("notify: starttls: %w", err)
		}
	} else if !loopback(host) {
		return fmt.Errorf("notify: %s does not offer STARTTLS", host)
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return fmt.Errorf("notify: auth: %w", err)
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("notify: mail from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("notify: rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: data: %w", err)
	}
	return c.Quit()
}

func (s SMTP) build(m Message) ([]byte, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	boundary := fmt.Sprintf("tenure-%d", now().UnixNano())
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", s.From)
	h("To", m.To)
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", now().UTC().Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	h("Content-Type", fmt.Sprintf(`multipart/mixed; boundary="%s"`, boundary))
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary)
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(m.Text)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")
	for _, a := range m.Attachments {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"%s\"\r\n\r\n",
			boundary, a.ContentType, strings.ReplaceAll(a.Name, `"`, ""))
		enc := base64.StdEncoding.EncodeToString(a.Data)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

// Language returns "en" for English, "fr" otherwise.
func Language(l string) string {
	if l == "en" {
		return "en"
	}
	return "fr"
}

// DocumentMail is the e-mail that carries an issued document.
type DocumentMail struct {
	Kind     string // rent_call, revision_notice, cancellation
	Number   string
	Period   string // already formatted
	Owner    string
	Language string
}

var subjects = map[string]map[string]string{
	"rent_call":       {"fr": "Avis d’échéance %s — %s", "en": "Rent call %s — %s"},
	"revision_notice": {"fr": "Révision annuelle du loyer — %[2]s", "en": "Annual rent revision — %[2]s"},
	"cancellation":    {"fr": "Annulation d’un document — %[2]s", "en": "Cancelled document — %[2]s"},
}

var bodies = map[string]string{
	"fr": "Bonjour,\n\nVeuillez trouver ci-joint le document n° %s émis par %s.\n\nLe document joint fait foi ; il est rédigé en français.\n\nCordialement,\n%s\n",
	"en": "Hello,\n\nPlease find attached document no. %s issued by %s.\n\nThe attached document is authoritative; it is written in French.\n\nKind regards,\n%s\n",
}

// Compose returns the message of an issued document for recipient.
func Compose(d DocumentMail, to string, pdf []byte) Message {
	lang := Language(d.Language)
	subj := subjects[d.Kind]
	if subj == nil {
		subj = subjects["rent_call"]
	}
	return Message{To: to, Subject: fmt.Sprintf(subj[lang], d.Period, d.Number),
		Text:        fmt.Sprintf(bodies[lang], d.Number, d.Owner, d.Owner),
		Attachments: []Attachment{{Name: d.Number + ".pdf", ContentType: "application/pdf", Data: pdf}}}
}
