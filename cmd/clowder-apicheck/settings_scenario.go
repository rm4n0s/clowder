package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"strings"

	"github.com/rm4n0s/errors"
)

// settingsScenarioName is the name of the settings scenario.
const settingsScenarioName = "settings"

// settingsSuffix is added to the display name by the settings scenario, to see
// the change, and taken off again at the end.
const settingsSuffix = " (apicheck)"

// settingsScenario is the scenario of step 18.1 for the profile, the avatar,
// the settings and the password. It logs in, changes the display name and the
// theme, uploads an avatar and fetches it from the address in avatar_url,
// logs in a second time, changes the password and shows that the second login
// is dead (401) while the first still works, then puts the password, the name
// and the theme back. An avatar that the user had is replaced and stays; one
// that the user did not have is removed again.
func settingsScenario(ctx context.Context, r *runner) error {
	a, err := r.login(ctx)
	if err != nil {
		return err
	}
	name, _ := a.field("user", "display_name").(string)
	hadAvatar := a.field("user", "avatar_url") != nil
	changed := r.password + "-changed-by-apicheck"
	r.c.redac.add(changed)

	a, err = r.c.do(ctx, "GET", "/api/v1/me/settings", nil, 200)
	if err != nil {
		return err
	}
	theme, _ := a.field("theme").(string)
	if theme == "" || a.field("sound") == nil || a.field("browser_notifications") == nil {
		return errors.New(tagAnswerUnexpected, "the settings lack the theme, the sound or the browser notifications")
	}

	a, err = r.c.do(ctx, "PATCH", "/api/v1/me", map[string]any{"display_name": name + settingsSuffix}, 200)
	if err != nil {
		return err
	}
	if a.field("display_name") != name+settingsSuffix {
		return errors.New(tagAnswerUnexpected, "the display name did not change", "got", a.field("display_name"))
	}

	newTheme := "dark"
	if theme == "dark" {
		newTheme = "light"
	}
	a, err = r.c.do(ctx, "PATCH", "/api/v1/me/settings", map[string]any{"theme": newTheme}, 200)
	if err != nil {
		return err
	}
	if a.field("theme") != newTheme {
		return errors.New(tagAnswerUnexpected, "the theme did not change", "got", a.field("theme"))
	}

	picture, err := settingsPicture()
	if err != nil {
		return err
	}
	a, err = r.c.sendFile(ctx, http.MethodPut, "/api/v1/me/avatar", "avatar.png", picture, 200)
	if err != nil {
		return err
	}
	avatar, _ := a.field("avatar_url").(string)
	if !strings.HasPrefix(avatar, "/api/v1/avatars/") {
		return errors.New(tagAnswerUnexpected, "the user has no avatar_url after the upload", "got", a.field("avatar_url"))
	}
	a, err = r.c.download(ctx, avatar, 200)
	if err != nil {
		return err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(a.body))
	if err != nil || cfg.Width != 256 || cfg.Height != 256 || a.header.Get("Content-Type") != "image/png" {
		return errors.New(tagAnswerUnexpected, "the avatar is not a 256x256 png", "type", a.header.Get("Content-Type"), "width", cfg.Width, "height", cfg.Height)
	}

	// A second login of the same user, in another client.
	second := &client{base: r.c.base, http: r.c.http, out: r.c.out, redac: r.c.redac, brief: r.c.brief}
	if _, err := r.loginOn(ctx, second, r.username, r.password); err != nil {
		return err
	}
	if _, err := second.do(ctx, "GET", "/api/v1/me", nil, 200); err != nil {
		return err
	}

	// A wrong current password changes nothing.
	if _, err := r.c.do(ctx, "POST", "/api/v1/me/password",
		map[string]any{"current_password": r.password + "x", "new_password": changed}, 422); err != nil {
		return err
	}
	if _, err := r.c.do(ctx, "POST", "/api/v1/me/password",
		map[string]any{"current_password": r.password, "new_password": changed}, 204); err != nil {
		return err
	}
	// The other login ended with the change; this one stays.
	if _, err := second.do(ctx, "GET", "/api/v1/me", nil, 401); err != nil {
		return err
	}
	if _, err := r.c.do(ctx, "GET", "/api/v1/me", nil, 200); err != nil {
		return err
	}
	// Put the password back.
	if _, err := r.c.do(ctx, "POST", "/api/v1/me/password",
		map[string]any{"current_password": changed, "new_password": r.password}, 204); err != nil {
		return err
	}

	// Put the rest back.
	if _, err := r.c.do(ctx, "PATCH", "/api/v1/me", map[string]any{"display_name": name}, 200); err != nil {
		return err
	}
	if _, err := r.c.do(ctx, "PATCH", "/api/v1/me/settings", map[string]any{"theme": theme}, 200); err != nil {
		return err
	}
	if !hadAvatar {
		if _, err := r.c.do(ctx, "DELETE", "/api/v1/me/avatar", nil, 204); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintln(r.c.out, "(the user had an avatar: the new one stays)")
		_, _ = fmt.Fprintln(r.c.out)
	}
	if _, err := r.c.do(ctx, "POST", "/api/v1/auth/logout", nil, 204); err != nil {
		return err
	}
	r.c.token = ""
	return nil
}

// settingsPicture draws the picture the scenario uploads: a ginger disc on a
// cream ground, 64 by 64 pixels, with the standard library.
func settingsPicture() ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{R: 0xfe, G: 0xf3, B: 0xe2, A: 255}), image.Point{}, draw.Src)
	for y := range 64 {
		for x := range 64 {
			if dx, dy := x-32, y-32; dx*dx+dy*dy <= 24*24 {
				img.SetNRGBA(x, y, color.NRGBA{R: 0xc2, G: 0x41, B: 0x0c, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, errors.NewErr(tagRequestEncodeFailed, err)
	}
	return buf.Bytes(), nil
}
