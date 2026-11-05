//go:build integration

package itest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"github.com/ovander/tenure-api/internal/auth"
)

const (
	subR  = "9701" // owner of the photo scenario (RENT-INV-001 slice 4)
	subS  = "9702" // another owner
	subRA = "9703" // R's accountant
	subRC = "9704" // R's co-owner
)

// exifJPEG is a 4×2 camera-like JPEG carrying an Exif segment: orientation 6,
// a GPS pointer and a maker string, none of which may reach the archive.
func exifJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: uint8(60 * x), G: 90, B: 200, A: 255})
		}
	}
	var j bytes.Buffer
	if err := jpeg.Encode(&j, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	var tiff bytes.Buffer
	tiff.WriteString("II*\x00")
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(8))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(2))
	_ = binary.Write(&tiff, binary.LittleEndian, []uint16{0x0112, 3})
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, []uint16{6, 0})
	_ = binary.Write(&tiff, binary.LittleEndian, []uint16{0x8825, 4})
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0))
	tiff.WriteString("GPS 45.7640N 4.8357E SECRET-MAKER")
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	src := j.Bytes()
	out := append([]byte{}, src[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, src[2:]...)
}

// postPhoto uploads a photo, the target fields first and the file last.
func postPhoto(t *testing.T, tok, scope, inventory string, fields map[string]string, content []byte) resp {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, k := range []string{"item_id", "meter_id"} {
		if v, ok := fields[k]; ok {
			_ = mw.WriteField(k, v)
		}
	}
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	_, _ = fw.Write(content)
	_ = mw.Close()
	r, _ := http.NewRequest("POST", env.server.URL+"/api/v1/inventories/"+inventory+"/photos", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+tok)
	if scope != "" {
		r.Header.Set(auth.ScopeHeader, scope)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Header: res.Header, Body: out}
}

type photoView struct {
	ID          string `json:"id"`
	InventoryID string `json:"inventory_id"`
	ItemID      string `json:"item_id"`
	MeterID     string `json:"meter_id"`
	SHA256      string `json:"sha256"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// TestInventoryPhotos covers the photo pipeline (RENT-INV-001 §3, §7, slice
// 4): a camera JPEG is archived upright with no metadata and the hash of the
// stored object; the audit event holds both hashes; the link is signed and
// audited; an infected file, a file that is not an image, a wrong target and
// an issued inventory are refused; the accountant and another owner see
// nothing.
func TestInventoryPhotos(t *testing.T) {
	tok, tokS := token(t, subR), token(t, subS)
	var owner, ownerS, apt, ten, l1 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur R", "address": "7 rue R, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokS, body: map[string]any{"legal_name": "Bailleur S", "address": "1 rue S"}}).json(t, &ownerS)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "7 rue R, Lyon"}}).json(t, &apt)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}}}).json(t, &ten)
	call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{apt.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l1)
	if l1.ID == "" {
		t.Fatal("lease not created")
	}
	readyToActivate(t, tok, l1.ID)
	var invs []struct {
		ID string `json:"id"`
	}
	call(t, "GET", "/leases/"+l1.ID+"/inventories", req{tok: tok}).json(t, &invs)
	if len(invs) != 1 {
		t.Fatalf("inventories: %d", len(invs))
	}
	entry := invs[0].ID // an issued upload

	// A draft exit with one item and one meter (the capture routes come with slice 5).
	room, element, draft, item, meter := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()),
		uuid.Must(uuid.NewV7())
	if err := asAPICommit(t, owner.ID, subR, func(tx *sql.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO asset_rooms (id, owner_id, asset_id, position, kind, label, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, 1, 'living', 'Séjour', now(), $4, now(), $4)`, []any{room, owner.ID, apt.ID, subR}},
			{`INSERT INTO asset_elements (id, owner_id, room_id, position, category, label, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, 1, 'floor', 'Sol', now(), $4, now(), $4)`, []any{element, owner.ID, room, subR}},
			{`INSERT INTO inventories (id, owner_id, lease_id, kind, mode, state, entry_inventory_id, legacy_items, created_at, created_by)
				VALUES ($1, $2, $3, 'exit', 'amicable', 'draft', $4, '[]', now(), $5)`, []any{draft, owner.ID, l1.ID, entry, subR}},
			{`INSERT INTO inventory_items (id, owner_id, inventory_id, room_id, element_id, room_label, position, category, label, condition,
				created_at, created_by, updated_at, updated_by) VALUES ($1, $2, $3, $4, $5, 'Séjour', 1, 'floor', 'Sol', 'good', now(), $6, now(), $6)`,
				[]any{item, owner.ID, draft, room, element, subR}},
			{`INSERT INTO inventory_meters (id, owner_id, inventory_id, kind, reading, created_at, created_by)
				VALUES ($1, $2, $3, 'water_cold', '123,45', now(), $4)`, []any{meter, owner.ID, draft, subR}},
		} {
			if _, err := tx.Exec(q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	inv := draft.String()
	src := exifJPEG(t)
	srcSum := sha256.Sum256(src)

	var ph photoView
	t.Run("a camera photo is archived upright without metadata", func(t *testing.T) {
		audited(t, seen, "POST /inventories/{id}/photos", 201, func() resp {
			return postPhoto(t, tok, "", inv, map[string]string{"item_id": item.String()}, src)
		}).json(t, &ph)
		if ph.ItemID != item.String() || ph.Width != 2 || ph.Height != 4 {
			t.Fatalf("photo %+v: want the item, 2×4 after orientation 6", ph)
		}
		var key, version, original string
		if err := env.super.QueryRow(`SELECT object_key, object_version, original_sha256 FROM inventory_photos WHERE id = $1`, ph.ID).
			Scan(&key, &version, &original); err != nil {
			t.Fatal(err)
		}
		if want := owner.ID + "/inventory/" + l1.ID + "/" + inv + "/" + ph.ID + ".jpg"; key != want {
			t.Errorf("object key %s, want %s", key, want)
		}
		if original != hex.EncodeToString(srcSum[:]) {
			t.Error("original_sha256 is not the upload's hash")
		}
		obj, err := env.s3.GetObject(context.Background(), bucket, key, minio.GetObjectOptions{VersionID: version})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := io.ReadAll(obj)
		_ = obj.Close()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(stored)
		if hex.EncodeToString(sum[:]) != ph.SHA256 {
			t.Error("sha256 does not match the stored object")
		}
		for _, leak := range []string{"Exif", "GPS", "SECRET-MAKER"} {
			if bytes.Contains(stored, []byte(leak)) {
				t.Errorf("the archived photo still holds %q", leak)
			}
		}
		if _, err := jpeg.Decode(bytes.NewReader(stored)); err != nil {
			t.Errorf("the archived photo is not a JPEG: %v", err)
		}
		mode, until, err := env.s3.GetObjectRetention(context.Background(), bucket, key, version)
		if err != nil || mode == nil || *mode != minio.Compliance || until == nil || until.Before(time.Now().AddDate(1, 0, 0)) {
			t.Errorf("retention %v %v %v", mode, until, err)
		}
		var after json.RawMessage
		if err := env.super.QueryRow(`SELECT after FROM audit_events WHERE action = 'inventory.photo.add' AND entity_id = $1`, inv).
			Scan(&after); err != nil {
			t.Fatal(err)
		}
		var ev struct {
			Original string    `json:"original_sha256"`
			Photo    photoView `json:"photo"`
		}
		_ = json.Unmarshal(after, &ev)
		if ev.Original != original || ev.Photo.SHA256 != ph.SHA256 {
			t.Errorf("the audit event lacks a hash: %s", after)
		}
		if quarantined(t) != 0 {
			t.Error("the quarantined upload was kept")
		}
	})

	t.Run("a meter photo", func(t *testing.T) {
		var m photoView
		audited(t, seen, "POST /inventories/{id}/photos", 201, func() resp {
			return postPhoto(t, tok, "", inv, map[string]string{"meter_id": meter.String()}, src)
		}).json(t, &m)
		if m.MeterID != meter.String() || m.ItemID != "" {
			t.Errorf("meter photo %+v", m)
		}
	})

	t.Run("signed link, audited", func(t *testing.T) {
		before := superCount(t, `SELECT count(*) FROM audit_events WHERE action = 'inventory.photo.view'`)
		var l struct {
			URL       string    `json:"url"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		r := call(t, "GET", "/inventories/"+inv+"/photos/"+ph.ID+"/content", req{tok: tok})
		if r.Status != 200 || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("content: %d %s", r.Status, r.Body)
		}
		r.json(t, &l)
		if time.Until(l.ExpiresAt) > 11*time.Minute || l.ExpiresAt.Before(time.Now()) {
			t.Errorf("link expires at %s", l.ExpiresAt)
		}
		res, err := http.Get(l.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		sum := sha256.Sum256(body)
		if res.StatusCode != 200 || hex.EncodeToString(sum[:]) != ph.SHA256 || res.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("signed link: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
		}
		if superCount(t, `SELECT count(*) FROM audit_events WHERE action = 'inventory.photo.view'`) != before+1 {
			t.Error("the link was not audited")
		}
		if r := call(t, "GET", "/inventories/"+entry+"/photos/"+ph.ID+"/content", req{tok: tok}); r.Status != 404 {
			t.Errorf("a photo through another inventory: %d", r.Status)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		photos := func() int { return superCount(t, `SELECT count(*) FROM inventory_photos WHERE inventory_id = $1`, inv) }
		n := photos()
		rejects := superCount(t, `SELECT count(*) FROM audit_events WHERE action = 'inventory.photo.reject'`)
		if r := postPhoto(t, tok, "", inv, map[string]string{"item_id": item.String()}, eicar()); r.problemType(t) != "document/infected" {
			t.Errorf("EICAR: %d %s", r.Status, r.Body)
		}
		if superCount(t, `SELECT count(*) FROM audit_events WHERE action = 'inventory.photo.reject'`) != rejects+1 {
			t.Error("the rejected upload left no audit event")
		}
		for name, c := range map[string]struct {
			inventory string
			fields    map[string]string
			body      []byte
			problem   string
		}{
			"a PDF":                  {inv, map[string]string{"item_id": item.String()}, []byte("%PDF-1.7\n%%EOF\n"), "inventory/photo-not-image"},
			"no target":              {inv, map[string]string{}, src, "inventory/photo-target-invalid"},
			"both targets":           {inv, map[string]string{"item_id": item.String(), "meter_id": meter.String()}, src, "inventory/photo-target-invalid"},
			"an item of another one": {inv, map[string]string{"item_id": uuid.Must(uuid.NewV7()).String()}, src, "inventory/photo-target-invalid"},
			"an issued inventory":    {entry, map[string]string{"item_id": item.String()}, src, "inventory/issued-immutable"},
		} {
			before := superCount(t, "SELECT count(*) FROM audit_events")
			if r := postPhoto(t, tok, "", c.inventory, c.fields, c.body); r.problemType(t) != c.problem {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
			}
			if superCount(t, "SELECT count(*) FROM audit_events") != before {
				t.Errorf("%s: a refused upload left an audit event", name)
			}
		}
		if photos() != n || quarantined(t) != 0 {
			t.Errorf("refused uploads left %d photos, %d quarantined", photos()-n, quarantined(t))
		}
	})

	t.Run("roles and isolation", func(t *testing.T) {
		now := time.Now().UTC()
		for sub, role := range map[string]string{subRA: "accountant", subRC: "co_owner"} {
			superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, 'itest', $5, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, sub, role, now)
		}
		acc, co := token(t, subRA), token(t, subRC)
		content := "/inventories/" + inv + "/photos/" + ph.ID + "/content"
		if r := call(t, "GET", content, req{tok: acc, scope: owner.ID}); r.Status != 403 {
			t.Errorf("accountant reads a photo: %d", r.Status)
		}
		if r := postPhoto(t, acc, owner.ID, inv, map[string]string{"item_id": item.String()}, src); r.Status != 403 {
			t.Errorf("accountant uploads: %d", r.Status)
		}
		if r := call(t, "GET", content, req{tok: co, scope: owner.ID}); r.Status != 200 {
			t.Errorf("co-owner cannot see a photo: %d", r.Status)
		}
		if r := postPhoto(t, co, owner.ID, inv, map[string]string{"item_id": item.String()}, src); r.Status != 403 {
			t.Errorf("co-owner uploads: %d", r.Status)
		}
		if r := call(t, "GET", content, req{tok: tokS}); r.Status != 404 {
			t.Errorf("S reads R's photo: %d", r.Status)
		}
		if r := postPhoto(t, tokS, "", inv, map[string]string{"item_id": item.String()}, src); r.Status != 404 {
			t.Errorf("S uploads to R's inventory: %d %s", r.Status, r.Body)
		}
		rlsZeroRows(t, owner.ID, ownerS.ID, subR, subS, apt.ID, map[string]string{"inventory_photos": "owner_id = $1"})
	})
}
