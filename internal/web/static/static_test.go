package static_test

import (
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rm4n0s/clowder/internal/web/static"
)

func TestFS(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"datastar.js"},
		{"app.css"},
		{"code.css"},
		{"code.js"},
		{"board.js"},
		{"messages.js"},
		{"notify.js"},
		{"favicon.svg"},
		{"favicon-dot.svg"},
		{"favicon-32.png"},
		{"apple-touch-icon.png"},
		{"fonts/README.md"},
		{"fonts/bricolage-grotesque/latin.woff2"},
		{"fonts/bricolage-grotesque/latin-ext.woff2"},
		{"fonts/nunito/latin.woff2"},
		{"fonts/nunito/latin-ext.woff2"},
		{"fonts/jetbrains-mono/latin.woff2"},
		{"fonts/jetbrains-mono/latin-ext.woff2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := static.FS.ReadFile(tt.name)
			if err != nil {
				t.Fatalf("read %s: %v", tt.name, err)
			}
			if len(data) == 0 {
				t.Errorf("%s is empty", tt.name)
			}
		})
	}
}

// maxFontBytes is the most the fonts may weigh in all (step 21.1 aims for
// under 400 KiB).
const maxFontBytes = 400 << 10

// TestFontLicenses checks that every font file has the license of its family
// next to it, that it is the SIL Open Font License, that the files are woff2,
// and that together they stay small.
func TestFontLicenses(t *testing.T) {
	total := 0
	found := 0
	err := fs.WalkDir(static.FS, "fonts", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".woff2" {
			return err
		}
		found++
		data, err := static.FS.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(data)
		if len(data) < 4 || string(data[:4]) != "wOF2" {
			t.Errorf("%s is not a woff2 file", p)
		}
		license, err := static.FS.ReadFile(path.Join(path.Dir(p), "OFL.txt"))
		if err != nil {
			t.Errorf("%s has no OFL.txt next to it: %v", p, err)
			return nil
		}
		if !strings.Contains(string(license), "SIL OPEN FONT LICENSE Version 1.1") {
			t.Errorf("the OFL.txt of %s is not the SIL Open Font License 1.1", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk fonts: %v", err)
	}
	if found != 6 {
		t.Errorf("woff2 files = %d, want 6 (Latin and Latin Extended of three families)", found)
	}
	if total > maxFontBytes {
		t.Errorf("the fonts weigh %d bytes, want at most %d", total, maxFontBytes)
	}
}

var (
	reComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reFontURL  = regexp.MustCompile(`url\("/static/([^"]+)"\)`)
	reTokens   = regexp.MustCompile(`--([a-z0-9-]+):\s*([^;]+);`)
	reHexColor = regexp.MustCompile(`^#([0-9a-fA-F]{6})$`)
	reTextVar  = regexp.MustCompile(`(?:^|[;{\s])color:\s*var\(--([a-z0-9-]+)\)`)
	reAnyColor = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\b(?:rgb|rgba|hsl|hsla|hwb|lab|lch|oklab|oklch|color)\(`)
	reDecl     = regexp.MustCompile(`[{;]\s*([a-z-]+)\s*:\s*([^;{}]+)`)
)

// namedColours are the CSS colour names that must not be written in app.css.
var namedColours = []string{
	"white", "black", "red", "green", "blue", "gray", "grey", "orange", "yellow", "purple", "pink", "brown",
	"silver", "gold", "navy", "teal", "maroon", "olive", "lime", "aqua", "fuchsia", "cyan", "magenta",
	"coral", "salmon", "tomato", "crimson", "ivory", "beige", "tan", "khaki",
}

// readCSS returns the file without its comments.
func readCSS(t *testing.T, name string) string {
	t.Helper()
	data, err := static.FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return reComment.ReplaceAllString(string(data), "")
}

// block returns the text between the braces of the first rule whose header is
// exactly header (at the start of a line, indented or not), and the css
// without that rule.
func block(t *testing.T, css, header string) (body, rest string) {
	t.Helper()
	loc := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(header) + ` \{`).FindStringIndex(css)
	if loc == nil {
		t.Fatalf("app.css has no rule %q", header)
	}
	open := loc[1] - 1
	depth := 0
	for i := open; i < len(css); i++ {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return css[open+1 : i], css[:loc[0]] + css[i+1:]
			}
		}
	}
	t.Fatalf("app.css: the rule %q is not closed", header)
	return "", ""
}

// tokenSet is the custom properties of a block, by name.
type tokenSet map[string]string

func parseTokens(body string) tokenSet {
	set := tokenSet{}
	for _, m := range reTokens.FindAllStringSubmatch(body, -1) {
		set[m[1]] = strings.TrimSpace(m[2])
	}
	return set
}

// palettes reads the three token blocks of app.css and returns the light
// tokens, the dark tokens of the system's setting, the dark tokens of the
// user's setting, and the css outside the three blocks.
func palettes(t *testing.T) (light, darkSystem, darkChosen tokenSet, rest string) {
	t.Helper()
	css := readCSS(t, "app.css")
	lightBody, css := block(t, css, ":root")
	media, css := block(t, css, "@media (prefers-color-scheme: dark)")
	systemBody, mediaRest := block(t, media, `:root:not([data-theme="light"])`)
	if strings.TrimSpace(mediaRest) != "" {
		t.Errorf("the dark media block holds more than the dark tokens: %q", strings.TrimSpace(mediaRest))
	}
	chosenBody, css := block(t, css, `:root[data-theme="dark"]`)
	return parseTokens(lightBody), parseTokens(systemBody), parseTokens(chosenBody), css
}

// luminance is the relative luminance of a #rrggbb colour (WCAG 2).
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	m := reHexColor.FindStringSubmatch(hex)
	if m == nil {
		t.Fatalf("%q is not a #rrggbb colour", hex)
	}
	channel := func(s string) float64 {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(m[1][0:2]) + 0.7152*channel(m[1][2:4]) + 0.0722*channel(m[1][4:6])
}

// ratio is the WCAG contrast ratio of two #rrggbb colours.
func ratio(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// pair is a colour that is drawn on a ground, and the contrast it needs: 4.5
// for text, 3 for what is not text (the focus ring, an icon).
type pair struct {
	fg, bg string
	min    float64
}

const (
	textRatio    = 4.5
	nonTextRatio = 3.0
)

// pairs lists every pair of text and ground of the look, as token names
// without the leading dashes. A token that app.css uses as a text colour but
// that is the fg of no pair here fails TestEveryTextTokenIsChecked, so a new
// token cannot skip the check.
func pairs() []pair {
	var ps []pair
	add := func(min float64, bg []string, fg ...string) {
		for _, f := range fg {
			for _, b := range bg {
				ps = append(ps, pair{f, b, min})
			}
		}
	}
	grounds := []string{"c-ground", "c-surface", "c-surface-raised", "c-surface-sunken", "c-bar", "c-field", "c-track"}
	add(textRatio, grounds, "c-text", "c-text-soft", "c-text-muted")
	add(textRatio, []string{"c-ground", "c-surface", "c-surface-raised", "c-surface-sunken", "c-bar", "c-field"}, "c-link")
	add(textRatio, []string{"c-ground", "c-surface", "c-surface-sunken"}, "c-link-hover")
	add(textRatio, []string{"c-ground", "c-surface", "c-surface-raised", "c-surface-sunken", "c-bar"}, "c-danger", "c-success")
	// The accent-tinted ground: the reaction you made, the comment a link points
	// to, the question of a group mention, and (21.3) the reader's own message,
	// the message a search result points to, a card a link points to and an
	// unread notification, with its links and its Delete.
	add(textRatio, []string{"c-accent-wash"}, "c-text", "c-text-soft", "c-text-muted", "c-link", "c-link-hover", "c-badge-accent-text", "c-danger")
	add(textRatio, []string{"c-accent"}, "c-on-accent")
	add(textRatio, []string{"c-danger"}, "c-on-danger")
	add(textRatio, []string{"c-badge-accent-bg"}, "c-badge-accent-text")
	add(textRatio, []string{"c-badge-sage-bg"}, "c-badge-sage-text")
	for i := range 8 {
		n := strconv.Itoa(i)
		add(textRatio, []string{"c-avatar-" + n + "-bg"}, "c-avatar-"+n+"-text")
	}
	add(textRatio, []string{"code-ground"}, "code-text", "code-name", "code-keyword", "code-function", "code-type",
		"code-string", "code-number", "code-comment", "code-operator", "code-punctuation")
	// Not text: the focus ring next to every ground, the accent of the dots and
	// the lines, the icons.
	add(nonTextRatio, grounds, "c-focus")
	add(nonTextRatio, []string{"c-ground", "c-surface", "c-surface-raised", "c-bar"}, "c-icon-muted", "c-accent")
	// (21.4) The knob of a switch that is off on its track, and the accent of a
	// chosen card or pill and of the paw of a notice, on the accent wash.
	add(nonTextRatio, []string{"c-track"}, "c-icon-muted")
	add(nonTextRatio, []string{"c-accent-wash"}, "c-accent")
	add(nonTextRatio, []string{"c-ground", "c-surface", "c-surface-raised", "c-surface-sunken"}, "c-sage")
	return ps
}

// TestContrast checks every declared pair of text and ground, in the light
// palette and in both dark ones.
func TestContrast(t *testing.T) {
	light, darkSystem, darkChosen, _ := palettes(t)
	palettes := []struct {
		name   string
		tokens tokenSet
	}{{"light", light}, {"dark (system)", darkSystem}, {"dark (chosen)", darkChosen}}
	for _, p := range palettes {
		for _, pr := range pairs() {
			t.Run(p.name+"/"+pr.fg+" on "+pr.bg, func(t *testing.T) {
				fg, ok := p.tokens[pr.fg]
				if !ok {
					t.Fatalf("the %s palette has no --%s", p.name, pr.fg)
				}
				bg, ok := p.tokens[pr.bg]
				if !ok {
					t.Fatalf("the %s palette has no --%s", p.name, pr.bg)
				}
				if got := ratio(t, fg, bg); got < pr.min {
					t.Errorf("--%s (%s) on --%s (%s) = %.2f:1, want at least %.1f:1", pr.fg, fg, pr.bg, bg, got, pr.min)
				}
			})
		}
	}
}

// TestPalettesAgree checks that the two dark blocks are the same, and that the
// dark palette sets every colour the light one does.
func TestPalettesAgree(t *testing.T) {
	light, darkSystem, darkChosen, _ := palettes(t)
	for name, v := range darkSystem {
		if darkChosen[name] != v {
			t.Errorf("--%s is %q in the dark block of the system's setting and %q in the one of the user's", name, v, darkChosen[name])
		}
	}
	for name, v := range darkChosen {
		if _, ok := darkSystem[name]; !ok {
			t.Errorf("--%s (%s) is in the dark block of the user's setting only", name, v)
		}
		if _, ok := light[name]; !ok {
			t.Errorf("--%s is dark only: the light palette must define it too", name)
		}
	}
	for name := range light {
		themed := strings.HasPrefix(name, "c-") || strings.HasPrefix(name, "code-") || strings.HasPrefix(name, "shadow-") || strings.HasPrefix(name, "filter-")
		if _, ok := darkChosen[name]; themed && !ok {
			t.Errorf("--%s is set for light and not for dark", name)
		}
	}
}

// TestEveryTextTokenIsChecked checks that each custom property that app.css or
// code.css uses as the colour of text has a pair in pairs(), so that a new
// token cannot skip the contrast test.
func TestEveryTextTokenIsChecked(t *testing.T) {
	_, _, _, rest := palettes(t)
	used := map[string]bool{}
	for _, css := range []string{rest, readCSS(t, "code.css")} {
		for _, m := range reTextVar.FindAllStringSubmatch(css, -1) {
			used[m[1]] = true
		}
	}
	checked := map[string]bool{}
	for _, p := range pairs() {
		checked[p.fg] = true
	}
	for token := range used {
		if !checked[token] {
			t.Errorf("--%s is used as a text colour and is in no pair of TestContrast", token)
		}
	}
	if len(used) < 8 {
		t.Errorf("found only %d text tokens in the stylesheets, the pattern is probably wrong", len(used))
	}
}

// TestNoColourOutsideTheTokenBlocks checks that app.css writes no colour in
// its rules: every colour is a custom property set in one of the token blocks.
func TestNoColourOutsideTheTokenBlocks(t *testing.T) {
	_, _, _, rest := palettes(t)
	for _, m := range reDecl.FindAllStringSubmatch(rest, -1) {
		// Declarations only: a selector such as #feed-empty is not a colour.
		if c := reAnyColor.FindString(m[2]); c != "" {
			t.Errorf("app.css writes the colour %q in %s outside the token blocks", c, m[1])
		}
		for _, word := range regexp.MustCompile(`[a-zA-Z]+`).FindAllString(m[2], -1) {
			if slices.Contains(namedColours, strings.ToLower(word)) {
				t.Errorf("app.css writes the colour name %q in %s: %s", word, m[1], strings.TrimSpace(m[2]))
			}
		}
	}
}

// TestCodeCSSHasNoColour checks that code.css has tokens only.
func TestCodeCSSHasNoColour(t *testing.T) {
	css := readCSS(t, "code.css")
	if m := reAnyColor.FindString(css); m != "" {
		t.Errorf("code.css writes the colour %q", m)
	}
	light, _, _, _ := palettes(t)
	for _, m := range regexp.MustCompile(`var\(--([a-z0-9-]+)`).FindAllStringSubmatch(css, -1) {
		if _, ok := light[m[1]]; !ok {
			t.Errorf("code.css uses --%s, which app.css does not define", m[1])
		}
	}
}

// TestFontFaces checks that every font that @font-face names is in the
// embedded files, and that the stylesheet asks for no other site.
func TestFontFaces(t *testing.T) {
	css := readCSS(t, "app.css")
	urls := reFontURL.FindAllStringSubmatch(css, -1)
	if len(urls) != 6 {
		t.Errorf("@font-face urls = %d, want 6", len(urls))
	}
	for _, m := range urls {
		if _, err := static.FS.ReadFile(m[1]); err != nil {
			t.Errorf("@font-face names /static/%s: %v", m[1], err)
		}
	}
	if strings.Contains(css, "@import") || strings.Contains(css, "http://") || strings.Contains(css, "https://") {
		t.Errorf("app.css asks for a file of another site")
	}
	if n := strings.Count(css, "font-display: swap"); n != 6 {
		t.Errorf("font-display: swap appears %d times, want 6", n)
	}
}

// TestAccessibilityRules checks that the stylesheet has a visible focus ring
// and turns motion off for people who ask for it.
func TestAccessibilityRules(t *testing.T) {
	css := readCSS(t, "app.css")
	for _, want := range []string{":focus-visible {", "outline: 2px solid var(--c-focus)", "@media (prefers-reduced-motion: reduce)", "transition-duration: 0.01ms"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
}
