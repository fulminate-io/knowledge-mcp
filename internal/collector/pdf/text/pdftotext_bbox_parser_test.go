package text

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// pdftotextBBoxDoc / pdftotextBBoxPage / pdftotextBBoxWord shadow the
// XHTML structure poppler emits with `pdftotext -bbox -layout`:
//
//	<doc><page width=".." height=".."><word xMin=".." yMin=".." xMax=".." yMax="..">text</word>...</page></doc>
//
// Parsed by parsePdftotextBBox; consumed by
// charbounds_pdftotext_test.go. _test.go suffix keeps these symbols
// out of the production package surface.
type pdftotextBBoxDoc struct {
	Pages []pdftotextBBoxPage
}

type pdftotextBBoxPage struct {
	Width  float64
	Height float64
	Words  []pdftotextBBoxWord
}

type pdftotextBBoxWord struct {
	XMin float64 `xml:"xMin,attr"`
	YMin float64 `xml:"yMin,attr"`
	XMax float64 `xml:"xMax,attr"`
	YMax float64 `xml:"yMax,attr"`
	Text string  `xml:",chardata"`
}

// parsePdftotextBBox parses the poppler -bbox XHTML output into a
// per-page list of words. The parser is tolerant of the XHTML
// preamble and only extracts <page> / <word> elements.
func parsePdftotextBBox(b []byte) (pdftotextBBoxDoc, error) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var doc pdftotextBBoxDoc
	var cur *pdftotextBBoxPage
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return doc, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "page" {
				doc.Pages = append(doc.Pages, pdftotextBBoxPage{})
				cur = &doc.Pages[len(doc.Pages)-1]
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "width":
						cur.Width = atofBBox(a.Value)
					case "height":
						cur.Height = atofBBox(a.Value)
					}
				}
			} else if t.Name.Local == "word" && cur != nil {
				var w pdftotextBBoxWord
				if err := dec.DecodeElement(&w, &t); err != nil {
					return doc, err
				}
				w.Text = strings.TrimSpace(w.Text)
				if w.Text != "" {
					cur.Words = append(cur.Words, w)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "page" {
				cur = nil
			}
		}
	}
	return doc, nil
}

// atofBBox is a small attribute-value parser. Returns 0 on parse
// failure; callers use it on poppler-generated numeric strings where
// parse errors are not expected in practice.
func atofBBox(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
