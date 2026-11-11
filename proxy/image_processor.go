package proxy

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"runtime"
	"strings"

	xdraw "golang.org/x/image/draw"
)

const (
	imageMaxLongEdge     = 4000
	imageMaxSinglePixels = 4_000_000
	imageMaxMultiPixels  = 1_000_000
	imageForceBytes      = 200_000
	imageMaxBytes        = 64 << 20
	imageMaxPixels       = 100_000_000
)

var imageSlots = make(chan struct{}, imageWorkerLimit())

func imageWorkerLimit() int {
	n := runtime.GOMAXPROCS(0)
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}

type processedImage struct {
	data   string
	format string
}

func processPayloadImages(payload *KiroPayload) bool {
	if payload == nil {
		return false
	}
	hasImages := false

	processMessage := func(message *KiroUserInputMessage) {
		if message == nil || len(message.Images) == 0 {
			return
		}
		hasImages = true
		imageCount := len(message.Images)
		for i := range message.Images {
			imageSlots <- struct{}{}
			out := compressKiroImage(message.Images[i], imageCount)
			<-imageSlots
			message.Images[i].Format = out.format
			message.Images[i].Source.Bytes = out.data
		}
	}

	processMessage(&payload.ConversationState.CurrentMessage.UserInputMessage)
	for i := range payload.ConversationState.History {
		processMessage(payload.ConversationState.History[i].UserInputMessage)
	}
	return hasImages
}

func compressKiroImage(img KiroImage, imageCount int) processedImage {
	original := strings.TrimSpace(img.Source.Bytes)
	raw, err := decodeImageBase64(original)
	if err != nil {
		return processedImage{data: original, format: normalizeImageFormat(img.Format)}
	}
	if len(raw) > imageMaxBytes {
		return processedImage{data: original, format: actualImageFormat(raw, img.Format)}
	}

	format := actualImageFormat(raw, img.Format)
	if format == "webp" || format == "gif" {
		// The Go standard library cannot resize these formats without losing
		// animation. Correct the MIME label and pass the original bytes through.
		return processedImage{data: original, format: format}
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return processedImage{data: original, format: format}
	}
	sourcePixels := uint64(cfg.Width) * uint64(cfg.Height)
	if sourcePixels > imageMaxPixels {
		return processedImage{data: original, format: format}
	}

	maxPixels := imageMaxSinglePixels
	if imageCount >= 2 {
		maxPixels = imageMaxMultiPixels
	}
	targetWidth, targetHeight := scaledImageSize(cfg.Width, cfg.Height, imageMaxLongEdge, maxPixels)
	needsResize := targetWidth != cfg.Width || targetHeight != cfg.Height
	if !needsResize && len(raw) <= imageForceBytes {
		return processedImage{data: original, format: format}
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return processedImage{data: original, format: format}
	}
	var dst image.Image = src
	if needsResize {
		canvas := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		xdraw.CatmullRom.Scale(canvas, canvas.Bounds(), src, src.Bounds(), xdraw.Src, nil)
		dst = canvas
	}

	encoded, encodedFormat, err := encodeImage(dst, format)
	if err != nil || len(encoded) >= len(raw) {
		return processedImage{data: original, format: format}
	}
	if format == "png" && len(raw) > imageForceBytes {
		if jpegData, err := encodeJPEG(dst); err == nil && len(jpegData) < len(encoded) {
			encoded = jpegData
			encodedFormat = "jpeg"
		}
	}

	return processedImage{
		data:   base64.StdEncoding.EncodeToString(encoded),
		format: encodedFormat,
	}
}

func decodeImageBase64(value string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, encoding := range encodings {
		data, err := encoding.DecodeString(value)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func actualImageFormat(raw []byte, declared string) string {
	switch {
	case len(raw) >= 8 && bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "png"
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "jpeg"
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return "gif"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	default:
		return normalizeImageFormat(declared)
	}
}

func normalizeImageFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(format, "image/")))
	switch format {
	case "jpg", "pjpeg":
		return "jpeg"
	case "png", "jpeg", "gif", "webp":
		return format
	default:
		return "png"
	}
}

func scaledImageSize(width, height, maxLongEdge, maxPixels int) (int, int) {
	w, h := float64(width), float64(height)
	if maxLongEdge > 0 {
		longEdge := w
		if h > longEdge {
			longEdge = h
		}
		if longEdge > float64(maxLongEdge) {
			scale := float64(maxLongEdge) / longEdge
			w *= scale
			h *= scale
		}
	}
	if maxPixels > 0 && w*h > float64(maxPixels) {
		scale := math.Sqrt(float64(maxPixels) / (w * h))
		w *= scale
		h *= scale
	}
	return maxOf(1, int(w)), maxOf(1, int(h))
}

func maxOf(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func encodeImage(img image.Image, format string) ([]byte, string, error) {
	var buf bytes.Buffer
	switch format {
	case "jpeg":
		err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 84})
		return buf.Bytes(), "jpeg", err
	case "png":
		err := png.Encode(&buf, img)
		return buf.Bytes(), "png", err
	case "gif":
		err := gif.Encode(&buf, img, nil)
		return buf.Bytes(), "gif", err
	default:
		return nil, format, io.ErrUnexpectedEOF
	}
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82})
	return buf.Bytes(), err
}
