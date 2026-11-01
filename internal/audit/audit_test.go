package audit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCanonicalIsStableAcrossJSONRenderings(t *testing.T) {
	owner := uuid.MustParse("01900000-0000-7000-8000-000000000001")
	at := time.Date(2025, 3, 15, 10, 0, 0, 123456000, time.UTC)
	a := Event{OwnerID: &owner, ActorSub: "42", Scope: "owner", Action: "asset.create", EntityType: "asset", EntityID: "x",
		After: json.RawMessage(`{"b":1,"a":"<é>","n":12345678901234567}`), OccurredAt: at, PrevHash: Genesis}
	b := a
	// jsonb returns the same document with its own key order and spacing.
	b.After = json.RawMessage(`{"a": "<é>", "n": 12345678901234567, "b": 1}`)
	ha, err := Hash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := Hash(b)
	if ha != hb || len(ha) != 64 {
		t.Errorf("hash depends on rendering: %s vs %s", ha, hb)
	}
	c := a
	c.After = json.RawMessage(`{"b":2,"a":"<é>","n":12345678901234567}`)
	if hc, _ := Hash(c); hc == ha {
		t.Error("a changed value kept the hash")
	}
	d := a
	d.PrevHash = "1" + Genesis[1:]
	if hd, _ := Hash(d); hd == ha {
		t.Error("a changed link kept the hash")
	}
	canon, _ := Canonical(a)
	if string(canon[:12]) != `{"action":"a` {
		t.Errorf("keys not sorted: %s", canon)
	}
}
