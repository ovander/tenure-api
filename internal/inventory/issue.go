package inventory

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/draw"
	"image/jpeg"
	"io"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// EntryTemplate is the version of templates/inventory the entry inventory
// (EE) renders with; a template change adds a version (tenure-api#84).
const EntryTemplate = "v1"

// thumbSide is the long side of a photo thumbnail embedded in the PDF; the
// full photo stays in the archive, listed by hash in the annex.
const thumbSide = 320

// French labels of the document (generated documents are French only).
var (
	conditionFR = map[Condition]string{New: "Neuf", Good: "Bon état", Fair: "État d’usage", Poor: "Mauvais état", Broken: "Hors d’usage"}
	worksFR     = map[Works]string{WorksOK: "fonctionne", WorksKO: "ne fonctionne pas", WorksNA: "sans objet"}
	meterFR     = map[string]string{"elec_peak": "Électricité, heures pleines", "elec_offpeak": "Électricité, heures creuses",
		"elec_base": "Électricité", "gas": "Gaz", "water_cold": "Eau froide", "water_hot": "Eau chaude", "heating_energy": "Énergie de chauffage"}
	keyFR    = map[string]string{"key": "Clé", "badge": "Badge", "remote": "Télécommande", "card": "Carte", "code": "Code"}
	partyFR  = map[string]string{"owner": "Bailleur", "tenant": "Locataire", "representative": "Mandataire"}
	methodFR = map[string]string{"esign": "signature électronique", "wet_scan": "signature manuscrite (copie numérisée)"}
)

// EntryItem is an item line of the EE document.
type EntryItem struct {
	Label     string
	Condition string
	Works     string
	Quantity  int
	Comment   string
	Photos    []int
	Thumbs    []template.URL
}

// EntryRoom is a room of the EE document.
type EntryRoom struct {
	Label string
	Items []EntryItem
}

// EntryLine is a meter or key line of the EE document.
type EntryLine struct {
	Kind, Detail, Value, Comment string
	Photos                       []int
}

// EntrySignature is a signature of the EE document.
type EntrySignature struct {
	Party, Name, Method string
	SignedOn            period.Date
	Reservations        string
}

// EntryPhoto is a line of the photo annex: the archived photo by hash.
type EntryPhoto struct {
	N             int
	Of            string
	SHA256        string
	Width, Height int
}

// EntryData is the data of templates/inventory/v1: the mentions of décret
// 2016-382 art. 2 (type, date, premises, parties and their domicile, meter
// readings, keys, the state of each room and equipment, signatures).
type EntryData struct {
	Number       string
	IssuedOn     period.Date
	DoneOn       period.Date
	Owner        document.Party
	Tenants      []string
	Premises     []string
	Furnished    bool
	Rooms        []EntryRoom
	Meters       []EntryLine
	Keys         []EntryLine
	Observations string
	Signatures   []EntrySignature
	Photos       []EntryPhoto
	// The tenant's windows to ask for a completion (loi 89-462 art. 3-2),
	// from legal_params as of the inventory's date.
	CompletionDays int64
	HeatingMonths  int64
}

// entryData builds the document's content from a capture; thumbs holds the
// thumbnail of each photo id, absent from the snapshot.
func entryData(c capture, thumbs map[uuid.UUID]template.URL) EntryData {
	d := EntryData{Owner: c.facts.Owner, Tenants: c.facts.Tenants, Furnished: c.facts.Kind == "furnished",
		Observations: c.head.Observations, Rooms: []EntryRoom{}, Meters: []EntryLine{}, Keys: []EntryLine{},
		Signatures: []EntrySignature{}, Photos: []EntryPhoto{}}
	if c.head.DoneOn != nil {
		d.DoneOn = period.FromTime(*c.head.DoneOn)
	}
	for _, a := range c.facts.Assets {
		d.Premises = append(d.Premises, a.Label)
	}
	byItem, byMeter := map[uuid.UUID][]int{}, map[uuid.UUID][]int{}
	labels := map[uuid.UUID]string{}
	for _, it := range c.items {
		labels[it.ID] = it.RoomLabel + " · " + it.Label
	}
	for _, m := range c.meters {
		labels[m.ID] = meterFR[m.Kind]
	}
	thumbOf := map[int]template.URL{}
	for i, p := range c.photos {
		n := i + 1
		of := ""
		switch {
		case p.ItemID != nil:
			byItem[*p.ItemID] = append(byItem[*p.ItemID], n)
			of = labels[*p.ItemID]
		case p.MeterID != nil:
			byMeter[*p.MeterID] = append(byMeter[*p.MeterID], n)
			of = labels[*p.MeterID]
		}
		if t, ok := thumbs[p.ID]; ok {
			thumbOf[n] = t
		}
		d.Photos = append(d.Photos, EntryPhoto{N: n, Of: of, SHA256: p.SHA256, Width: p.Width, Height: p.Height})
	}
	index := map[string]int{}
	for _, it := range c.items {
		k := roomKey(it)
		i, ok := index[k]
		if !ok {
			i = len(d.Rooms)
			index[k] = i
			d.Rooms = append(d.Rooms, EntryRoom{Label: it.RoomLabel})
		}
		line := EntryItem{Label: it.Label, Quantity: it.Quantity, Comment: it.Comment, Photos: byItem[it.ID]}
		if it.Condition != nil {
			line.Condition = conditionFR[Condition(*it.Condition)]
		}
		if it.Works != nil {
			line.Works = worksFR[Works(*it.Works)]
		}
		for _, n := range line.Photos {
			if t, ok := thumbOf[n]; ok {
				line.Thumbs = append(line.Thumbs, t)
			}
		}
		d.Rooms[i].Items = append(d.Rooms[i].Items, line)
	}
	for _, m := range c.meters {
		value := m.Reading
		if value != "" && m.Unit != "" {
			value += " " + m.Unit
		}
		d.Meters = append(d.Meters, EntryLine{Kind: meterFR[m.Kind], Detail: m.MeterNumber, Value: value, Photos: byMeter[m.ID]})
	}
	for _, k := range c.keys {
		d.Keys = append(d.Keys, EntryLine{Kind: keyFR[k.Kind], Detail: k.Destination, Value: fmt.Sprint(k.Quantity), Comment: k.Comment})
	}
	for _, s := range c.sigs {
		d.Signatures = append(d.Signatures, EntrySignature{Party: partyFR[s.Party], Name: s.SignerName, Method: methodFR[s.Method],
			SignedOn: period.FromTime(s.SignedAt.In(paris())), Reservations: s.Reservations})
	}
	return d
}

// issueEntry renders and archives the EE document of a signed entry, inside
// the transition's transaction, and returns its id.
func (s Service) issueEntry(ctx context.Context, c capture) (uuid.UUID, error) {
	thumbs := map[uuid.UUID]template.URL{}
	for _, p := range c.photos {
		t, err := s.thumbnail(ctx, p)
		if err != nil {
			return uuid.Nil, fmt.Errorf("inventory: thumbnail of photo %s: %w", p.ID, err)
		}
		thumbs[p.ID] = t
	}
	snapshot := entryData(c, nil)
	on := snapshot.DoneOn
	days, err := c.facts.Params.Int64(params.KeyCompletionWindowDays, on)
	if err != nil {
		return uuid.Nil, err
	}
	months, err := c.facts.Params.Int64(params.KeyHeatingWindowMonths, on)
	if err != nil {
		return uuid.Nil, err
	}
	snapshot.CompletionDays, snapshot.HeatingMonths = days, months
	data := entryData(c, thumbs)
	data.CompletionDays, data.HeatingMonths = days, months
	lid := c.head.LeaseID
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassInventory, Template: document.ClassInventory,
		TemplateVersion: EntryTemplate, Series: document.SeriesEntry, AssetID: c.facts.Principal, LeaseID: &lid, Snapshot: snapshot,
		Data: func(number string, issued period.Date) any {
			data.Number, data.IssuedOn = number, issued
			return data
		}}, c.facts.Params)
	if err != nil {
		return uuid.Nil, err
	}
	return iss.DocumentID, nil
}

// thumbnail reads an archived photo and returns a small JPEG of it as a data
// URL for the PDF.
func (s Service) thumbnail(ctx context.Context, p photoRow) (template.URL, error) {
	r, err := s.Photos.Store.Open(ctx, p.ObjectKey, p.ObjectVersion)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	src, err := jpeg.Decode(io.LimitReader(r, int64(MaxPhotoPixels)*4))
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, shrink(src, thumbSide), &jpeg.Options{Quality: 75}); err != nil {
		return "", err
	}
	// #nosec G203 -- a JPEG this function encoded, never user markup.
	return template.URL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b.Bytes())), nil
}

// shrink scales src down so its long side is at most side, averaging the
// source pixels each output pixel covers; a smaller image is returned as is.
func shrink(src image.Image, side int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return src
	}
	dw, dh := side, h*side/w
	if h > w {
		dw, dh = w*side/h, side
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	in := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*h/dh, (y+1)*h/dh
		for x := 0; x < dw; x++ {
			x0, x1 := x*w/dw, (x+1)*w/dw
			var r, g, bl, n uint32
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					o := in.PixOffset(xx, yy)
					r += uint32(in.Pix[o])
					g += uint32(in.Pix[o+1])
					bl += uint32(in.Pix[o+2])
					n++
				}
			}
			o := out.PixOffset(x, y)
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return out
}
