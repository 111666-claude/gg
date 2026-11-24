package svg

import (
	"fmt"
	"math"
	"strings"
)

// maxUseDepth bounds <use> expansion chains. SVG documents nesting instances
// deeper than this are rejected rather than expanded without limit.
const maxUseDepth = 32

// SourcePos identifies an element's location in the SVG source. Line and
// column are 1-based, matching conventional compiler diagnostics.
type SourcePos struct {
	Line   int
	Column int
}

func (p SourcePos) String() string {
	if p.Line <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// svgError carries a message together with the source position of the element
// the error is reported on.
type svgError struct {
	pos     SourcePos
	element string
	message string
}

func (e *svgError) Error() string {
	var b strings.Builder
	b.WriteString("svg: ")
	switch {
	case e.element != "" && e.pos.Line > 0:
		fmt.Fprintf(&b, "<%s> at %s: %s", e.element, e.pos, e.message)
	case e.element != "":
		fmt.Fprintf(&b, "<%s>: %s", e.element, e.message)
	case e.pos.Line > 0:
		fmt.Fprintf(&b, "at %s: %s", e.pos, e.message)
	default:
		b.WriteString(e.message)
	}
	return b.String()
}

// parseContext threads source position and the id registry through parsing.
type parseContext struct {
	data []byte

	// defs maps id -> first element carrying that id.
	defs map[string]Element

	// idPos records where each id was first declared, so duplicate-id errors
	// can point at both declarations.
	idPos map[string]SourcePos

	// uses collects every <use> element for post-parse resolution.
	uses []*UseElement
}

func newParseContext(data []byte) *parseContext {
	return &parseContext{
		data:  data,
		defs:  make(map[string]Element),
		idPos: make(map[string]SourcePos),
	}
}

// positionAt converts a byte offset into the source into a 1-based line/column.
// The offset is walked back to the nearest '<' so the reported position is the
// start of the owning element rather than the token following it.
func (pc *parseContext) positionAt(offset int64) SourcePos {
	if len(pc.data) == 0 {
		return SourcePos{}
	}
	if int(offset) > len(pc.data) {
		offset = int64(len(pc.data))
	}
	start := int(offset)
	for start > 0 && pc.data[start-1] != '<' {
		start--
	}
	line, col := 1, 1
	for i := 0; i < start; i++ {
		switch pc.data[i] {
		case '\n':
			line++
			col = 1
		case '\r':
			// A following '\n' advances the line on its own.
			if i+1 < len(pc.data) && pc.data[i+1] == '\n' {
				continue
			}
			line++
			col = 1
		default:
			col++
		}
	}
	return SourcePos{Line: line, Column: col}
}

// elementErrorf builds a positioned error for an element.
func elementErrorf(pos SourcePos, element, format string, args ...any) error {
	return &svgError{pos: pos, element: element, message: fmt.Sprintf(format, args...)}
}

// registerID records an id -> element binding. Empty ids are ignored; a second
// element using the same id is an error and identifies both locations.
func (pc *parseContext) registerID(id, elementName string, elem Element, pos SourcePos) error {
	if id == "" {
		return nil
	}
	if _, ok := pc.defs[id]; ok {
		return elementErrorf(pos, elementName, "duplicate id %q: first declared at %s", id, pc.idPos[id])
	}
	pc.defs[id] = elem
	pc.idPos[id] = pos
	return nil
}

// resolveUses validates and resolves every collected <use> element after the
// full document tree has been built, so forward references work.
func (pc *parseContext) resolveUses() error {
	for _, use := range pc.uses {
		if err := pc.resolveUse(use, map[Element]bool{}, 0); err != nil {
			return err
		}
	}
	return nil
}

// resolveUse resolves one <use>, recursively walking instances nested inside
// the referenced subtree. stack holds the elements currently being expanded,
// so re-entering an element already on the stack is a circular reference.
// The depth argument counts <use> hops from the outermost instance.
func (pc *parseContext) resolveUse(use *UseElement, stack map[Element]bool, depth int) error {
	if depth >= maxUseDepth {
		return elementErrorf(use.pos, "use",
			"reference depth exceeds limit of %d while resolving %q", maxUseDepth, use.HRef)
	}

	href := strings.TrimSpace(use.HRef)
	if href == "" {
		return elementErrorf(use.pos, "use", "missing href attribute")
	}
	if !strings.HasPrefix(href, "#") {
		// Anything that is not a same-document fragment reference is out of
		// scope: external files, URLs and other documents.
		return elementErrorf(use.pos, "use",
			"cross-document reference %q is not supported; only same-document #id references are allowed", href)
	}
	id := href[1:]
	if id == "" {
		return elementErrorf(use.pos, "use", "empty fragment reference %q", href)
	}
	target, ok := pc.defs[id]
	if !ok {
		return elementErrorf(use.pos, "use", "reference %q does not match any element with id %q", href, id)
	}

	// A symbol with an unusable viewport cannot establish a viewBox mapping.
	if use.hasWidth && (math.IsNaN(use.Width) || math.IsInf(use.Width, 0) || use.Width < 0) {
		return elementErrorf(use.pos, "use", "invalid width %g for reference %q", use.Width, href)
	}
	if use.hasHeight && (math.IsNaN(use.Height) || math.IsInf(use.Height, 0) || use.Height < 0) {
		return elementErrorf(use.pos, "use", "invalid height %g for reference %q", use.Height, href)
	}

	// Re-entering an element already on the expansion stack closes a cycle.
	if stack[target] {
		return elementErrorf(use.pos, "use",
			"circular reference detected: %q refers back through its own expansion chain", href)
	}

	use.Ref = target
	stack[target] = true
	defer delete(stack, target)

	// Resolve instances nested inside the referenced subtree so cycles through
	// defs-only content are caught even when never rendered directly.
	for _, child := range nestedUses(target) {
		if err := pc.resolveUse(child, stack, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// targetElements returns the children of an element that can carry subtree
// content (groups and symbols).
func targetElements(elem Element) []Element {
	switch e := elem.(type) {
	case *GroupElement:
		return e.Children
	case *SymbolElement:
		return e.Children
	default:
		return nil
	}
}

// nestedUses collects every <use> inside an element's subtree, in document
// order.
func nestedUses(elem Element) []*UseElement {
	var uses []*UseElement
	var walk func([]Element)
	walk = func(elements []Element) {
		for _, child := range elements {
			if use, ok := child.(*UseElement); ok {
				uses = append(uses, use)
				continue
			}
			walk(targetElements(child))
		}
	}
	walk(targetElements(elem))
	return uses
}
