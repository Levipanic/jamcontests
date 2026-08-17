package web

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// maxAvatarDimension — длинная сторона аватара после ресайза.
	maxAvatarDimension = 256
	// maxAvatarInputDimension — предел стороны исходного изображения до декодирования.
	maxAvatarInputDimension = 4096
	// maxAvatarInputPixels — предел общего числа пикселей исходного изображения.
	maxAvatarInputPixels = 12 << 20
	// maxAvatarStoredBytes — жёсткий потолок файла аватара на диске.
	maxAvatarStoredBytes = 256 << 10
)

// processAvatar декодирует загруженные байты, уменьшает изображение до
// maxAvatarDimension по длинной стороне и перекодирует в канонический формат:
// JPEG без прозрачности, PNG с прозрачностью. Возвращает готовые к записи
// байты и расширение файла.
func processAvatar(data []byte) ([]byte, string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("Некорректное изображение.")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > maxAvatarInputDimension || cfg.Height > maxAvatarInputDimension ||
		int64(cfg.Width)*int64(cfg.Height) > maxAvatarInputPixels {
		return nil, "", errors.New("Изображение слишком велико: максимум 4096×4096 пикселей.")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("Некорректное изображение.")
	}
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	if width > maxAvatarDimension || height > maxAvatarDimension {
		if width > height {
			height = maxAvatarDimension * height / width
			width = maxAvatarDimension
		} else {
			width = maxAvatarDimension * width / height
			height = maxAvatarDimension
		}
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] != 255 {
			return encodeAvatarPNG(dst)
		}
	}
	return encodeAvatarJPEG(dst)
}

func encodeAvatarPNG(src *image.RGBA) ([]byte, string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		return nil, "", errors.New("Не удалось сжать изображение.")
	}
	if buf.Len() <= maxAvatarStoredBytes {
		return buf.Bytes(), ".png", nil
	}
	opaque := image.NewRGBA(src.Bounds())
	draw.Draw(opaque, opaque.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(opaque, opaque.Bounds(), src, src.Bounds().Min, draw.Over)
	return encodeAvatarJPEG(opaque)
}

func encodeAvatarJPEG(src *image.RGBA) ([]byte, string, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 80}); err != nil {
		return nil, "", errors.New("Не удалось сжать изображение.")
	}
	if buf.Len() > maxAvatarStoredBytes {
		return nil, "", errors.New("Не удалось сжать изображение: файл всё ещё превышает допустимый размер.")
	}
	return buf.Bytes(), ".jpg", nil
}
