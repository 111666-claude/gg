package capgate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"image"

	"github.com/gogpu/gg/recording"
)

// Digest is a structural snapshot of a Recording for mutation detection.
type Digest struct {
	// SHA256 is the canonical fingerprint of commands and resources.
	SHA256 string `json:"sha256"`
	// Commands is the command count.
	Commands int `json:"commands"`
	// Paths, Brushes, Images, Fonts are resource pool counts.
	Paths   int `json:"paths"`
	Brushes int `json:"brushes"`
	Images  int `json:"images"`
	Fonts   int `json:"fonts"`
}

// Fingerprint computes a stable digest of the recording's commands and
// pooled resources. Playback must leave the digest unchanged; the gate
// compares fingerprints before and after replay.
func Fingerprint(r *recording.Recording) Digest {
	if r == nil {
		return Digest{}
	}
	h := sha256.New()
	cmds := r.Commands()
	fmt.Fprintf(h, "rec %dx%d cmds=%d\n", r.Width(), r.Height(), len(cmds))
	for i, c := range cmds {
		// %#v includes slice contents (dash patterns) and value fields;
		// pointer-bearing fields stay address-stable within one process.
		fmt.Fprintf(h, "cmd[%d]=%T %#v\n", i, c, c)
	}

	d := Digest{SHA256: "", Commands: len(cmds)}
	pool := r.Resources()
	if pool != nil {
		d.Paths = pool.PathCount()
		d.Brushes = pool.BrushCount()
		d.Images = pool.ImageCount()
		d.Fonts = pool.FontCount()

		for i := 0; i < d.Paths; i++ {
			p := pool.GetPath(recording.PathRef(i))
			fmt.Fprintf(h, "path[%d] ", i)
			if p == nil {
				fmt.Fprintln(h, "nil")
				continue
			}
			fmt.Fprintf(h, "verbs=%v coords=%v\n", p.Verbs(), p.Coords())
		}
		for i := 0; i < d.Brushes; i++ {
			fmt.Fprintf(h, "brush[%d]=%#v\n", i, pool.GetBrush(recording.BrushRef(i)))
		}
		for i := 0; i < d.Images; i++ {
			img := pool.GetImage(recording.ImageRef(i))
			fmt.Fprintf(h, "image[%d]=", i)
			hashImage(h, img)
			fmt.Fprintln(h)
		}
		for i := 0; i < d.Fonts; i++ {
			fmt.Fprintf(h, "font[%d]=%#v\n", i, pool.GetFont(recording.FontRef(i)))
		}
	}

	d.SHA256 = hex.EncodeToString(h.Sum(nil))
	return d
}

func hashImage(h hash.Hash, img image.Image) {
	if img == nil {
		fmt.Fprint(h, "nil")
		return
	}
	b := img.Bounds()
	fmt.Fprintf(h, "%T bounds=%v ", img, b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			rr, gg, bb, aa := img.At(x, y).RGBA()
			fmt.Fprintf(h, "%d,%d,%d,%d;", rr, gg, bb, aa)
		}
	}
}

// Unchanged reports whether two digests are identical in every field.
func (d Digest) Unchanged(other Digest) bool {
	return d == other
}
