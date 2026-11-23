package text

import (
	"unicode"
)

// This file implements a reusable Unicode line break opportunity calculator
// based on UAX #14 (Unicode Line Breaking Algorithm), layered on top of
// grapheme cluster segmentation (UAX #29) so that breaks never split a
// visual character: emoji ZWJ sequences, flag pairs, keycap sequences,
// and combining mark chains stay intact.
//
// The single exported entry point is BreakOpportunities. All wrap modes
// (WrapWord, WrapChar, WrapWordChar) and explicit newline handling reuse
// the same rule engine; the modes only filter the resulting opportunities.
//
// Complexity: O(n) time and O(n) memory (one output slice per call), with
// a single forward pass and O(1) state — no recursion and no unbounded
// lookbehind, so arbitrarily long combining mark runs or huge inputs are
// handled with bounded memory. Invalid UTF-8 decodes to U+FFFD (an ordinary
// AL-class character) and cannot cause a failure.

// lineClass is a UAX #14 line breaking class (practical subset).
type lineClass uint8

const (
	clsAL  lineClass = iota // alphabetic / default
	clsBK                   // mandatory break (NEL, FF, VT, PS, LS)
	clsCR                   // carriage return
	clsLF                   // line feed
	clsCM                   // combining mark (inherits base class, LB9/LB10)
	clsSP                   // space
	clsZW                   // zero-width space
	clsZWJ                  // zero-width joiner
	clsWJ                   // word joiner
	clsGL                   // non-breaking glue (NBSP, figure space, ...)
	clsOP                   // opening punctuation
	clsCL                   // closing punctuation (CL and CP merged)
	clsEX                   // exclamation / interrogation
	clsIS                   // infix separator (, . : ;)
	clsNS                   // nonstarter (small kana, iteration marks)
	clsSY                   // symbols allowing break after (/)
	clsHY                   // hyphen
	clsBA                   // break after (dashes, TAB-like)
	clsNU                   // numeric
	clsPR                   // numeric prefix ($, £, €, +)
	clsPO                   // numeric postfix (%, ‰)
	clsID                   // ideographic / emoji (break either side)
	clsIN                   // inseparable (…)
	clsQU                   // quotation marks
	clsRI                   // regional indicator (flags)
	clsB2                   // break opportunity before and after (em dash)
)

// classifyLineBreak returns the UAX #14 line breaking class of a rune.
func classifyLineBreak(r rune) lineClass {
	if r < 0x80 {
		return classifyASCII(r)
	}
	return classifyNonASCII(r)
}

// classifyASCII classifies ASCII runes.
func classifyASCII(r rune) lineClass {
	switch r {
	case '\n':
		return clsLF
	case '\r':
		return clsCR
	case '\v', '\f':
		return clsBK
	case '\t', ' ':
		return clsSP
	case '!', '?':
		return clsEX
	case '"', '\'':
		return clsQU
	case '(', '[', '{':
		return clsOP
	case ')', ']', '}':
		return clsCL
	case ',', '.', ':', ';':
		return clsIS
	case '-':
		return clsHY
	case '/':
		return clsSY
	case '%':
		return clsPO
	case '$', '+':
		return clsPR
	}
	if r >= '0' && r <= '9' {
		return clsNU
	}
	return clsAL
}

// classifyNonASCII classifies runes at or above U+0080.
func classifyNonASCII(r rune) lineClass {
	// Specific characters first (ordered by code point where practical).
	if c, ok := classifySpecial(r); ok {
		return c
	}
	if isQuotationPunct(r) {
		return clsQU
	}
	// Combining marks, variation selectors, tag characters, and emoji
	// skin tone modifiers all extend the previous grapheme cluster.
	if isGraphemeExtend(r) {
		return clsCM
	}
	if r >= 0x1F1E6 && r <= 0x1F1FF { // regional indicators
		return clsRI
	}
	if unicode.IsDigit(r) { // includes Arabic-Indic, fullwidth, Devanagari digits
		return clsNU
	}
	if unicode.Is(unicode.Sc, r) { // currency symbols (£ ¥ € ₹ ...)
		return clsPR
	}
	if isNonstarter(r) {
		return clsNS
	}
	if IsCJKRune(r) || isEmojiBase(r) {
		return clsID
	}
	if unicode.Is(unicode.Ps, r) {
		return clsOP
	}
	if unicode.Is(unicode.Pe, r) {
		return clsCL
	}
	return clsAL
}

// classifySpecial handles specific code points with dedicated UAX #14 classes.
func classifySpecial(r rune) (lineClass, bool) {
	switch r {
	case 0x0085: // NEL
		return clsBK, true
	case 0x00A0, 0x2007, 0x202F: // NBSP, figure space, narrow NBSP
		return clsGL, true
	case 0x00AD: // soft hyphen
		return clsBA, true
	case 0x200B: // zero-width space
		return clsZW, true
	case 0x200D: // zero-width joiner
		return clsZWJ, true
	case 0x2010: // hyphen
		return clsHY, true
	case 0x2011: // non-breaking hyphen
		return clsGL, true
	case 0x2012, 0x2013: // figure dash, en dash
		return clsBA, true
	case 0x2014, 0x2015: // em dash, horizontal bar
		return clsB2, true
	case 0x2028, 0x2029: // line/paragraph separator
		return clsBK, true
	case 0x2026: // horizontal ellipsis
		return clsIN, true
	case 0x2030, 0x2031: // per mille / per ten thousand
		return clsPO, true
	case 0x2060, 0xFEFF: // word joiner, zero-width no-break space
		return clsWJ, true
	case 0x3000: // ideographic space
		return clsBA, true
	}
	return clsAL, false
}

// isQuotationPunct reports whether r is a quotation mark treated as QU.
func isQuotationPunct(r rune) bool {
	switch r {
	case 0x00AB, 0x00BB, // « »
		0x2018, 0x2019, 0x201A, 0x201B, // single quotes
		0x201C, 0x201D, 0x201E, 0x201F, // double quotes
		0x2039, 0x203A: // single guillemets
		return true
	}
	return false
}

// isNonstarter reports whether r is a kinsoku nonstarter (NS): small kana,
// iteration marks, and halfwidth kana that may not start a line.
func isNonstarter(r rune) bool {
	switch r {
	case 0x3041, 0x3043, 0x3045, 0x3047, 0x3049, 0x3063, // small hiragana
		0x3083, 0x3085, 0x3087, 0x308E, 0x3095, 0x3096,
		0x30A1, 0x30A3, 0x30A5, 0x30A7, 0x30A9, 0x30C3, // small katakana
		0x30E3, 0x30E5, 0x30E7, 0x30EE, 0x30F5, 0x30F6,
		0x3005, 0x303B, 0x309D, 0x309E, 0x30FD, 0x30FE: // iteration marks
		return true
	}
	return (r >= 0xFF67 && r <= 0xFF6F) || // halfwidth small kana
		(r >= 0x31F0 && r <= 0x31FF) // katakana phonetic extensions
}

// isEmojiBase reports whether r is an emoji base character. Emoji are
// treated as ideographic (ID) for line breaking, matching UAX #14.
func isEmojiBase(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || // emoji blocks (RI/modifiers checked earlier)
		(r >= 0x2600 && r <= 0x27BF) || // misc symbols + dingbats
		(r >= 0x2B00 && r <= 0x2BFF) || // supplemental arrows-B / stars
		r == 0x00A9 || r == 0x00AE // © ®
}

// isExtendedPictographic reports whether r can terminate an emoji ZWJ
// sequence (UAX #29 GB11).
func isExtendedPictographic(r rune) bool {
	if isEmojiBase(r) {
		return true
	}
	switch r {
	case 0x203C, 0x2049, 0x2122, 0x2139, 0x231A, 0x231B, 0x2328, 0x23CF,
		0x24C2, 0x25B6, 0x25C0, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return (r >= 0x2194 && r <= 0x2199) ||
		(r >= 0x21A9 && r <= 0x21AA) ||
		(r >= 0x23E9 && r <= 0x23F3) ||
		(r >= 0x23F8 && r <= 0x23FA) ||
		(r >= 0x25AA && r <= 0x25AB) ||
		(r >= 0x25FB && r <= 0x25FE) ||
		(r >= 0x2934 && r <= 0x2935)
}

// isRegionalIndicator reports whether r is a regional indicator (flag letter).
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// isGraphemeExtend reports whether r extends the previous grapheme cluster
// (UAX #29 Extend): combining marks, variation selectors, tag characters,
// and emoji modifiers.
func isGraphemeExtend(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) {
		return true
	}
	return (r >= 0xE0020 && r <= 0xE007F) || // tags
		(r >= 0x1F3FB && r <= 0x1F3FF) // skin tone modifiers
}

// isHardBreakRune reports whether r forces a grapheme/line boundary
// (UAX #29 GB4/GB5 controls).
func isHardBreakRune(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x0085, 0x2028, 0x2029:
		return true
	}
	return false
}

// BreakOpportunities analyzes UTF-8 text and returns grapheme-safe line
// break opportunities following Unicode UAX #14.
//
// The result has one entry per rune: index i describes the boundary BEFORE
// rune i. Index 0 is always BreakNo (LB2: never break at start of text);
// the end of text is an implicit final break (LB3). BreakMandatory marks
// hard breaks after CR, LF, NEL and similar controls (CR LF is treated as
// a single break unit).
//
// Breaks never fall inside a grapheme cluster: combining mark sequences,
// emoji ZWJ sequences, keycap sequences, and regional indicator (flag)
// pairs are kept intact. Invalid UTF-8 bytes decode to U+FFFD and are
// treated as ordinary characters, so the function never fails.
//
// The algorithm is a single forward pass with O(1) state: O(n) time and
// one O(n) output allocation regardless of input content.
func BreakOpportunities(text string) []BreakOpportunity {
	if text == "" {
		return nil
	}
	return computeLineBreaks([]rune(text))
}

// computeLineBreaks runs the UAX #14 pair-table over runes in one pass.
func computeLineBreaks(runes []rune) []BreakOpportunity {
	n := len(runes)
	if n == 0 {
		return nil
	}
	breaks := make([]BreakOpportunity, n)
	breaks[0] = BreakNo // LB2

	prevRaw := classifyLineBreak(runes[0])
	prevEff := prevRaw
	if prevEff == clsCM || prevEff == clsZWJ {
		prevEff = clsAL // LB10: leading combining marks behave as AL
	}
	riRun := 0
	if prevEff == clsRI {
		riRun = 1
	}

	for i := 1; i < n; i++ {
		raw := classifyLineBreak(runes[i])
		eff := raw
		if raw == clsCM || raw == clsZWJ {
			// LB9: combining marks inherit the class of their base.
			// LB10: after spaces or hard breaks they behave as AL.
			switch prevEff {
			case clsSP, clsZW, clsBK, clsCR, clsLF:
				eff = clsAL
			default:
				eff = prevEff
			}
		}

		breaks[i] = pairBreak(prevRaw, prevEff, raw, eff, riRun)

		if eff == clsRI {
			riRun++
		} else {
			riRun = 0
		}
		prevRaw = raw
		prevEff = eff
	}
	return breaks
}

// pairBreak applies the UAX #14 pair rules and returns the break opportunity
// before the current character. prevRaw/curRaw are the raw classes (needed
// for hard break, ZWJ and CM handling); prevEff/curEff are the resolved
// classes after LB9/LB10 combining-mark inheritance. riRun is the number of
// consecutive regional indicators immediately preceding the boundary.
func pairBreak(prevRaw, prevEff, curRaw, curEff lineClass, riRun int) BreakOpportunity {
	if b, done := hardBreakRules(prevRaw, curRaw); done {
		return b
	}
	if noBreakAround(prevEff, curEff) {
		return BreakNo
	}
	// LB18: break after spaces.
	if prevEff == clsSP {
		return BreakAllowed
	}
	// LB21/LB22: never break before hyphens, break-after chars, inseparables.
	switch curEff {
	case clsHY, clsBA, clsIN:
		return BreakNo
	}
	if affinityRules(prevEff, curEff) {
		return BreakNo
	}
	// LB17: never break between two em dashes; otherwise em dash
	// (B2) allows a break on both sides.
	if prevEff == clsB2 && curEff == clsB2 {
		return BreakNo
	}
	if curEff == clsB2 || prevEff == clsB2 {
		return BreakAllowed
	}
	// LB30a: break between regional indicator pairs only (flags stay whole).
	if prevEff == clsRI && curEff == clsRI {
		if riRun%2 == 0 {
			return BreakAllowed
		}
		return BreakNo
	}
	// LB31: break everywhere else (covers ID ÷ ID, ID ÷ AL, emoji, ...).
	return BreakAllowed
}

// hardBreakRules implements the UAX #14 rules that depend on the raw
// (unresolved) classes: LB4–LB9. done is true when a rule matched.
func hardBreakRules(prevRaw, curRaw lineClass) (b BreakOpportunity, done bool) {
	// LB4/LB5: mandatory break after hard break controls; CR × LF.
	if prevRaw == clsCR && curRaw == clsLF {
		return BreakNo, true
	}
	switch prevRaw {
	case clsBK, clsCR, clsLF:
		return BreakMandatory, true
	}
	// LB6/LB7/LB8a/LB9: never break before hard break controls, spaces,
	// zero-width space, zero-width joiners, or combining marks.
	switch curRaw {
	case clsBK, clsCR, clsLF, clsSP, clsZW, clsZWJ, clsCM:
		return BreakNo, true
	}
	// LB8: break after zero-width space (ZW SP* ÷, simplified).
	if prevRaw == clsZW {
		return BreakAllowed, true
	}
	// LB8a: never break after a zero-width joiner.
	if prevRaw == clsZWJ {
		return BreakNo, true
	}
	return BreakNo, false
}

// noBreakAround implements the UAX #14 no-break rules LB11–LB16 over
// resolved classes: word joiners, glue, closers, openers, quotes, and
// nonstarters.
func noBreakAround(prevEff, curEff lineClass) bool {
	// LB11/LB12/LB12a: never break around word joiners or glue (NBSP ...).
	if curEff == clsWJ || prevEff == clsWJ || curEff == clsGL || prevEff == clsGL {
		return true
	}
	// LB13: never break before closers, exclamation, infix, or slash.
	switch curEff {
	case clsCL, clsEX, clsIS, clsSY:
		return true
	}
	// LB14/LB15/LB19/LB16: openers, quotation marks, nonstarters.
	return prevEff == clsOP || curEff == clsQU || prevEff == clsQU || curEff == clsNS
}

// affinityRules implements the UAX #14 no-break rules between letters,
// digits, numeric affixes, and parentheses (LB23–LB30).
func affinityRules(prevEff, curEff lineClass) bool {
	return letterAffixRules(prevEff, curEff) ||
		numericRunRules(prevEff, curEff) ||
		letterPairRules(prevEff, curEff)
}

// isNumAffix reports whether c is a numeric prefix (PR) or postfix (PO).
func isNumAffix(c lineClass) bool {
	return c == clsPR || c == clsPO
}

// letterAffixRules implements LB23, LB23a and LB24: no breaks between
// letters, digits, ideographs and numeric prefix/postfix signs.
func letterAffixRules(prevEff, curEff lineClass) bool {
	// LB23: never break between letters and digits.
	if (prevEff == clsAL && curEff == clsNU) || (prevEff == clsNU && curEff == clsAL) {
		return true
	}
	// LB23a: never break between numeric prefix/postfix and ideographs/emoji.
	if (prevEff == clsPR && curEff == clsID) || (prevEff == clsID && curEff == clsPO) {
		return true
	}
	// LB24: never break between letters and numeric prefix/postfix.
	if (isNumAffix(prevEff) && curEff == clsAL) || (prevEff == clsAL && isNumAffix(curEff)) {
		return true
	}
	return false
}

// numericRunRules implements LB25: numbers, their separators, and their
// prefix/postfix signs stay together.
func numericRunRules(prevEff, curEff lineClass) bool {
	if prevEff == clsNU {
		// NU × NU, NU × IS/SY, NU × PR/PO
		return curEff == clsNU || curEff == clsIS || curEff == clsSY || isNumAffix(curEff)
	}
	// IS/SY × NU, PR/PO × NU
	return (prevEff == clsIS || prevEff == clsSY || isNumAffix(prevEff)) && curEff == clsNU
}

// letterPairRules implements LB28–LB30: no breaks inside words or between
// words and parentheses.
func letterPairRules(prevEff, curEff lineClass) bool {
	if prevEff == clsAL && curEff == clsAL { // LB28
		return true
	}
	if prevEff == clsIS && curEff == clsAL { // LB29
		return true
	}
	// LB30: no break between letters/digits and parentheses.
	if (prevEff == clsAL || prevEff == clsNU) && curEff == clsOP {
		return true
	}
	return prevEff == clsCL && (curEff == clsAL || curEff == clsNU)
}

// graphemeBreaks returns grapheme cluster boundaries for runes following
// UAX #29 (simplified): gb[i] is true when a new cluster starts before
// rune i. gb[0] is always true.
func graphemeBreaks(runes []rune) []bool {
	n := len(runes)
	if n == 0 {
		return nil
	}
	gb := make([]bool, n)
	gb[0] = true
	riRun := 0
	if isRegionalIndicator(runes[0]) {
		riRun = 1
	}
	for i := 1; i < n; i++ {
		prev, cur := runes[i-1], runes[i]
		switch {
		case prev == '\r' && cur == '\n':
			gb[i] = false // GB3: CR × LF
		case isHardBreakRune(prev) || isHardBreakRune(cur):
			gb[i] = true // GB4/GB5
		case cur == '\u200D' || isGraphemeExtend(cur):
			gb[i] = false // GB9: × Extend, × ZWJ
		case prev == '\u200D' && isExtendedPictographic(cur):
			gb[i] = false // GB11: ZWJ × Extended_Pictographic
		case isRegionalIndicator(prev) && isRegionalIndicator(cur):
			gb[i] = riRun%2 == 0 // GB12/GB13: break between RI pairs
		default:
			gb[i] = true
		}
		if isRegionalIndicator(cur) {
			riRun++
		} else {
			riRun = 0
		}
	}
	return gb
}
