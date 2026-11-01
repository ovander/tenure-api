package notify

import (
	"context"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/notify/smtptest"
)

func TestSendToLoopback(t *testing.T) {
	srv, err := smtptest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	m := SMTP{Addr: srv.Addr, From: "no-reply@example.test"}
	msg := Compose(DocumentMail{Kind: "rent_call", Number: "AL-X-2025-0001", Period: "mars 2025", Owner: "Propriétaire A", Language: "fr"},
		"jeanne@example.test", []byte("%PDF-1.7 test"))
	if err := m.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].To[0] != "jeanne@example.test" || !strings.Contains(got[0].Data, `filename="AL-X-2025-0001.pdf"`) ||
		!strings.Contains(got[0].Data, "=?utf-8?q?Avis_d=E2=80=99=C3=A9ch=C3=A9ance") {
		t.Fatalf("%+v", got)
	}
	en := Compose(DocumentMail{Kind: "rent_call", Number: "N", Period: "March 2025", Owner: "O", Language: "en"}, "a@b.test", nil)
	if !strings.HasPrefix(en.Subject, "Rent call") || !strings.Contains(en.Text, "written in French") {
		t.Errorf("English mail: %+v", en)
	}
	srv.FailRcpt = "bounce@example.test"
	if err := m.Send(context.Background(), Compose(DocumentMail{Kind: "rent_call"}, "bounce@example.test", nil)); err == nil {
		t.Error("refused recipient not reported")
	}
	if err := m.Send(context.Background(), Message{To: "a@b.test\r\nBcc: x@y"}); err == nil {
		t.Error("header injection accepted")
	}
}

// Only a loopback host may skip STARTTLS; any other host without it is refused.
func TestOnlyLoopbackSkipsTLS(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true,
		"smtp.example.test": false, "10.0.0.5": false, "localhost.example.test": false} {
		if loopback(host) != want {
			t.Errorf("loopback(%q) = %v", host, !want)
		}
	}
}
