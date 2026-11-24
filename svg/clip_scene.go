package svg

import (
	"strings"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/scene"
)

// applySceneAttrClips pushes the clip-path referenced by attrs onto the
// retained scene. root maps the referencing element's local user space into
// scene coordinates. It returns the number of PushClip entries the caller
// must balance with PopClip after drawing the clipped content.
func applySceneAttrClips(target *scene.Scene, clips map[string]*ClipPath, attrs *Attrs, targetBox box, root gg.Matrix) int {
	if target == nil || attrs == nil || clips == nil {
		return 0
	}
	raw := strings.TrimSpace(attrs.ClipPathRef)
	if raw == "" || strings.EqualFold(raw, "none") {
		return 0
	}
	id, ok := parseClipReference(raw)
	if !ok {
		return 0
	}
	forcedRule := ""
	if strings.TrimSpace(attrs.ClipRule) != "" {
		forcedRule = ruleName(attrs.ClipRule)
	}
	return pushSceneClipRef(target, clips, id, targetBox, forcedRule, map[string]bool{}, gg.Identity(), root)
}

// pushSceneClipRef mirrors pushClipRef but encodes clips into a scene.
func pushSceneClipRef(target *scene.Scene, clips map[string]*ClipPath, id string, targetBox box, forcedRule string, chain map[string]bool, pre gg.Matrix, root gg.Matrix) int {
	cp, ok := clips[id]
	if !ok || chain[id] {
		// Unknown/cyclic hand-built definition: clip everything out.
		target.PushClip(scene.NewRectShape(0, 0, 0, 0))
		return 1
	}
	chain[id] = true
	defer delete(chain, id)

	m := pre
	if cp.Units == "objectBoundingBox" {
		if !targetBox.valid || targetBox.x1 == targetBox.x0 || targetBox.y1 == targetBox.y0 {
			return 0 // degenerate bbox disables OBB clipping
		}
		obb := gg.Translate(targetBox.x0, targetBox.y0).
			Multiply(gg.Scale(targetBox.x1-targetBox.x0, targetBox.y1-targetBox.y0))
		m = m.Multiply(obb)
	}
	if t := strings.TrimSpace(cp.Attrs.Transform); t != "" {
		if tm, err := transformMatrix(t); err == nil && matrixFinite(tm) {
			m = m.Multiply(tm)
		}
	}

	pushes := 0
	if nested := strings.TrimSpace(cp.Attrs.ClipPathRef); nested != "" && !strings.EqualFold(nested, "none") {
		if nid, ok := parseClipReference(nested); ok {
			pushes += pushSceneClipRef(target, clips, nid, targetBox, "", chain, pre, root)
		}
	}

	merged := gg.NewPath()
	hint := &ruleHint{}
	appendClipGeometry(cp.Children, m, merged, hint, strings.TrimSpace(cp.Attrs.ClipRule),
		func(rawRef string, shapePre gg.Matrix, shapeBox box, rule string) {
			nid, ok := parseClipReference(rawRef)
			if !ok {
				return
			}
			pushes += pushSceneClipRef(target, clips, nid, shapeBox, rule, chain, shapePre, root)
		})

	_ = forcedRule // scene PathShape has no configurable clip fill rule

	if len(merged.Verbs()) == 0 {
		target.PushClip(scene.NewRectShape(0, 0, 0, 0))
		return pushes + 1
	}
	// Bake the element-to-scene transform exactly once.
	baked := merged.Transform(root)
	target.PushClip(scene.NewGGPathShape(baked))
	return pushes + 1
}
