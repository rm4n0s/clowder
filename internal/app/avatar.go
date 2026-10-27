package app

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"  // registers the GIF decoder
	_ "image/jpeg" // registers the JPEG decoder
	"image/png"
	"io"
	"math"
	"strconv"

	"github.com/rm4n0s/errors"
)

// The limits of an avatar (SPEC §7.14).
const (
	// AvatarMaxBytes is the largest file that SetAvatar takes.
	AvatarMaxBytes = 2 << 20
	// AvatarMaxPixels is the largest width and height of the file that
	// SetAvatar takes.
	AvatarMaxPixels = 4096
	// AvatarSize is the width and the height of the stored avatar.
	AvatarSize = 256
)

// pictureSlots is the number of avatars decoded at once; the others wait.
const pictureSlots = 2

// FieldAvatar names the avatar in the metadata "fields" of a TagInvalidInput
// error.
const FieldAvatar = "avatar"

// Messages of the field "avatar".
const (
	messageAvatarTooLarge  = "Use a picture of at most 2 MiB."
	messageAvatarTooBig    = "Use a picture of at most 4096 by 4096 pixels."
	messageAvatarNotPicure = "Use a PNG, JPEG or GIF picture."
)

// AvatarPath returns the address of the avatar of the user, with the key in
// the query so that a new avatar has a new address (SPEC §7.14), or "" when
// the key is empty, which is a user without an avatar.
func AvatarPath(userID uint64, key string) string {
	if key == "" {
		return ""
	}
	return "/avatars/" + strconv.FormatUint(userID, 10) + "?v=" + key
}

// AvatarURL is the address of the avatar of the user, or "" when there is none.
func (u User) AvatarURL() string {
	return AvatarPath(u.ID, u.AvatarKey)
}

// AvatarURL is the address of the avatar of the user, or "" when there is none.
func (u UserRef) AvatarURL() string {
	return AvatarPath(u.ID, u.AvatarKey)
}

// AvatarURL is the address of the avatar of the member, or "" when there is
// none.
func (m Member) AvatarURL() string {
	return AvatarPath(m.UserID, m.AvatarKey)
}

// makeAvatar turns the file a person uploaded into the PNG that is stored: it
// reads at most AvatarMaxBytes from r, checks the format and the size from the
// header of the picture before decoding it, decodes it (the first frame of a
// GIF), cuts the centre square, scales it to AvatarSize by AvatarSize with a
// box filter and encodes it as a PNG. A picture that breaks a rule fails with
// TagInvalidInput (field "avatar"); a reader or an encoder that fails, with
// the tags AvatarReadFailed and AvatarEncodeFailed.
func makeAvatar(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, AvatarMaxBytes+1))
	if err != nil {
		return nil, errors.NewErr(tagAvatarReadFailed, err)
	}
	if len(data) > AvatarMaxBytes {
		return nil, errors.New(TagInvalidInput, "the avatar is too large",
			"fields", map[string]string{FieldAvatar: messageAvatarTooLarge}, "limit", AvatarMaxBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg" && format != "gif") {
		return nil, errors.New(TagInvalidInput, "the avatar is not a PNG, JPEG or GIF picture",
			"fields", map[string]string{FieldAvatar: messageAvatarNotPicure})
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > AvatarMaxPixels || cfg.Height > AvatarMaxPixels {
		return nil, errors.New(TagInvalidInput, "the avatar has too many pixels",
			"fields", map[string]string{FieldAvatar: messageAvatarTooBig}, "width", cfg.Width, "height", cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New(TagInvalidInput, "the avatar cannot be decoded",
			"fields", map[string]string{FieldAvatar: messageAvatarNotPicure})
	}
	if b := img.Bounds(); b.Dx() < 1 || b.Dy() < 1 || b.Dx() > AvatarMaxPixels || b.Dy() > AvatarMaxPixels {
		return nil, errors.New(TagInvalidInput, "the avatar has too many pixels",
			"fields", map[string]string{FieldAvatar: messageAvatarTooBig}, "width", b.Dx(), "height", b.Dy())
	}
	var out bytes.Buffer
	if err := png.Encode(&out, scaleSquare(centreSquare(img.Bounds()), img, AvatarSize)); err != nil {
		return nil, errors.NewErr(tagAvatarEncodeFailed, err)
	}
	return out.Bytes(), nil
}

// centreSquare returns the largest square that fits in b, centred in it.
func centreSquare(b image.Rectangle) image.Rectangle {
	side := min(b.Dx(), b.Dy())
	x := b.Min.X + (b.Dx()-side)/2
	y := b.Min.Y + (b.Dy()-side)/2
	return image.Rect(x, y, x+side, y+side)
}

// span is how much of one destination pixel a source pixel covers.
type span struct {
	// dst is the index of the destination pixel.
	dst int
	// weight is the length of the overlap, in source pixels.
	weight float64
}

// spansOf returns, for each of the side source pixels of a line, the
// destination pixels (of size) it overlaps and by how much, when the line is
// scaled to size pixels: the source pixel p covers [p, p+1) and the
// destination pixel d covers [d*side/size, (d+1)*side/size).
func spansOf(side, size int) [][]span {
	scale := float64(side) / float64(size)
	out := make([][]span, side)
	for p := range side {
		first := int(math.Floor(float64(p) / scale))
		last := int(math.Ceil(float64(p+1)/scale)) - 1
		for d := max(first, 0); d <= min(last, size-1); d++ {
			lo := math.Max(float64(p), float64(d)*scale)
			hi := math.Min(float64(p+1), float64(d+1)*scale)
			if hi > lo {
				out[p] = append(out[p], span{dst: d, weight: hi - lo})
			}
		}
	}
	return out
}

// scaleSquare scales the square sq of img to size by size pixels with a box
// filter: every destination pixel is the average, weighted by the covered
// area, of the source pixels it covers, with the colour premultiplied by the
// alpha, so that transparent pixels do not tint their neighbours. It reads
// one row of the picture at a time, so that a large picture is not copied.
func scaleSquare(sq image.Rectangle, img image.Image, size int) *image.NRGBA {
	side := sq.Dx()
	cols := spansOf(side, size)
	rows := spansOf(side, size)
	// acc holds the sums of the premultiplied channels of every destination
	// pixel: red, green, blue, alpha.
	acc := make([]float64, size*size*4)
	line := image.NewRGBA(image.Rect(0, 0, side, 1))
	for sy := range side {
		draw.Draw(line, line.Bounds(), img, image.Pt(sq.Min.X, sq.Min.Y+sy), draw.Src)
		for _, rs := range rows[sy] {
			base := rs.dst * size
			for sx := range side {
				px := line.Pix[sx*4 : sx*4+4]
				for _, cs := range cols[sx] {
					w := rs.weight * cs.weight
					i := (base + cs.dst) * 4
					acc[i] += w * float64(px[0])
					acc[i+1] += w * float64(px[1])
					acc[i+2] += w * float64(px[2])
					acc[i+3] += w * float64(px[3])
				}
			}
		}
	}
	area := float64(side) / float64(size)
	area *= area
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < size*size; i++ {
		r, g, b, a := acc[i*4]/area, acc[i*4+1]/area, acc[i*4+2]/area, acc[i*4+3]/area
		out.SetNRGBA(i%size, i/size, unpremultiply(r, g, b, a))
	}
	return out
}

// unpremultiply makes the colour of the premultiplied channels, 0 to 255 each.
func unpremultiply(r, g, b, a float64) color.NRGBA {
	if a < 0.5 {
		return color.NRGBA{}
	}
	clamp := func(v float64) uint8 { return uint8(math.Min(255, math.Max(0, math.Round(v*255/a)))) }
	return color.NRGBA{R: clamp(r), G: clamp(g), B: clamp(b), A: uint8(math.Min(255, math.Round(a)))}
}

const (
	tagAvatarReadFailed   = "AvatarReadFailed"
	tagAvatarEncodeFailed = "AvatarEncodeFailed"
)
