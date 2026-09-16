// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestDesktopPlatformResponseArms pins the arm selection cell by cell. The arm
// is decided by the CONTENT TYPE alone and never by whether the bytes happen to
// parse: a JSON-typed body that is not valid JSON stays the failure it was, and
// an implementation that reads "not parseable" as "binary" would turn a gateway
// error page into a successful download.
func TestDesktopPlatformResponseArms(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	const jsonBody = `{"ok":true}`
	for _, row := range []struct {
		name        string
		contentType string
		body        string
		status      int
		wantKeys    []string
		wantCode    string
	}{
		{name: "application/json is the JSON arm", contentType: "application/json", body: jsonBody, status: http.StatusOK, wantKeys: []string{"body", "status"}},
		{name: "a charset parameter stays the JSON arm", contentType: "application/json; charset=utf-8", body: jsonBody, status: http.StatusOK, wantKeys: []string{"body", "status"}},
		{name: "an absent content type is the JSON arm", contentType: "", body: jsonBody, status: http.StatusOK, wantKeys: []string{"body", "status"}},
		{name: "text/csv is the binary arm", contentType: "text/csv", body: "a,b\n1,2\n", status: http.StatusOK, wantKeys: []string{"bodyBase64", "contentType", "status"}},
		{name: "a csv charset parameter is carried verbatim", contentType: "text/csv; charset=utf-8", body: "a,b\n1,2\n", status: http.StatusOK, wantKeys: []string{"bodyBase64", "contentType", "status"}},
		{name: "application/pdf is the binary arm", contentType: "application/pdf", body: "%PDF-1.7\n", status: http.StatusOK, wantKeys: []string{"bodyBase64", "contentType", "status"}},
		{name: "an empty binary body still carries its key", contentType: "text/csv", body: "", status: http.StatusOK, wantKeys: []string{"bodyBase64", "contentType", "status"}},
		{name: "a non-2xx binary answer is still the binary arm", contentType: "text/csv", body: "a,b\n", status: http.StatusPaymentRequired, wantKeys: []string{"bodyBase64", "contentType", "status"}},
		{name: "a JSON type with bytes that are not JSON stays a failure", contentType: "application/json", body: "<html>gateway</html>", status: http.StatusBadGateway, wantCode: platformCodeError},
		{name: "an unparseable content type is a failure", contentType: "text/csv; charset=\"", body: "a,b\n", status: http.StatusOK, wantCode: platformCodeError},
		// The bound on the content type is a refusal arm of its own: this command
		// repeats the type to the renderer, which makes it a RESPONSE HEADER, so a
		// type it cannot vouch for is not one to pass on. Without the bound this
		// row is a 200 whose contentType is the whole over-length string. The
		// fixture is computed from the declaration, never pinned.
		{name: "a content type over the bound is a failure", contentType: "text/csv; charset=utf-8; x=" + strings.Repeat("p", platformContentTypeLimit), body: "a,b\n", status: http.StatusOK, wantCode: platformCodeError},
	} {
		t.Run(row.name, func(t *testing.T) {
			f.contentType, f.body, f.status = row.contentType, row.body, row.status
			got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"}}`)
			if row.wantCode != "" {
				if got.Code != row.wantCode {
					t.Fatalf("runDesktopPlatform(%s) = %+v, want code %q", row.name, got, row.wantCode)
				}
				return
			}
			wire := platformWire(t, got)
			if keys := platformWireKeys(wire); !slices.Equal(keys, row.wantKeys) {
				t.Fatalf("runDesktopPlatform(%s) sent keys %v, want %v", row.name, keys, row.wantKeys)
			}
			if status, _ := wire["status"].(float64); int(status) != row.status {
				t.Errorf("runDesktopPlatform(%s) sent status %v, want %d", row.name, wire["status"], row.status)
			}
			if slices.Contains(row.wantKeys, "bodyBase64") {
				if declared, _ := wire["contentType"].(string); declared != row.contentType {
					t.Errorf("runDesktopPlatform(%s) sent contentType %q, want the platform's %q", row.name, declared, row.contentType)
				}
				encoded, _ := wire["bodyBase64"].(string)
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Fatalf("runDesktopPlatform(%s) sent bodyBase64 that is not base64: %v", row.name, err)
				}
				if string(decoded) != row.body {
					t.Errorf("runDesktopPlatform(%s) round-tripped %q, want %q", row.name, decoded, row.body)
				}
				return
			}
			if body, _ := wire["body"]; body == nil {
				t.Errorf("runDesktopPlatform(%s) sent no body on the JSON arm", row.name)
			}
		})
	}
	// The 204 row the JSON arm has always carried: an empty body with no content
	// type reaches the renderer as null, which an arm selection that read an
	// absent type as binary would lose.
	f.contentType, f.body, f.status = "", "", http.StatusNoContent
	got := f.run(t, `{"method":"GET","path":"/auth/me","params":{}}`)
	if got.Code != "" || got.Status != http.StatusNoContent || string(got.Body) != "null" {
		t.Errorf("runDesktopPlatform(204) = %+v, want the JSON arm's null", got)
	}
	if keys := platformWireKeys(platformWire(t, got)); !slices.Equal(keys, []string{"body", "status"}) {
		t.Errorf("runDesktopPlatform(204) sent keys %v, want the JSON arm's", keys)
	}
}

// TestDesktopPlatformBinaryRoundTrip pins that the bytes survive the hop
// unmangled, on a payload that is NOT valid UTF-8. A real PDF is binary from
// its fifth byte, so a string conversion anywhere on this path would replace
// bytes with U+FFFD and the downloaded file would be corrupt in a way no text
// fixture can show.
func TestDesktopPlatformBinaryRoundTrip(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	fixture := []byte{0x25, 0x50, 0x44, 0x46, 0x2d, 0x31, 0x2e, 0x37, 0x0a, 0x80, 0xff, 0xfe, 0x00, 0x01, 0x7f, 0xc3, 0x28}
	if utf8 := strings.ToValidUTF8(string(fixture), ""); utf8 == string(fixture) {
		t.Fatalf("the fixture is valid UTF-8, so this row cannot observe a string conversion")
	}
	f.contentType, f.body = "application/pdf", string(fixture)
	got := f.run(t, `{"method":"GET","path":"/v1/accounts/{account_id}/billing/invoices/{invoice_id}/pdf","params":{"account_id":"account-A","invoice_id":"in_1"},"query":{"download":"true"}}`)
	if got.Code != "" || got.BodyBase64 == nil {
		t.Fatalf("runDesktopPlatform(pdf) = %+v, want the binary arm", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(*got.BodyBase64)
	if err != nil {
		t.Fatalf("base64.DecodeString() errored: %v", err)
	}
	if !slices.Equal(decoded, fixture) {
		t.Errorf("the round trip returned % x, want % x", decoded, fixture)
	}
	if got.Body != nil {
		t.Errorf("the binary arm also carried a JSON body: %s", got.Body)
	}
}

// TestDesktopPlatformResponseCap pins the cap boundary on this wall: a body AT
// the cap is returned, one byte over is refused with the named code, and the
// refusal is never a truncated body and never the code a down platform gets.
//
// Both fixtures are computed from the declared cap rather than pinned as
// literals, so a re-spelled cap moves the boundary with it.
func TestDesktopPlatformResponseCap(t *testing.T) {
	f := newPlatformFixture(t, "account-A")
	export := `{"method":"GET","path":"/v1/accounts/{account_id}/audit-logs/export","params":{"account_id":"account-A"}}`
	f.contentType = "text/csv"
	f.body = strings.Repeat("c", platformBodyCap)
	got := f.run(t, export)
	if got.Code != "" || got.BodyBase64 == nil {
		t.Fatalf("runDesktopPlatform(a body at the cap) = %+v, want the binary arm", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(*got.BodyBase64)
	if err != nil {
		t.Fatalf("base64.DecodeString() errored: %v", err)
	}
	if len(decoded) != platformBodyCap {
		t.Errorf("a body at the cap returned %d bytes, want the whole %d", len(decoded), platformBodyCap)
	}
	f.body = strings.Repeat("c", platformBodyCap+1)
	if got := f.run(t, export); got.Code != platformCodeResponseTooLarge {
		t.Fatalf("runDesktopPlatform(one byte over the cap) = %+v, want %q", summarize(got), platformCodeResponseTooLarge)
	}
	// The JSON arm shares the cap: the transport's limit rose to carry the
	// binary arm, and a JSON answer must not have silently widened with it.
	f.contentType = "application/json"
	f.body = `{"pad":"` + strings.Repeat("j", platformBodyCap) + `"}`
	if got := f.run(t, export); got.Code != platformCodeResponseTooLarge {
		t.Fatalf("runDesktopPlatform(an oversize JSON body) = %+v, want %q", summarize(got), platformCodeResponseTooLarge)
	}
	// A body over the TRANSPORT's own outer limit is still the named code and
	// never platform_unreachable, which is the outcome a caller would read as a
	// down platform.
	f.contentType = "text/csv"
	f.body = strings.Repeat("c", auth.PlatformResponseLimit+1)
	if got := f.run(t, export); got.Code != platformCodeResponseTooLarge {
		t.Fatalf("runDesktopPlatform(over the transport limit) = %+v, want %q", summarize(got), platformCodeResponseTooLarge)
	}
}

// summarize renders an outcome without its body, so a failure message about a
// multi-megabyte fixture stays readable.
func summarize(out desktopPlatformResult) string {
	return "{state:" + out.State + " code:" + out.Code + " status:" + strconv.Itoa(out.Status) + " contentType:" + out.ContentType + " bodyBytes:" + strconv.Itoa(len(out.Body)) + "}"
}

// TestDesktopPlatformCapIsInsideTheTransportLimit pins the relation the named
// refusal depends on, read from BOTH declarations rather than from a literal:
// if the transport's limit ever fell below this command's cap, the transport
// would refuse first and the named code would become unreachable.
func TestDesktopPlatformCapIsInsideTheTransportLimit(t *testing.T) {
	if platformBodyCap > auth.PlatformResponseLimit {
		t.Errorf("platformBodyCap = %d, want it at or below auth.PlatformResponseLimit (%d)", platformBodyCap, auth.PlatformResponseLimit)
	}
}

// TestDesktopPlatformOutcomeVocabulary pins the declared set itself: every code
// this command can emit is a member, and the member requirement 2 adds is
// present. The cross-wall half of this row lives in the Desktop suite, which
// reads this declaration's own members rather than a hand list.
func TestDesktopPlatformOutcomeVocabulary(t *testing.T) {
	if !slices.Contains(platformOutcomeCodes, platformCodeResponseTooLarge) {
		t.Errorf("platformOutcomeCodes = %v, want it to declare %q", platformOutcomeCodes, platformCodeResponseTooLarge)
	}
	for _, code := range platformOutcomeCodes {
		if code == "" || strings.ContainsAny(code, " \t\"") {
			t.Errorf("platformOutcomeCodes carries %q, which is not a code shape the other wall can admit", code)
		}
	}
	if len(slices.Compact(slices.Clone(platformOutcomeCodes))) != len(platformOutcomeCodes) {
		t.Errorf("platformOutcomeCodes = %v, want no repeated member", platformOutcomeCodes)
	}
}

// platformOutcomeViolations reports every place in the PRODUCTION source it is
// given where an outcome code is written as a bare literal outside its
// declaration, or where a desktopPlatformResult failure is constructed outside
// platformFailure. It is a function over a parsed file set rather than a test
// body so the test can run it over a synthetic bad file as its own known
// positive: a census with no proven red says nothing about the source it walked.
//
// THE BARE-LITERAL HALF IS SCOPED TO THIS COMMAND'S OWN FILES, and the first
// run of this census is why: the package's sibling Desktop seams declare their
// own outcome vocabularies, and desktop_terminal.go, desktop_settings.go,
// desktop_remote.go, desktop_dashboard.go, desktop_environment_write.go and
// desktop_dashboard_http.go each write words this set happens to share
// ("invalid_request", and "response_too_large" at desktop_dashboard_http.go's
// own 1 MiB bound). Those are their words, not this command's. The
// CONSTRUCTION half is not scoped, because desktopPlatformResult is this
// command's own type and a construction of it anywhere in the package is in
// scope wherever it sits.
func platformOutcomeViolations(fset *token.FileSet, files map[string]*ast.File, declaring string) []string {
	var found []string
	for name, file := range files {
		scoped := name == declaring || strings.HasPrefix(name, "desktop_platform")
		declared := token.NoPos
		end := token.NoPos
		if name == declaring {
			for _, decl := range file.Decls {
				group, ok := decl.(*ast.GenDecl)
				if !ok || group.Tok != token.CONST {
					continue
				}
				for _, spec := range group.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok || len(value.Names) == 0 || !strings.HasPrefix(value.Names[0].Name, "platformCode") {
						continue
					}
					declared, end = group.Pos(), group.End()
				}
			}
		}
		for _, decl := range file.Decls {
			function, _ := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.BasicLit:
					if !scoped || typed.Kind != token.STRING {
						return true
					}
					text, err := strconv.Unquote(typed.Value)
					if err != nil || !slices.Contains(platformOutcomeCodes, text) {
						return true
					}
					if declared != token.NoPos && typed.Pos() > declared && typed.End() < end {
						return true
					}
					found = append(found, fset.Position(typed.Pos()).String()+": bare outcome literal "+typed.Value)
				case *ast.CompositeLit:
					named, ok := typed.Type.(*ast.Ident)
					if !ok || named.Name != "desktopPlatformResult" {
						return true
					}
					for _, element := range typed.Elts {
						pair, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := pair.Key.(*ast.Ident)
						if !ok || (key.Name != "State" && key.Name != "Code") {
							continue
						}
						if function != nil && function.Name.Name == "platformFailure" {
							continue
						}
						from := "a package-level declaration"
						if function != nil {
							from = function.Name.Name
						}
						found = append(found, fset.Position(pair.Pos()).String()+": failure result constructed in "+from)
						break
					}
				}
				return true
			})
		}
	}
	slices.Sort(found)
	return found
}

// TestDesktopPlatformOutcomeVocabularyIsDeclared is the source census the
// declared set rests on: the agreement row between the two walls passes while a
// refusal site bypasses the set with a bare literal, and this is what makes
// that bypass a red.
func TestDesktopPlatformOutcomeVocabularyIsDeclared(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("os.ReadDir(\".\") errored: %v", err)
	}
	files := map[string]*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			// t.Fatal, deliberately: this loop is not the test table, it is the
			// SETUP that builds the file set the census walks, and a failure
			// that sets up the whole test before its loop is the case the
			// t.Fatal rule (gostyle-best-practices-t-fatal) blesses. The branch
			// is reachable only for a file the package's build constraints
			// exclude yet which does not parse; any other unparseable file
			// would already have failed the test binary's own compilation.
			t.Fatalf("parser.ParseFile(%q) errored: %v", name, err)
		}
		files[name] = parsed
	}
	if len(files) == 0 {
		t.Fatal("the census parsed no production file, so its verdict would be vacuous")
	}
	if _, ok := files["desktop_platform.go"]; !ok {
		t.Fatal("the census did not reach desktop_platform.go, which declares the set")
	}
	if violations := platformOutcomeViolations(fset, files, "desktop_platform.go"); len(violations) != 0 {
		t.Errorf("the outcome vocabulary is bypassed:\n%s", strings.Join(violations, "\n"))
	}
	// THE KNOWN POSITIVE, through the same instrument in the same run: a
	// synthetic file carrying every shape this census forbids, one per class
	// the loop below names.
	badFset := token.NewFileSet()
	bad, err := parser.ParseFile(badFset, "synthetic.go", `package cli
func refuse() desktopPlatformResult { return desktopPlatformResult{State: "error", Code: "platform_error"} }
`, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parser.ParseFile(the synthetic control) errored: %v", err)
	}
	control := platformOutcomeViolations(badFset, map[string]*ast.File{"desktop_platform_synthetic.go": bad}, "desktop_platform.go")
	// BOTH CLASSES, not a count: the census reports one line per site and the
	// number of sites in the control is not the property under test.
	for _, class := range []string{"bare outcome literal", "failure result constructed in"} {
		if !slices.ContainsFunc(control, func(line string) bool { return strings.Contains(line, class) }) {
			t.Errorf("the census reported %v on its known positive, with no %q line — it cannot see that class", control, class)
		}
	}
}
