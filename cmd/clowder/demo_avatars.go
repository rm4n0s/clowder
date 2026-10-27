package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"

	"github.com/rm4n0s/clowder/internal/app"
	"github.com/rm4n0s/errors"
)

// demoAvatarColours are the colours of the avatars of the demo users that have
// one: the ground and the cat on it. A user of the demo who is not here keeps
// the initials, so that the demo shows both.
var demoAvatarColours = map[string][2]color.NRGBA{
	"admin": {{R: 0xfd, G: 0xe6, B: 0x8a, A: 255}, {R: 0x92, G: 0x40, B: 0x0e, A: 255}},
	"alice": {{R: 0xbf, G: 0xdb, B: 0xfe, A: 255}, {R: 0x1e, G: 0x40, B: 0xaf, A: 255}},
	"bob":   {{R: 0xbb, G: 0xf7, B: 0xd0, A: 255}, {R: 0x16, G: 0x65, B: 0x34, A: 255}},
}

// demoAvatarSize is the width and the height of the picture of a demo avatar.
const demoAvatarSize = 256

// makeDemoAvatar draws a cat's head with simple shapes, with no image file: a
// disc for the head, two triangles for the ears and two dots for the eyes, in
// the colour fg on the ground bg. It returns the picture as a PNG.
func makeDemoAvatar(bg, fg color.NRGBA) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, demoAvatarSize, demoAvatarSize))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	eye := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for y := range demoAvatarSize {
		for x := range demoAvatarSize {
			switch {
			case inDisc(x, y, 128, 150, 78),
				inTriangle(x, y, [3][2]int{{58, 120}, {64, 40}, {112, 82}}),
				inTriangle(x, y, [3][2]int{{198, 120}, {192, 40}, {144, 82}}):
				img.SetNRGBA(x, y, fg)
			}
			if inDisc(x, y, 102, 140, 11) || inDisc(x, y, 154, 140, 11) {
				img.SetNRGBA(x, y, eye)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, errors.NewErr(tagDemoAvatarFailed, err)
	}
	return buf.Bytes(), nil
}

// inDisc reports whether the point (x, y) is in the disc of the centre (cx, cy)
// and the radius r.
func inDisc(x, y, cx, cy, r int) bool {
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// inTriangle reports whether the point (x, y) is in the triangle p.
func inTriangle(x, y int, p [3][2]int) bool {
	side := func(a, b [2]int) int {
		return (b[0]-a[0])*(y-a[1]) - (b[1]-a[1])*(x-a[0])
	}
	d1, d2, d3 := side(p[0], p[1]), side(p[1], p[2]), side(p[2], p[0])
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !neg || !pos
}

// makeDemoAvatars gives the demo users of demoAvatarColours an avatar, through
// the accounts service, and says so on stdout.
func makeDemoAvatars(ctx context.Context, accounts *app.Accounts, actors map[string]app.Actor, stdout io.Writer) error {
	for _, d := range demoUsers {
		colours, ok := demoAvatarColours[d.username]
		if !ok {
			continue
		}
		picture, err := makeDemoAvatar(colours[0], colours[1])
		if err != nil {
			return err
		}
		if _, err := accounts.SetAvatar(ctx, actors[d.username], bytes.NewReader(picture)); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "  avatar for %s\n", d.username)
	}
	return nil
}

const tagDemoAvatarFailed = "DemoAvatarFailed"
