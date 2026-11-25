package gg

// Capabilities describes the features implemented by a gg.SoftwareRenderer.
//
// The CPU renderer supports the full public BlendMode set, anti-aliasing,
// images and text. It is provided so callers (and offline replay gates) can
// query support instead of assuming Multiply/Screen are unavailable on CPU.
type Capabilities struct {
	// SupportsAntialiasing reports coverage-based anti-aliased rasterization.
	SupportsAntialiasing bool

	// SupportsBlendModes reports that all public BlendMode values are
	// implemented by the software fill/stroke/image paths.
	SupportsBlendModes bool

	// SupportsImages reports DrawImage/DrawImageEx compositing support.
	SupportsImages bool

	// SupportsText reports glyph rendering support.
	SupportsText bool
}

// RendererCapabilities is an alias for callers that qualify the name with the
// Renderer prefix.
type RendererCapabilities = Capabilities

// Capabilities returns the software renderer's actual feature set. The blend
// flag is true because Fill, Stroke, image and layer paths all dispatch
// through the premultiplied blend table.
func (r *SoftwareRenderer) Capabilities() Capabilities {
	return Capabilities{
		SupportsAntialiasing: true,
		SupportsBlendModes:   true,
		SupportsImages:       true,
		SupportsText:         true,
	}
}

// SupportsBlendMode reports whether the software pipeline implements mode.
// Every public gg BlendMode value (including the legacy 0-3 aliases and the
// 29 internal operators) is supported; out-of-range values are not.
func (r *SoftwareRenderer) SupportsBlendMode(mode BlendMode) bool {
	bm := mapPublicBlendMode(mode)
	return int(bm) < len(blendFuncTable) && blendFuncTable[bm] != nil
}
