package svg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gogpu/gg/text"
)

func (r *systemResolver) ResolveFonts(q FontQuery) []text.Face {
	size := q.Size
	if size <= 0 || size > 10000 {
		size = 16
	}
	italic := q.Italic
	bold := q.Weight >= 600

	var paths []string

	families := q.Families
	if q.Family != "" {
		families = append([]string{normalizeFamily(q.Family)}, families...)
	}
	joined := strings.Join(families, " ")

	switch {
	case strings.Contains(joined, "wenquanyi") || strings.Contains(joined, "wqy") ||
		strings.Contains(joined, "cjk") || strings.Contains(joined, "han") ||
		strings.Contains(joined, "hei") || strings.Contains(joined, "song") ||
		strings.Contains(joined, "ming") || strings.Contains(joined, "pingfang") ||
		strings.Contains(joined, "hiragino"):
		paths = append(paths, cJKFontPaths()...)
	case strings.Contains(joined, "mono"):
		paths = append(paths, monoFontPaths(bold, italic)...)
	case strings.Contains(joined, "serif") && !strings.Contains(joined, "sans"):
		paths = append(paths, serifFontPaths(bold, italic)...)
	default:
		paths = append(paths, sansFontPaths(bold, italic)...)
	}

	// Script coverage fallbacks. DejaVu covers Arabic and many other scripts;
	// WenQuanYi/Noto cover CJK. Keeping these after the requested family lets
	// Latin use the intended metrics while missing runes still render.
	paths = append(paths,
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
	)
	paths = append(paths, cJKFontPaths()...)
	if runtime.GOOS == "darwin" {
		paths = append(paths,
			"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
			"/System/Library/Fonts/PingFang.ttc",
		)
	}
	if runtime.GOOS == "windows" {
		win := os.Getenv("WINDIR")
		if win == "" {
			win = `C:\Windows`
		}
		paths = append(paths,
			filepath.Join(win, "Fonts", "msyh.ttc"),
			filepath.Join(win, "Fonts", "simsun.ttc"),
		)
	}

	seen := make(map[string]bool)
	faces := make([]text.Face, 0, 4)
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		source := r.source(path)
		if source == nil {
			continue
		}
		faces = append(faces, source.Face(size, text.WithLanguage(orDefaultLang(q.Language))))
	}
	return faces
}

func orDefaultLang(lang string) string {
	if lang == "" {
		return "en"
	}
	return lang
}

func sansFontPaths(bold, italic bool) []string {
	home, _ := os.UserHomeDir()
	ubuntu := "/usr/share/fonts/truetype/ubuntu/UbuntuSans[wdth,wght].ttf"
	dejavu := "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
	dejavuBold := "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"
	if italic && bold {
		return []string{
			"/usr/share/fonts/truetype/dejavu/DejaVuSans-BoldOblique.ttf",
			filepath.Join(home, ".local/fonts/DejaVuSans-BoldOblique.ttf"),
		}
	}
	if bold {
		return []string{dejavuBold, ubuntu}
	}
	if italic {
		return []string{
			"/usr/share/fonts/truetype/dejavu/DejaVuSans-Oblique.ttf",
			ubuntu,
			dejavu,
		}
	}
	return []string{dejavu, ubuntu}
}

func serifFontPaths(bold, italic bool) []string {
	if bold {
		return []string{"/usr/share/fonts/truetype/dejavu/DejaVuSerif-Bold.ttf"}
	}
	if italic {
		return []string{"/usr/share/fonts/truetype/dejavu/DejaVuSerif-Italic.ttf"}
	}
	return []string{"/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf"}
}

func monoFontPaths(bold, italic bool) []string {
	if bold {
		return []string{"/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"}
	}
	if italic {
		return []string{"/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Oblique.ttf"}
	}
	return []string{"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"}
}

func cJKFontPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".local", "share", "fonts", "wqy-zenhei.ttc"),
		"/home/real666/.local/share/fonts/wqy-zenhei.ttc",
		"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/opentype/noto/NotoSansCJKsc-Regular.otf",
		"/usr/share/fonts/opentype/noto/NotoSerifCJK-Regular.ttc",
		"/System/Library/Fonts/PingFang.ttc",
		"/System/Library/Fonts/STHeiti Light.ttc",
		`C:\Windows\Fonts\msyh.ttc`,
		`C:\Windows\Fonts\simsun.ttc`,
	}
}

func resizeFace(face text.Face, size float64, lang string, rtl bool) text.Face {
	if face == nil {
		return nil
	}
	source := face.Source()
	if source == nil || size <= 0 {
		return face
	}
	opts := []text.FaceOption{text.WithLanguage(orDefaultLang(lang))}
	if rtl {
		opts = append(opts, text.WithDirection(text.DirectionRTL))
	}
	if feats := face.Features(); len(feats) > 0 {
		opts = append(opts, text.WithFeatures(feats...))
	}
	if vars := face.Variations(); len(vars) > 0 {
		opts = append(opts, text.WithVariations(vars...))
	}
	return source.Face(size, opts...)
}

func uniqueFaces(faces []text.Face) []text.Face {
	out := make([]text.Face, 0, len(faces))
	seenSources := make(map[*text.FontSource]bool)
	for _, face := range faces {
		if face == nil {
			continue
		}
		if src := face.Source(); src != nil {
			if seenSources[src] {
				continue
			}
			seenSources[src] = true
		}
		out = append(out, face)
	}
	return out
}

func (d *Document) resolveFaces(fs fontState, lang string) []text.Face {
	q := FontQuery{
		Family:   fs.family,
		Families: parseFontFamilies(fs.family),
		Weight:   fs.weight,
		Italic:   fs.italic,
		Size:     fs.size,
		Language: lang,
	}

	var faces []text.Face
	if d != nil {
		if d.Font != nil {
			faces = append(faces, d.Font)
		}
		if d.TextFont != nil {
			faces = append(faces, d.TextFont)
		}
		if d.FontFace != nil && d.FontFace != d.Font {
			faces = append(faces, d.FontFace)
		}
		if Font != nil {
			faces = append(faces, Font)
		}
		if TextFont != nil {
			faces = append(faces, TextFont)
		}
		if DefaultFont != nil {
			faces = append(faces, DefaultFont)
		}
		if DefaultFontFace != nil && DefaultFontFace != DefaultFont {
			faces = append(faces, DefaultFontFace)
		}
		if DefaultTextFont != nil && DefaultTextFont != DefaultFont {
			faces = append(faces, DefaultTextFont)
		}
		if d.NamedFonts != nil {
			for _, family := range q.Families {
				if face := d.NamedFonts[family]; face != nil {
					faces = append(faces, face)
				}
			}
		}
		if d.FontResolver != nil {
			faces = append(faces, d.FontResolver.ResolveFonts(q)...)
			if simple, ok := d.FontResolver.(SimpleFontResolver); ok {
				face, err := simple.ResolveFont(q.Family, q.Size)
				if err == nil && face != nil {
					faces = append(faces, face)
				} else if err != nil {
					d.reportTextError(&TextError{Code: ErrExternalFont, Err: err, Text: q.Family})
				}
			}
		} else if DefaultFontResolver != nil {
			faces = append(faces, DefaultFontResolver.ResolveFonts(q)...)
		}
		faces = append(faces, d.FallbackFonts...)
		if !d.TextOptions.DisableSystemFonts {
			faces = append(faces, defaultSystemResolver().ResolveFonts(q)...)
		}
	}

	rtl := lang == "ar" || lang == "he" || strings.HasPrefix(lang, "ar-") || strings.HasPrefix(lang, "he-")
	for i := range faces {
		faces[i] = resizeFace(faces[i], fs.size, lang, rtl)
	}
	faces = uniqueFaces(faces)
	if len(faces) == 0 {
		d.reportTextError(&TextError{Code: ErrNoFont, Text: q.Family})
	}
	return faces
}
