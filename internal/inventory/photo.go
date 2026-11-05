package inventory

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// Product limits of an inventory photo (not legal values). The web app sends
// photos resized to 2 048 px on the long side; a camera file is accepted up to
// MaxPhotoSide, and anything larger is refused before it is decoded.
const (
	MaxPhotoSide   = 8192
	MaxPhotoPixels = 40_000_000
	photoQuality   = 85
)

// ErrNotPhoto is returned for a file that is not a JPEG or PNG image, or is
// larger than the limits.
var ErrNotPhoto = errors.New("inventory: not a JPEG or PNG photo within the size limits")

// CleanPhoto turns an uploaded JPEG or PNG into the JPEG archived for an
// inventory (RENT-INV-001 §3): the pixels only, upright, re-encoded, so no
// metadata survives (EXIF, location, maker notes, XMP, ICC, comments). The EXIF
// orientation of a JPEG is applied to the pixels first, so a photo taken
// sideways is not archived sideways. It returns the cleaned file and its
// dimensions.
func CleanPhoto(data []byte) (out []byte, width, height int, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, 0, 0, ErrNotPhoto
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxPhotoSide || cfg.Height > MaxPhotoSide ||
		int64(cfg.Width)*int64(cfg.Height) > MaxPhotoPixels {
		return nil, 0, 0, ErrNotPhoto
	}
	var img image.Image
	switch format {
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
	default:
		img, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, 0, 0, ErrNotPhoto
	}
	if format == "jpeg" {
		img = orient(img, jpegOrientation(data))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: photoQuality}); err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}

// jpegOrientation reads the EXIF orientation (tag 0x0112 of IFD0) of a JPEG,
// 1 (upright) when there is none or it cannot be read.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			v := int(bo.Uint16(t[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation (1–8) to the pixels.
func orient(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	in := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2: // mirrored
				nx, ny = w-1-x, y
			case 3: // rotated 180°
				nx, ny = w-1-x, h-1-y
			case 4: // mirrored vertically
				nx, ny = x, h-1-y
			case 5: // transposed
				nx, ny = y, x
			case 6: // rotated 90° clockwise to be upright
				nx, ny = h-1-y, x
			case 7: // transversed
				nx, ny = h-1-y, w-1-x
			case 8: // rotated 90° counter-clockwise to be upright
				nx, ny = y, w-1-x
			}
			out.Set(nx, ny, in.At(x, y))
		}
	}
	return out
}
