package inventory

import (
	"html/template"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
)

var states = []string{StateDraft, StateScheduled, StateInProgress, StateCompleted, StateSigned, StateIssued}

// Every state × every action: an action leaves exactly one state, and what
// each state lets the owner edit follows RENT-INV-001 §5.
func TestLifecycleEveryStateAndAction(t *testing.T) {
	want := map[string]string{ActionSchedule: StateDraft, ActionStart: StateScheduled, ActionComplete: StateInProgress,
		ActionReopen: StateCompleted, ActionIssue: StateSigned}
	next := map[string]string{ActionSchedule: StateScheduled, ActionStart: StateInProgress, ActionComplete: StateCompleted,
		ActionReopen: StateInProgress, ActionIssue: StateIssued}
	for _, st := range states {
		for _, a := range []string{ActionSchedule, ActionStart, ActionComplete, ActionReopen, ActionIssue} {
			move := transitions[a]
			if got := move[0] == st; got != (want[a] == st) {
				t.Errorf("%s from %s: allowed %v", a, st, got)
			}
			if move[1] != next[a] {
				t.Errorf("%s enters %s, want %s", a, move[1], next[a])
			}
		}
		header := st == StateDraft || st == StateScheduled || st == StateInProgress
		items := st == StateDraft || st == StateInProgress
		if headerEditable(st) != header || itemsEditable(st) != items {
			t.Errorf("%s: header %v items %v", st, headerEditable(st), itemsEditable(st))
		}
	}
	if len(transitions) != 5 {
		t.Errorf("%d actions", len(transitions))
	}
	if _, ok := transitions["sign"]; ok {
		t.Error("sign comes with the signatures (slice 6)")
	}
}

func str(s string) *string { return &s }

func sampleCapture() capture {
	done := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	room := uuid.Must(uuid.NewV7())
	inv := uuid.Must(uuid.NewV7())
	item, sink, meter := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	photo := uuid.Must(uuid.NewV7())
	return capture{
		head: captureRow{ID: inv, Kind: "entry", Mode: "amicable", State: StateSigned, DoneOn: &done, Observations: "Peinture refaite en 2025."},
		items: []itemRow{
			{ID: item, RoomID: &room, RoomLabel: "Séjour", RoomKind: str("living"), Position: 1, Category: "floor", Label: "Parquet",
				Condition: str("good"), Comment: "Rayure <près> de la fenêtre"},
			{ID: sink, RoomLabel: "Jardin", RoomKind: str(OutsideKind), Position: 2, Category: "other", Label: "Portail",
				Condition: str("fair"), Works: str("ok")},
		},
		meters: []meterRow{{ID: meter, Kind: "water_cold", MeterNumber: "A12", Reading: "123,45", Unit: "m³"}},
		keys:   []keyRow{{Kind: "key", Destination: "porte d’entrée", Quantity: 2}},
		photos: []photoRow{{ID: photo, ItemID: &item, SHA256: strings.Repeat("a", 64), Width: 2048, Height: 1536}},
		sigs: []signatureRow{{Party: "owner", SignerName: "Bailleur A", Method: "wet_scan", SignedAt: done},
			{Party: "tenant", SignerName: "Léa Martin", Method: "esign", SignedAt: done, Reservations: "Tache au plafond non relevée"}},
		facts: lease.InventoryFacts{Kind: lease.KindUnfurnished, Owner: document.Party{Name: "Bailleur A", Address: "1 rue A"},
			Tenants: []string{"Léa Martin"}, Assets: []lease.InventoryAsset{{Label: "2 rue B, Lyon"}}},
	}
}

func TestEntryDocumentRenders(t *testing.T) {
	c := sampleCapture()
	thumb := template.URL("data:image/jpeg;base64,AAAA")
	d := entryData(c, map[uuid.UUID]template.URL{c.photos[0].ID: thumb})
	d.Number, d.IssuedOn, d.CompletionDays, d.HeatingMonths = "EE-X-2026-0001", period.NewDate(2026, 3, 2), 7, 2
	html, _, err := document.Renderer{}.Render(document.ClassInventory, EntryTemplate, d, document.Footer{Number: d.Number, SnapshotHash: "abcd"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"État des lieux d’entrée", "EE-X-2026-0001", "01/03/2026", "Bailleur A", "Léa Martin", "2 rue B, Lyon",
		"Séjour", "Parquet", "Bon état", "Jardin", "État d’usage", "fonctionne", "Eau froide", "123,45 m³", "porte d’entrée",
		"Peinture refaite", "Tache au plafond non relevée", "signature manuscrite", "7 jours", "les 2 premiers mois",
		`src="data:image/jpeg;base64,AAAA"`, strings.Repeat("a", 64), "Rayure &lt;près&gt;"} {
		if !strings.Contains(s, want) {
			t.Errorf("the EE document lacks %q", want)
		}
	}
	if strings.Contains(s, "<no value>") || strings.Contains(s, "%!") || strings.Contains(s, "#ZgotmplZ") {
		t.Errorf("unfilled or unsafe value in the document")
	}
	// The snapshot carries no thumbnail: the photos are fingerprinted by hash.
	if snap := entryData(c, nil); len(snap.Rooms[0].Items[0].Thumbs) != 0 || snap.Photos[0].SHA256 == "" {
		t.Error("snapshot with thumbnails, or without the photo hash")
	}
}

func TestEngineView(t *testing.T) {
	c := sampleCapture()
	inv := c.engine()
	if len(inv.Rooms) != 2 || inv.Rooms[1].Kind != OutsideKind || inv.Items[0].Photos != 1 {
		t.Fatalf("engine %+v", inv)
	}
	if len(inv.Parties) != 2 || !inv.Parties[0].Signed || !inv.Parties[1].Signed {
		t.Errorf("parties %+v", inv.Parties)
	}
	// The living room lacks its walls and ceiling; the garden needs none.
	checks := Validate(inv)
	n := 0
	for _, x := range checks.Blocking {
		if x.Code == CodeCoveringMissing {
			n++
			if x.Room != "Séjour" {
				t.Errorf("covering check in %s", x.Room)
			}
		}
	}
	if n != 2 || !checks.Blocks(StageComplete) || codes(checks, StageComplete) == "" {
		t.Errorf("checks %+v", checks)
	}
	v := c.view()
	if len(v.Rooms) != 2 || len(v.Rooms[0].Items[0].Photos) != 1 || len(v.Meters) != 1 || v.Meters[0].Photos == nil {
		t.Errorf("view %+v", v)
	}
	c.sigs = c.sigs[:1]
	if p := c.parties(); p[1].Signed {
		t.Error("the tenant has not signed")
	}
}

func TestShrink(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 1000; x++ {
			src.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	out := shrink(src, thumbSide)
	if b := out.Bounds(); b.Dx() != thumbSide || b.Dy() != 128 {
		t.Errorf("thumb %v", b)
	}
	if r, g, b, _ := out.At(10, 10).RGBA(); r>>8 != 200 || g>>8 != 100 || b>>8 != 50 {
		t.Errorf("averaged colour %d %d %d", r>>8, g>>8, b>>8)
	}
	small := image.NewRGBA(image.Rect(0, 0, 10, 20))
	if shrink(small, thumbSide) != image.Image(small) {
		t.Error("a small image is kept as is")
	}
}
