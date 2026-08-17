package web

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func avatarFixturePNG(t *testing.T, width, height int, transparent bool) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := color.RGBA{uint8(x % 251), uint8(y % 199), uint8((x + y) % 173), 255}
			if transparent && (x+y)%7 == 0 {
				c.A = 0
			}
			src.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// avatarFixtureHugePNG builds a tiny PNG whose IHDR claims huge dimensions,
// mimicking a decompression bomb without allocating its pixel data.
func avatarFixtureHugePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	data := avatarFixturePNG(t, 4, 4, false)
	binary.BigEndian.PutUint32(data[16:20], uint32(width))
	binary.BigEndian.PutUint32(data[20:24], uint32(height))
	return data
}

func TestProcessAvatarResizesToMaxDimension(t *testing.T) {
	out, ext, err := processAvatar(avatarFixturePNG(t, 2000, 1500, false))
	if err != nil {
		t.Fatal(err)
	}
	if ext != ".jpg" {
		t.Fatalf("opaque avatar encoded as %q, want .jpg", ext)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 256 || cfg.Height != 192 {
		t.Fatalf("resized to %dx%d, want 256x192", cfg.Width, cfg.Height)
	}
	if len(out) > maxAvatarStoredBytes {
		t.Fatalf("stored bytes %d exceed cap %d", len(out), maxAvatarStoredBytes)
	}
}

func TestProcessAvatarKeepsSmallImageUntouched(t *testing.T) {
	out, _, err := processAvatar(avatarFixturePNG(t, 100, 80, false))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 100 || cfg.Height != 80 {
		t.Fatalf("small avatar was scaled to %dx%d", cfg.Width, cfg.Height)
	}
}

func TestProcessAvatarKeepsTransparencyAsPNG(t *testing.T) {
	out, ext, err := processAvatar(avatarFixturePNG(t, 800, 600, true))
	if err != nil {
		t.Fatal(err)
	}
	if ext != ".png" {
		t.Fatalf("transparent avatar encoded as %q, want .png", ext)
	}
	src, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	b := src.Bounds()
	hasAlpha := false
	for y := b.Min.Y; y < b.Max.Y && !hasAlpha; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, alpha := src.At(x, y).RGBA()
			if alpha != 0xffff {
				hasAlpha = true
				break
			}
		}
	}
	if !hasAlpha {
		t.Fatal("transparency lost in PNG output")
	}
}

func TestProcessAvatarRejectsDecompressionBomb(t *testing.T) {
	for name, fixture := range map[string][]byte{
		"side oversize":    avatarFixtureHugePNG(t, 50000, 8),
		"pixel count bomb": avatarFixtureHugePNG(t, 5000, 5000),
	} {
		if _, _, err := processAvatar(fixture); err == nil {
			t.Fatalf("%s: bomb accepted", name)
		}
	}
}

func TestProcessAvatarRejectsGarbage(t *testing.T) {
	if _, _, err := processAvatar([]byte("this is definitely not an image")); err == nil {
		t.Fatal("garbage accepted as avatar")
	}
}
