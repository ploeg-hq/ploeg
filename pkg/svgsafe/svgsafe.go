// Package svgsafe decides whether an SVG an operator consumer hands Ploeg
// is safe to store on a forge and embed in a pull request comment
// (ADR-0079). It refuses rather than rewrites: an SVG with anything outside
// a static drawing is rejected whole.
package svgsafe

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// MaxBytes is the largest SVG Check accepts.
const MaxBytes = 256 * 1024

var elements = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true, "symbol": true, "use": true, "style": true,
	"rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "path": true,
	"text": true, "tspan": true, "textPath": true, "image": true, "marker": true, "pattern": true, "clipPath": true, "mask": true,
	"linearGradient": true, "radialGradient": true, "stop": true, "filter": true,
	"feBlend": true, "feColorMatrix": true, "feComponentTransfer": true, "feComposite": true, "feDropShadow": true,
	"feFlood": true, "feFuncA": true, "feFuncB": true, "feFuncG": true, "feFuncR": true, "feGaussianBlur": true,
	"feMerge": true, "feMergeNode": true, "feMorphology": true, "feOffset": true,
}

var (
	rasterData   = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=\s]*$`)
	externalURL  = regexp.MustCompile(`(?i)url\(\s*['"]?\s*[^#'"\s)]`)
	dangerousCSS = regexp.MustCompile(`(?i)@import|expression\s*\(|javascript:|behavior\s*:|-moz-binding`)
)

// ErrUnsafe wraps every refusal.
var ErrUnsafe = errors.New("unsafe svg")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafe, fmt.Sprintf(format, args...))
}

// Check returns nil when svg is a static drawing: an <svg> root holding only
// drawing elements, no scripts, event handlers, foreignObject, entity
// declarations or references outside the document except embedded raster
// images.
func Check(svg []byte) error {
	if len(svg) == 0 || len(svg) > MaxBytes {
		return refuse("size must be between 1 and %d bytes", MaxBytes)
	}
	decoder := xml.NewDecoder(bytes.NewReader(svg))
	decoder.Strict = true
	decoder.Entity = map[string]string{}
	depth, roots := 0, 0
	inStyle := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return refuse("not well-formed XML: %v", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if depth == 0 {
				roots++
				if name != "svg" || roots > 1 {
					return refuse("the document must be one <svg> element")
				}
			}
			if !elements[name] {
				return refuse("element <%s> is not allowed", name)
			}
			for _, a := range t.Attr {
				if err := checkAttr(a); err != nil {
					return err
				}
			}
			inStyle = name == "style"
			depth++
		case xml.EndElement:
			depth--
			inStyle = false
		case xml.CharData:
			if inStyle {
				if err := checkCSS(string(t)); err != nil {
					return err
				}
			}
		case xml.Directive:
			return refuse("declarations such as DOCTYPE and ENTITY are not allowed")
		case xml.ProcInst:
			if t.Target != "xml" {
				return refuse("processing instruction %q is not allowed", t.Target)
			}
		}
	}
	if roots != 1 {
		return refuse("the document must be one <svg> element")
	}
	return nil
}

func checkAttr(a xml.Attr) error {
	name := strings.ToLower(a.Name.Local)
	value := a.Value
	compact := strings.ToLower(strings.Join(strings.Fields(value), ""))
	switch {
	case strings.HasPrefix(name, "on"):
		return refuse("event handler attribute %q is not allowed", a.Name.Local)
	case strings.Contains(compact, "javascript:") || strings.Contains(compact, "vbscript:"):
		return refuse("attribute %q carries a script URL", a.Name.Local)
	case name == "href" || name == "src":
		v := strings.TrimSpace(value)
		if !strings.HasPrefix(v, "#") && !rasterData.MatchString(v) {
			return refuse("attribute %q may only reference the document or an embedded raster image", a.Name.Local)
		}
	case name == "style":
		return checkCSS(value)
	}
	if externalURL.MatchString(value) {
		return refuse("attribute %q references something outside the document", a.Name.Local)
	}
	return nil
}

func checkCSS(css string) error {
	if dangerousCSS.MatchString(css) {
		return refuse("style carries an import, expression or script")
	}
	if externalURL.MatchString(css) {
		return refuse("style references something outside the document")
	}
	return nil
}
