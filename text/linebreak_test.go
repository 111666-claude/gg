package text

import (
	"strings"
	"testing"
)

// breakPositions returns the rune indices where a break is allowed or mandatory.
func breakPositions(breaks []BreakOpportunity) (allowed, mandatory []int) {
	for i, b := range breaks {
		switch b {
		case BreakAllowed:
			allowed = append(allowed, i)
		case BreakMandatory:
			mandatory = append(mandatory, i)
		}
	}
	return allowed, mandatory
}

// assertBreaks checks the exact allowed/mandatory break positions for text.
func assertBreaks(t *testing.T, text string, wantAllowed, wantMandatory []int) {
	t.Helper()
	breaks := BreakOpportunities(text)
	runes := []rune(text)
	if len(breaks) != len(runes) {
		t.Fatalf("BreakOpportunities(%q): got %d entries, want %d (one per rune)",
			text, len(breaks), len(runes))
	}
	if len(breaks) > 0 && breaks[0] != BreakNo {
		t.Errorf("BreakOpportunities(%q): position 0 must be BreakNo (LB2), got %v", text, breaks[0])
	}
	gotAllowed, gotMandatory := breakPositions(breaks)
	if !intsEqual(gotAllowed, wantAllowed) {
		t.Errorf("BreakOpportunities(%q) allowed = %v, want %v", text, gotAllowed, wantAllowed)
	}
	if !intsEqual(gotMandatory, wantMandatory) {
		t.Errorf("BreakOpportunities(%q) mandatory = %v, want %v", text, gotMandatory, wantMandatory)
	}
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBreakOpportunities_Latin covers basic English word wrapping rules:
// breaks after spaces and hyphens, never inside words or numbers.
func TestBreakOpportunities_Latin(t *testing.T) {
	assertBreaks(t, "hello world", []int{6}, nil)
	assertBreaks(t, "The quick brown fox", []int{4, 10, 16}, nil)
	assertBreaks(t, "abc", nil, nil)         // no break inside a word
	assertBreaks(t, "a-b", []int{2}, nil)    // break after hyphen
	assertBreaks(t, "don't", nil, nil)       // apostrophe keeps word together
	assertBreaks(t, "a,b", nil, nil)         // no break around infix comma
	assertBreaks(t, "hi!", nil, nil)         // no break before exclamation
	assertBreaks(t, "x—y", []int{1, 2}, nil) // em dash breaks on both sides
	assertBreaks(t, "a…b", []int{2}, nil)    // no break before ellipsis; break after
}

// TestBreakOpportunities_Punctuation covers open/close and quote rules.
func TestBreakOpportunities_Punctuation(t *testing.T) {
	assertBreaks(t, "(abc)", nil, nil) // no break after '(' or before ')'
	assertBreaks(t, "a (b) c", []int{2, 6}, nil)
	assertBreaks(t, "say “hi” ok", []int{9}, nil) // × QU: no break before quote
	assertBreaks(t, "a.b", nil, nil)              // infix period: no breaks
	assertBreaks(t, "a; b", []int{3}, nil)        // break after space only
}

// TestBreakOpportunities_Numbers covers numeric runs (UAX #14 LB25):
// ASCII and Arabic-Indic digits, decimal separators, percent and currency.
func TestBreakOpportunities_Numbers(t *testing.T) {
	assertBreaks(t, "123456", nil, nil)                // digits stay together
	assertBreaks(t, "3.14", nil, nil)                  // decimal point
	assertBreaks(t, "1,000,000", nil, nil)             // thousands separators
	assertBreaks(t, "50%", nil, nil)                   // postfix percent
	assertBreaks(t, "$100", nil, nil)                  // prefix currency
	assertBreaks(t, "١٢٣٤٥", nil, nil)                 // Arabic-Indic digits stay together
	assertBreaks(t, "العدد ٤٢ صحيح", []int{6, 9}, nil) // breaks after spaces only
	assertBreaks(t, "a1b2", nil, nil)                  // LB23: letters and digits glue
	assertBreaks(t, "x 42 y", []int{2, 5}, nil)
}

// TestBreakOpportunities_CJK covers ideographic breaking and kinsoku
// nonstarter rules, plus Chinese-English mixed text.
func TestBreakOpportunities_CJK(t *testing.T) {
	assertBreaks(t, "中文", []int{1}, nil)         // break between ideographs
	assertBreaks(t, "中文测试", []int{1, 2, 3}, nil) // break around every ideograph
	assertBreaks(t, "hello世界", []int{5, 6}, nil) // mixed Latin/CJK
	assertBreaks(t, "世界hello", []int{1, 2}, nil) // CJK then Latin
	assertBreaks(t, "你好world你好", []int{1, 2, 7, 8}, nil)
	assertBreaks(t, "ほらぁ", []int{1}, nil)        // small kana ぁ is a nonstarter
	assertBreaks(t, "カタカナ", []int{1, 2, 3}, nil) // katakana breaks freely
	assertBreaks(t, "한글한글", []int{1, 2, 3}, nil) // hangul syllables
}

// TestBreakOpportunities_Emoji verifies that emoji grapheme clusters are
// never split: ZWJ sequences, flag pairs, keycaps, and skin tones.
func TestBreakOpportunities_Emoji(t *testing.T) {
	// Family emoji: 👨 ZWJ 👩 ZWJ 👧 — no breaks anywhere inside.
	assertBreaks(t, "👨‍👩‍👧", nil, nil)
	// Two families: break only between the clusters.
	assertBreaks(t, "👨‍👩‍👧👨‍👩‍👧", []int{5}, nil)
	// Single flag: no break. Two flags: break between pairs only.
	assertBreaks(t, "🇦🇧", nil, nil)
	assertBreaks(t, "🇦🇧🇨🇩", []int{2}, nil)
	assertBreaks(t, "🇦🇧🇨🇩🇪🇫", []int{2, 4}, nil)
	// Keycap sequence: 1 VS16 U+20E3 — no breaks.
	assertBreaks(t, "1️⃣", nil, nil)
	// Skin tone modifier glues to its base.
	assertBreaks(t, "👍🏽", nil, nil)
	// Breaks between separate emoji are allowed (ID ÷ ID).
	assertBreaks(t, "😀😀😀", []int{1, 2}, nil)
	// Emoji mixed with text.
	assertBreaks(t, "hi 😀😀 yo", []int{3, 4, 6}, nil)
}

// TestBreakOpportunities_Combining covers combining marks and ZWJ control.
func TestBreakOpportunities_Combining(t *testing.T) {
	assertBreaks(t, "éx", nil, nil)  // é as e + combining acute
	assertBreaks(t, "á̂b", nil, nil) // stacked marks
	assertBreaks(t, "x é y", []int{2, 5}, nil)

	// Extremely long combining mark run: no breaks, no state blowup.
	var sb strings.Builder
	sb.WriteString("a")
	for i := 0; i < 10000; i++ {
		sb.WriteString("́")
	}
	sb.WriteString("b")
	assertBreaks(t, sb.String(), nil, nil)
}

// TestBreakOpportunities_HardBreaks covers CR, LF, CRLF, NEL and friends.
func TestBreakOpportunities_HardBreaks(t *testing.T) {
	// LF: mandatory break after.
	assertBreaks(t, "a\nb", nil, []int{2})
	// CR alone: mandatory break after.
	assertBreaks(t, "a\rb", nil, []int{2})
	// CRLF: no break between CR and LF, mandatory after the pair.
	assertBreaks(t, "a\r\nb", nil, []int{3})
	// NEL and paragraph separator.
	assertBreaks(t, "a\u0085b", nil, []int{2})
	assertBreaks(t, "a\u2029b", nil, []int{2})
	// Multiple paragraphs.
	assertBreaks(t, "ab\r\ncd\nef", nil, []int{4, 7})
	// No break before a hard break control itself (LB6).
	breaks := BreakOpportunities("ab\ncd")
	if breaks[2] != BreakNo {
		t.Errorf("break before LF should be BreakNo, got %v", breaks[2])
	}
}

// TestBreakOpportunities_NoBreakSpaces covers NBSP, word joiner and ZWSP.
func TestBreakOpportunities_NoBreakSpaces(t *testing.T) {
	assertBreaks(t, "a\u00A0b", nil, nil) // NBSP glues both sides
	assertBreaks(t, "a b\u00A0\u00A0c", []int{2}, nil)
	assertBreaks(t, "a\u2060b", nil, nil)      // word joiner glues
	assertBreaks(t, "a\u200Bb", []int{2}, nil) // ZWSP: break after
	assertBreaks(t, "a\u200B\u200Bb", []int{3}, nil)
	assertBreaks(t, "x\u200B y", []int{3}, nil) // ZW SP* ÷: break lands after spaces
}

// TestBreakOpportunities_InvalidUTF8 verifies stable handling of invalid
// UTF-8: replacement characters are ordinary (AL) characters, no panic.
func TestBreakOpportunities_InvalidUTF8(t *testing.T) {
	texts := []string{
		"a\xffb",
		"\xff\xfe",
		"hello \xc3 world", // truncated 2-byte sequence
		"\xed\xa0\x80",     // surrogate halves
		"a\xf0\x9f\x98",    // truncated emoji
	}
	for _, s := range texts {
		breaks := BreakOpportunities(s)
		want := len([]rune(s))
		if len(breaks) != want {
			t.Errorf("BreakOpportunities(%q): got %d entries, want %d", s, len(breaks), want)
		}
		if len(breaks) > 0 && breaks[0] != BreakNo {
			t.Errorf("BreakOpportunities(%q): position 0 must be BreakNo", s)
		}
	}
}

// TestBreakOpportunities_GraphemeSafe is a property test: every reported
// break position must coincide with a grapheme cluster boundary.
func TestBreakOpportunities_GraphemeSafe(t *testing.T) {
	corpus := []string{
		"👨‍👩‍👧 family 🇦🇧 flag 1️⃣ keycap",
		"é̂ marks \u200D zwj",
		"中文 english 混排 test ١٢٣",
		"👍🏽👍🏽👍🏽 and 🇦🇧🇨🇩🇪🇫🇬🇭 flags",
		"a\u00A0b\u200Bc\u2060d",
		"(paren) “quote” 3.14 50% $9",
		"ほらぁ small カタカナ kana",
		"line1\r\nline2\nline3\rend",
	}
	for _, text := range corpus {
		runes := []rune(text)
		breaks := BreakOpportunities(text)
		clusters := graphemeBreaks(runes)
		for i := 1; i < len(runes); i++ {
			if breaks[i] != BreakNo && !clusters[i] {
				t.Errorf("BreakOpportunities(%q): break at rune %d splits a grapheme cluster", text, i)
			}
		}
	}
}

// TestBreakOpportunities_HugeText verifies stable, single-pass handling of
// very large inputs.
func TestBreakOpportunities_HugeText(t *testing.T) {
	text := strings.Repeat("The quick brown fox. 中文混排 😀👍 ", 20000)
	breaks := BreakOpportunities(text)
	if len(breaks) != len([]rune(text)) {
		t.Fatalf("got %d breaks, want %d", len(breaks), len([]rune(text)))
	}
	// Spot-check: break after every space before "The".
	runes := []rune(text)
	if breaks[1] != BreakNo {
		t.Error("no break expected inside word 'The'")
	}
	spaceIdx := -1
	for i, r := range runes {
		if r == ' ' {
			spaceIdx = i
			break
		}
	}
	if spaceIdx < 0 || breaks[spaceIdx+1] != BreakAllowed {
		t.Errorf("expected break after first space (rune %d)", spaceIdx)
	}
}

// TestFindBreakOpportunities_Modes verifies that all wrap modes reuse the
// same UAX #14 engine with mode-appropriate filtering.
func TestFindBreakOpportunities_Modes(t *testing.T) {
	// Emoji ZWJ sequence: WrapChar must not split the cluster.
	family := "👨‍👩‍👧👨‍👩‍👧"
	breaks := findBreakOpportunities(family, WrapChar)
	allowed, _ := breakPositions(breaks)
	if !intsEqual(allowed, []int{5}) {
		t.Errorf("WrapChar on ZWJ emoji: allowed = %v, want [5]", allowed)
	}

	// WrapChar on plain text: every boundary allowed.
	breaks = findBreakOpportunities("abc", WrapChar)
	allowed, _ = breakPositions(breaks)
	if !intsEqual(allowed, []int{1, 2}) {
		t.Errorf("WrapChar on abc: allowed = %v, want [1 2]", allowed)
	}

	// WrapWord and WrapWordChar share UAX #14 word opportunities.
	for _, mode := range []WrapMode{WrapWord, WrapWordChar} {
		breaks = findBreakOpportunities("hello world", mode)
		allowed, _ = breakPositions(breaks)
		if !intsEqual(allowed, []int{6}) {
			t.Errorf("%v on 'hello world': allowed = %v, want [6]", mode, allowed)
		}
	}

	// Mandatory breaks are visible to word modes too.
	breaks = findBreakOpportunities("a\nb", WrapWord)
	_, mandatory := breakPositions(breaks)
	if !intsEqual(mandatory, []int{2}) {
		t.Errorf("WrapWord mandatory = %v, want [2]", mandatory)
	}
}

// TestWrapText_CRLF verifies explicit newlines (CRLF, CR, LF) produce
// separate lines through the shared paragraph handling.
func TestWrapText_CRLF(t *testing.T) {
	face := wrapTestFace(t)
	tests := []struct {
		name  string
		text  string
		lines []string
	}{
		{"LF", "one\ntwo", []string{"one", "two"}},
		{"CRLF", "one\r\ntwo", []string{"one", "two"}},
		{"CR", "one\rtwo", []string{"one", "two"}},
		{"blank line", "one\n\nthree", []string{"one", "", "three"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := WrapText(tt.text, face, 1000, WrapWord)
			if len(results) != len(tt.lines) {
				t.Fatalf("WrapText(%q) produced %d lines, want %d: %v",
					tt.text, len(results), len(tt.lines), results)
			}
			for i, want := range tt.lines {
				if results[i].Text != want {
					t.Errorf("line %d = %q, want %q", i, results[i].Text, want)
				}
			}
		})
	}
}

// TestWrapText_EmojiNotSplit verifies that wrapping never splits emoji
// grapheme clusters, in both WrapChar and WrapWordChar modes.
func TestWrapText_EmojiNotSplit(t *testing.T) {
	face := wrapTestFace(t)

	// Width of a single emoji cluster as measured by the wrapper.
	single := measureRune('😀', face)
	if single <= 0 {
		t.Skip("test font cannot measure emoji")
	}

	text := "😀😀😀😀😀"
	for _, mode := range []WrapMode{WrapChar, WrapWordChar} {
		results := WrapText(text, face, single*1.5, mode)
		if len(results) != 5 {
			t.Fatalf("%v: got %d lines, want 5 (one emoji each): %q",
				mode, len(results), results)
		}
		for i, r := range results {
			if r.Text != "😀" {
				t.Errorf("%v: line %d = %q, want a single whole emoji", mode, i, r.Text)
			}
		}
	}
}

// TestWrapText_ZWJEmojiNotSplit verifies ZWJ emoji sequences stay whole
// even when the character fallback triggers.
func TestWrapText_ZWJEmojiNotSplit(t *testing.T) {
	face := wrapTestFace(t)

	family := "👨‍👩‍👧"
	var clusterWidth float64
	for _, r := range family {
		clusterWidth += measureRune(r, face)
	}
	if clusterWidth <= 0 {
		t.Skip("test font cannot measure emoji")
	}

	text := family + family + family
	for _, mode := range []WrapMode{WrapChar, WrapWordChar} {
		results := WrapText(text, face, clusterWidth*1.5, mode)
		if len(results) != 3 {
			t.Fatalf("%v: got %d lines, want 3: %q", mode, len(results), results)
		}
		for i, r := range results {
			if r.Text != family {
				t.Errorf("%v: line %d = %q, want whole ZWJ sequence %q", mode, i, r.Text, family)
			}
		}
	}
}

// TestWrapText_FlagNotSplit verifies regional indicator flag pairs are
// never split across lines.
func TestWrapText_FlagNotSplit(t *testing.T) {
	face := wrapTestFace(t)

	riWidth := measureRune('🇦', face)
	if riWidth <= 0 {
		t.Skip("test font cannot measure regional indicators")
	}

	// Width fits one flag (2 RIs) but not two.
	text := "🇦🇧🇨🇩"
	results := WrapText(text, face, riWidth*2.5, WrapChar)
	if len(results) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(results), results)
	}
	if results[0].Text != "🇦🇧" || results[1].Text != "🇨🇩" {
		t.Errorf("lines = %q, want [🇦🇧 🇨🇩] (flags must stay whole)", results)
	}
}

// TestWrapText_CJKMixed verifies Chinese-English mixed wrapping uses
// UAX #14 opportunities and preserves the full text.
func TestWrapText_CJKMixed(t *testing.T) {
	face := wrapTestFace(t)
	text := "你好world这是test"

	results := WrapText(text, face, 60, WrapWord)
	if len(results) < 2 {
		t.Fatalf("mixed CJK/Latin text should wrap, got %d lines", len(results))
	}

	// Reassembled text must match the original (no spaces to trim here).
	var combined strings.Builder
	for _, r := range results {
		combined.WriteString(r.Text)
	}
	if combined.String() != text {
		t.Errorf("reassembled = %q, want %q", combined.String(), text)
	}

	// Every line boundary must be a UAX #14 break opportunity.
	breaks := BreakOpportunities(text)
	runes := []rune(text)
	for _, r := range results[1:] {
		// Find rune index of line start.
		runeIdx := len([]rune(text[:r.Start]))
		if runeIdx > 0 && runeIdx < len(runes) && breaks[runeIdx] == BreakNo {
			t.Errorf("line break at rune %d (%q) is not a UAX #14 opportunity", runeIdx, r.Text)
		}
	}
}

// TestWrapText_LongNumber verifies digit runs are not split in word mode.
func TestWrapText_LongNumber(t *testing.T) {
	face := wrapTestFace(t)
	text := "total 1234567890 end"

	results := WrapText(text, face, 120, WrapWord)

	// The number must appear whole on a single line.
	found := false
	for _, r := range results {
		if strings.Contains(r.Text, "1234567890") {
			found = true
		} else if strings.ContainsAny(r.Text, "0123456789") {
			t.Errorf("digit run split across lines: %q", r.Text)
		}
	}
	if !found {
		t.Error("number not found in wrapped output")
	}
}

// TestWrapText_LineWidths verifies wrapped lines respect maxWidth
// according to the same per-rune measurement the wrapper uses.
func TestWrapText_LineWidths(t *testing.T) {
	face := wrapTestFace(t)
	text := "The quick brown fox jumps over the lazy dog"
	const maxWidth = 100.0

	results := WrapText(text, face, maxWidth, WrapWord)
	if len(results) < 2 {
		t.Fatalf("expected wrapping at width %v, got 1 line", maxWidth)
	}
	for i, r := range results {
		var w float64
		for _, rn := range r.Text {
			w += measureRune(rn, face)
		}
		if w > maxWidth*1.001 {
			t.Errorf("line %d %q measures %v, exceeds maxWidth %v", i, r.Text, w, maxWidth)
		}
	}
}

// TestWrapText_InvalidUTF8 verifies wrapping survives invalid UTF-8.
func TestWrapText_InvalidUTF8(t *testing.T) {
	face := wrapTestFace(t)
	text := "hello \xff\xfe world"

	results := WrapText(text, face, 60, WrapWordChar)
	if len(results) == 0 {
		t.Fatal("expected results for invalid UTF-8 input")
	}
	// All result offsets must stay within the original text bounds.
	for _, r := range results {
		if r.Start < 0 || r.End > len(text) || r.Start > r.End {
			t.Errorf("invalid offsets [%d,%d) for text of %d bytes", r.Start, r.End, len(text))
		}
	}
}

// BenchmarkBreakOpportunities benchmarks the UAX #14 engine.
func BenchmarkBreakOpportunities(b *testing.B) {
	text := "The quick brown fox jumps over the lazy dog. 中文混排 😀👍🇦🇧 "

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BreakOpportunities(text)
	}
}

// BenchmarkBreakOpportunities_Long benchmarks the engine on large input.
func BenchmarkBreakOpportunities_Long(b *testing.B) {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BreakOpportunities(text)
	}
}
