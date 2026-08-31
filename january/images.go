package january

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strings"

	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const MaxImageBytes = 3_500_000
const MaxImageDimension = 1024
const maxSourceImageBytes = 64 << 20
const maxImagePixels = 40_000_000

// ImageOptions controls local preprocessing. URLs and data URIs are always unchanged.
type ImageOptions struct{ DisablePreprocessing bool }

// PrepareImage returns an image string for FoodAnalysis.AnalyzePhoto.
// source accepts a public HTTP(S) URL, data URI, trusted local path, []byte,
// io.Reader (read from its current position; never closed), or image.Image.
// Never accept an untrusted user's string as a local filesystem path.
// Local images are validated; animations are rejected. Large, rotated or
// unsupported still formats are resized/flattened on white and encoded as JPEG.
// Re-encoding strips metadata. Compliant original bytes retain their metadata.
func PrepareImage(source any, options ...ImageOptions) (string, error) {
	if len(options) > 1 {
		return "", fmt.Errorf("%w: at most one ImageOptions is allowed", ErrInvalidInput)
	}
	preprocess := len(options) == 0 || !options[0].DisablePreprocessing
	var data []byte
	var decoded image.Image
	var err error
	switch value := source.(type) {
	case string:
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "data:") {
			return value, nil
		}
		if strings.Contains(value, "://") || strings.HasPrefix(lower, "file:") {
			return "", fmt.Errorf("%w: use HTTP(S), a data URI, or a trusted local path", ErrInvalidInput)
		}
		file, openErr := os.Open(value)
		if openErr != nil {
			return "", openErr
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, maxSourceImageBytes+1))
	case []byte:
		data = value
	case io.Reader:
		data, err = io.ReadAll(io.LimitReader(value, maxSourceImageBytes+1))
	case image.Image:
		if !preprocess {
			return "", fmt.Errorf("%w: image.Image requires preprocessing", ErrInvalidInput)
		}
		decoded = value
	default:
		return "", fmt.Errorf("%w: expected an image URL, path, bytes, reader, or image.Image", ErrInvalidInput)
	}
	if err != nil {
		return "", fmt.Errorf("%w: cannot read image: %w", ErrInvalidInput, err)
	}
	if decoded == nil {
		if len(data) == 0 || len(data) > maxSourceImageBytes {
			return "", fmt.Errorf("%w: image is empty or exceeds the 64 MiB source limit", ErrInvalidInput)
		}
		if animatedWebP(data) {
			return "", fmt.Errorf("%w: animated images are not supported", ErrInvalidInput)
		}
		config, format, configErr := image.DecodeConfig(bytes.NewReader(data))
		if configErr != nil {
			return "", fmt.Errorf("%w: unreadable image; convert HEIC/HEIF/AVIF to JPEG or PNG before scanning", ErrInvalidInput)
		}
		if !safeImageDimensions(config.Width, config.Height) {
			return "", fmt.Errorf("%w: image exceeds the 40-million-pixel limit", ErrInvalidInput)
		}
		if format == "gif" {
			g, gifErr := gif.DecodeAll(bytes.NewReader(data))
			if gifErr != nil || len(g.Image) != 1 {
				return "", fmt.Errorf("%w: corrupt or animated GIF", ErrInvalidInput)
			}
		}
		mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp"}[format]
		if !preprocess {
			if mime == "" || len(data) > MaxImageBytes {
				return "", fmt.Errorf("%w: use JPEG, PNG, WEBP, or still GIF under 3.5 MB, or enable preprocessing", ErrInvalidInput)
			}
			return imageDataURI(data, mime), nil
		}
		decoded, _, err = image.Decode(bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("%w: truncated or corrupt image", ErrInvalidInput)
		}
		orientation := imageOrientation(data)
		_, cmyk := decoded.(*image.CMYK)
		_, gray16 := decoded.(*image.Gray16)
		_, rgba64 := decoded.(*image.RGBA64)
		_, nrgba64 := decoded.(*image.NRGBA64)
		if mime != "" && len(data) <= MaxImageBytes && max(config.Width, config.Height) <= MaxImageDimension && orientation <= 1 && !cmyk && !gray16 && !rgba64 && !nrgba64 {
			return imageDataURI(data, mime), nil
		}
		decoded = orientImage(decoded, orientation)
	}
	bounds := decoded.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if !safeImageDimensions(w, h) {
		return "", fmt.Errorf("%w: invalid or oversized image dimensions", ErrInvalidInput)
	}
	if max(w, h) > MaxImageDimension {
		scale := float64(MaxImageDimension) / float64(max(w, h))
		w, h = max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	}
	flattened := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(flattened, flattened.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(flattened, flattened.Bounds(), decoded, bounds, draw.Over, nil)
	for _, quality := range []int{85, 75, 65} {
		var buffer bytes.Buffer
		if err := jpeg.Encode(&buffer, flattened, &jpeg.Options{Quality: quality}); err != nil {
			return "", fmt.Errorf("%w: image encoding failed", ErrInvalidInput)
		}
		if buffer.Len() <= MaxImageBytes {
			return imageDataURI(buffer.Bytes(), "image/jpeg"), nil
		}
	}
	return "", fmt.Errorf("%w: image cannot fit the 3.5 MB limit", ErrInvalidInput)
}

func safeImageDimensions(w, h int) bool { return w > 0 && h > 0 && w <= maxImagePixels/h }
func imageDataURI(data []byte, mime string) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}
func animatedWebP(data []byte) bool {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return false
	}
	for offset := 12; offset+8 <= len(data); {
		size := int64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		if size > int64(len(data)-offset-8) {
			return false
		}
		kind := string(data[offset : offset+4])
		if kind == "ANIM" || kind == "ANMF" || (kind == "VP8X" && size > 0 && data[offset+8]&2 != 0) {
			return true
		}
		offset += 8 + int(size) + (int(size) & 1)
	}
	return false
}

// Read TIFF Orientation from JPEG APP1 EXIF without retaining any metadata.
func imageOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			break
		}
		marker := data[offset+1]
		if marker == 0xda || marker == 0xd9 {
			break
		}
		size := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if size < 2 || offset+2+size > len(data) {
			break
		}
		segment := data[offset+4 : offset+2+size]
		if marker == 0xe1 && len(segment) >= 14 && string(segment[:6]) == "Exif\x00\x00" {
			tiff := segment[6:]
			var order binary.ByteOrder
			if string(tiff[:2]) == "II" {
				order = binary.LittleEndian
			} else if string(tiff[:2]) == "MM" {
				order = binary.BigEndian
			} else {
				return 1
			}
			if order.Uint16(tiff[2:4]) != 42 {
				return 1
			}
			start := int64(order.Uint32(tiff[4:8]))
			if start+2 > int64(len(tiff)) {
				return 1
			}
			count := int(order.Uint16(tiff[start : start+2]))
			for i := 0; i < count; i++ {
				p := int(start) + 2 + i*12
				if p+12 > len(tiff) {
					return 1
				}
				if order.Uint16(tiff[p:p+2]) == 0x112 && order.Uint16(tiff[p+2:p+4]) == 3 && order.Uint32(tiff[p+4:p+8]) == 1 {
					v := int(order.Uint16(tiff[p+8 : p+10]))
					if v >= 1 && v <= 8 {
						return v
					}
				}
			}
		}
		offset += 2 + size
	}
	return 1
}

func orientImage(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := x, y
			switch orientation {
			case 2:
				dx = w - 1 - x
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dy = h - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
