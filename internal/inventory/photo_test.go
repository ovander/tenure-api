package inventory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// withExif inserts an APP1 Exif segment after the SOI of a JPEG: IFD0 with an
// orientation and a fake GPS pointer, plus a maker string, all of which must
// be gone after cleaning.
func withExif(t *testing.T, j []byte, orientation uint16) []byte {
	t.Helper()
	var tiff bytes.Buffer
	tiff.WriteString("II*\x00")
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(8))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(2)) // two entries
	// Orientation, SHORT, count 1, value.
	_ = binary.Write(&tiff, binary.LittleEndian, []uint16{0x0112, 3})
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, []uint16{orientation, 0})
	// GPSInfo pointer, LONG.
	_ = binary.Write(&tiff, binary.LittleEndian, []uint16{0x8825, 4})
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0)) // no next IFD
	tiff.WriteString("GPS 45.7640N 4.8357E SECRET-MAKER")
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	out := append([]byte{}, j[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, j[2:]...)
}

// portrait is a 4×2 image, red on the left half, blue on the right.
func portrait(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= 2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCleanPhotoStripsMetadataAndRotates(t *testing.T) {
	src := withExif(t, portrait(t), 6)
	if jpegOrientation(src) != 6 {
		t.Fatalf("orientation read as %d", jpegOrientation(src))
	}
	out, w, h, err := CleanPhoto(src)
	if err != nil {
		t.Fatal(err)
	}
	if w != 2 || h != 4 {
		t.Errorf("rotated 90°: %d×%d, want 2×4", w, h)
	}
	for _, leak := range []string{"Exif", "GPS", "SECRET-MAKER"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Errorf("cleaned file still holds %q", leak)
		}
	}
	if jpegOrientation(out) != 1 {
		t.Error("the cleaned file carries an orientation")
	}
	// Rotated clockwise: the red left half is now on top.
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := img.At(1, 0).RGBA(); r < b {
		t.Errorf("top pixel is not red: r=%d b=%d", r, b)
	}
	// Cleaning again changes nothing but the encoder's own rounding: same size.
	again, w2, h2, err := CleanPhoto(out)
	if err != nil || w2 != w || h2 != h || len(again) == 0 {
		t.Errorf("second clean: %d×%d %v", w2, h2, err)
	}
}

func TestCleanPhotoOrientations(t *testing.T) {
	for o := uint16(1); o <= 8; o++ {
		_, w, h, err := CleanPhoto(withExif(t, portrait(t), o))
		if err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		}
		wantW, wantH := 4, 2
		if o >= 5 {
			wantW, wantH = 2, 4
		}
		if w != wantW || h != wantH {
			t.Errorf("orientation %d: %d×%d, want %d×%d", o, w, h, wantW, wantH)
		}
	}
}

func TestCleanPhotoRefusals(t *testing.T) {
	var p bytes.Buffer
	_ = png.Encode(&p, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	if _, w, _, err := CleanPhoto(p.Bytes()); err != nil || w != 3 {
		t.Errorf("a PNG is accepted and re-encoded: %v", err)
	}
	for name, data := range map[string][]byte{
		"pdf":   []byte("%PDF-1.7\n%%EOF\n"),
		"empty": nil,
		"gif":   []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
	} {
		if _, _, _, err := CleanPhoto(data); !errors.Is(err, ErrNotPhoto) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A header claiming a huge image is refused before decoding.
	var big bytes.Buffer
	_ = png.Encode(&big, image.NewGray(image.Rect(0, 0, MaxPhotoSide+1, 1)))
	if _, _, _, err := CleanPhoto(big.Bytes()); !errors.Is(err, ErrNotPhoto) {
		t.Errorf("oversized: %v", err)
	}
}
