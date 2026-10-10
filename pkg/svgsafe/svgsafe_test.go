package svgsafe

import (
	"errors"
	"strings"
	"testing"
)

func TestCheck_AcceptsAStaticDrawing(t *testing.T) {
	svg := `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="400" height="200">
  <title>Summary</title>
  <style>.a { fill: url(#g); font-family: sans-serif; }</style>
  <defs><linearGradient id="g"><stop offset="0" stop-color="#000"/></linearGradient>
    <filter id="f"><feGaussianBlur stdDeviation="2"/></filter></defs>
  <g class="a" filter="url(#f)"><rect width="400" height="200" rx="8"/><text x="10" y="20">Merged <tspan>today</tspan></text></g>
  <use xlink:href="#g"/>
  <image href="data:image/png;base64,iVBORw0KGgo=" width="1" height="1"/>
</svg>`
	if err := Check([]byte(svg)); err != nil {
		t.Fatal(err)
	}
}

func TestCheck_RefusesAnythingActiveOrExternal(t *testing.T) {
	ns := `xmlns="http://www.w3.org/2000/svg"`
	for name, svg := range map[string]string{
		"script":            `<svg ` + ns + `><script>alert(1)</script></svg>`,
		"event handler":     `<svg ` + ns + ` onload="alert(1)"/>`,
		"handler any case":  `<svg ` + ns + `><rect OnClick="x()"/></svg>`,
		"foreignObject":     `<svg ` + ns + `><foreignObject><div/></foreignObject></svg>`,
		"anchor":            `<svg ` + ns + `><a href="#x"><rect/></a></svg>`,
		"external href":     `<svg ` + ns + `><image href="https://evil.example/x.png"/></svg>`,
		"svg data href":     `<svg ` + ns + `><image href="data:image/svg+xml;base64,PHN2Zz4="/></svg>`,
		"javascript url":    `<svg ` + ns + `><use href="java&#x09;script:alert(1)"/></svg>`,
		"external css url":  `<svg ` + ns + `><style>rect { fill: url(https://evil.example/a) }</style></svg>`,
		"css import":        `<svg ` + ns + `><style>@import "x.css";</style></svg>`,
		"style attr url":    `<svg ` + ns + `><rect style="background:url('//evil.example')"/></svg>`,
		"external fill url": `<svg ` + ns + `><rect fill="url(other.svg#g)"/></svg>`,
		"doctype":           `<!DOCTYPE svg [<!ENTITY x "y">]><svg ` + ns + `/>`,
		"processing":        `<?xml-stylesheet href="x.css"?><svg ` + ns + `/>`,
		"not svg root":      `<html/>`,
		"two roots":         `<svg ` + ns + `/><svg ` + ns + `/>`,
		"malformed":         `<svg ` + ns + `><rect></svg>`,
		"empty":             ``,
		"too large":         `<svg ` + ns + `>` + strings.Repeat(" ", MaxBytes) + `</svg>`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := Check([]byte(svg)); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("Check = %v; want ErrUnsafe", err)
			}
		})
	}
}
