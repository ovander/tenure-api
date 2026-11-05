package esign_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/esign"
	"github.com/ovander/tenure-api/internal/esign/esigntest"
)

func pdf() []byte {
	return []byte("%PDF-1.7\n{{s1|signature|180|60}} {{s2|signature|180|60}}\n%%EOF\n")
}

func TestYousignRequestLifecycle(t *testing.T) {
	fake := esigntest.New("test-key")
	defer fake.Close()
	y := esign.Yousign{BaseURL: fake.URL + "/v3", APIKey: "test-key"}
	ctx := context.Background()
	started, err := y.Start(ctx, esign.Request{Name: "État des lieux d’entrée", ExternalID: "inv-1", FileName: "edl.pdf", PDF: pdf(),
		Signers: []esign.Signer{{Key: "s1", FirstName: "Bailleur", LastName: "A", Email: "owner@example.test"},
			{Key: "s2", FirstName: "Léa", LastName: "Martin", Email: "lea@example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Signers) != 2 || started.Signers[0].Key != "s1" || started.Signers[1].Link == "" {
		t.Fatalf("started %+v", started)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || reqs[0].DeliveryMode != "none" || !reqs[0].ParseAnchors || !bytes.Equal(reqs[0].Document, pdf()) || reqs[0].ExternalID != "inv-1" {
		t.Fatalf("at the provider %+v", reqs)
	}
	st, err := y.Status(ctx, started.RequestID)
	if err != nil || st.State != esign.StateOngoing || st.Signers[0].Signed {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := y.Download(ctx, started.RequestID); !errors.Is(err, esign.ErrProvider) {
		t.Errorf("download before done: %v", err)
	}
	fake.Sign(started.Signers[0].SignerID)
	st, _ = y.Status(ctx, started.RequestID)
	if st.State != esign.StateOngoing || !st.Signers[0].Signed || st.Signers[1].Signed {
		t.Errorf("one signed %+v", st)
	}
	fake.Sign(started.Signers[1].SignerID)
	if st, _ = y.Status(ctx, started.RequestID); st.State != esign.StateDone {
		t.Errorf("all signed %+v", st)
	}
	signed, err := y.Download(ctx, started.RequestID)
	if err != nil || !bytes.Contains(signed, []byte("signed by the fake provider")) {
		t.Errorf("download %q %v", signed, err)
	}
}

func TestYousignFailuresAndRefusals(t *testing.T) {
	fake := esigntest.New("test-key")
	defer fake.Close()
	ctx := context.Background()
	req := esign.Request{Name: "x", FileName: "x.pdf", PDF: pdf(), Signers: []esign.Signer{{Key: "s1", FirstName: "A", LastName: "B", Email: "a@example.test"}}}
	if _, err := (esign.Yousign{BaseURL: fake.URL + "/v3", APIKey: "wrong"}).Start(ctx, req); !errors.Is(err, esign.ErrProvider) {
		t.Errorf("wrong key: %v", err)
	}
	y := esign.Yousign{BaseURL: fake.URL + "/v3", APIKey: "test-key"}
	// A failure after creation cancels the draft at the provider.
	fake.Fail = "/signers"
	if _, err := y.Start(ctx, req); err == nil {
		t.Fatal("a failed signer must fail the start")
	}
	if r := fake.Requests(); len(r) != 1 || r[0].Status != "canceled" {
		t.Errorf("the draft left behind: %+v", r)
	}
	started, err := y.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	fake.Decline(started.RequestID)
	if st, _ := y.Status(ctx, started.RequestID); st.State != esign.StateRefused {
		t.Errorf("declined %+v", st)
	}
	if _, err := y.Start(ctx, esign.Request{Name: "x"}); err == nil {
		t.Error("a request without signers")
	}
}
