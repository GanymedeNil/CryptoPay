package api

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file parses docs/api.md — the TypeScript interfaces in its ```ts
// blocks, the `Page<T>` convention and the endpoint tables — so the
// contract tests check real responses against the document itself.

// tsType is a parsed TypeScript type expression.
type tsType struct {
	kind   string    // "union", "intersection", "array", "object", "ref", "prim", "lit", "boollit", "record", "page"
	alts   []*tsType // union, intersection
	elem   *tsType   // array, page
	fields []tsField // object
	name   string    // ref name, prim name
	lit    string    // string literal value
	blit   bool      // boolean literal value
}

type tsField struct {
	Name     string
	Optional bool
	Type     *tsType
}

type tsIface struct {
	Name    string
	Extends []string
	Fields  []tsField
}

// contractDoc is docs/api.md, parsed.
type contractDoc struct {
	ifaces  map[string]*tsIface
	aliases map[string]*tsType
	// routes maps "METHOD /full/path/{param}" to the documented success
	// response: its status (0 = 200/any 2xx) and type (nil = no body).
	routes map[string]routeSpec
}

type routeSpec struct {
	Status int
	Type   *tsType
	Raw    string
}

func apiDocPath(t testing.TB) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "api.md")
}

func loadContract(t testing.TB) *contractDoc {
	t.Helper()
	raw, err := os.ReadFile(apiDocPath(t))
	require.NoError(t, err, "docs/api.md")
	doc, err := parseContract(string(raw))
	require.NoError(t, err)
	return doc
}

var tsBlockRE = regexp.MustCompile("(?s)```ts\n(.*?)```")

func parseContract(md string) (*contractDoc, error) {
	doc := &contractDoc{ifaces: map[string]*tsIface{}, aliases: map[string]*tsType{}, routes: map[string]routeSpec{}}
	for _, m := range tsBlockRE.FindAllStringSubmatch(md, -1) {
		if err := doc.parseBlock(stripTSComments(m[1])); err != nil {
			return nil, err
		}
	}
	if err := doc.parseRoutes(md); err != nil {
		return nil, err
	}
	return doc, nil
}

func stripTSComments(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, "")
	return regexp.MustCompile(`//[^\n]*`).ReplaceAllString(s, "")
}

var (
	ifaceHeadRE = regexp.MustCompile(`^interface\s+(\w+)(?:\s+extends\s+([\w\s,]+?))?\s*\{`)
	aliasHeadRE = regexp.MustCompile(`^type\s+(\w+)\s*=`)
)

func (d *contractDoc) parseBlock(src string) error {
	s := strings.TrimSpace(src)
	for s != "" {
		switch {
		case ifaceHeadRE.MatchString(s):
			m := ifaceHeadRE.FindStringSubmatch(s)
			open := len(m[0]) - 1
			end := matchBrace(s, open)
			if end < 0 {
				return fmt.Errorf("unbalanced interface %s", m[1])
			}
			fields, err := parseFields(s[open+1 : end])
			if err != nil {
				return fmt.Errorf("interface %s: %w", m[1], err)
			}
			it := &tsIface{Name: m[1], Fields: fields}
			for _, e := range strings.Split(m[2], ",") {
				if e = strings.TrimSpace(e); e != "" {
					it.Extends = append(it.Extends, e)
				}
			}
			d.ifaces[it.Name] = it
			s = strings.TrimSpace(s[end+1:])
		case aliasHeadRE.MatchString(s):
			m := aliasHeadRE.FindStringSubmatch(s)
			rest := s[len(m[0]):]
			end := indexTop(rest, ';')
			if end < 0 {
				return fmt.Errorf("type %s: missing ;", m[1])
			}
			t, err := parseTSType(rest[:end])
			if err != nil {
				return fmt.Errorf("type %s: %w", m[1], err)
			}
			d.aliases[m[1]] = t
			s = strings.TrimSpace(rest[end+1:])
		default:
			return fmt.Errorf("unexpected TypeScript near %.40q", s)
		}
	}
	return nil
}

// matchBrace returns the index of the brace closing the one at open.
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits s at the separator bytes outside (), [], {}, <> and quotes.
func splitTop(s string, seps string) []string {
	var out []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inStr = !inStr
		case inStr:
		case strings.IndexByte("([{<", c) >= 0:
			depth++
		case strings.IndexByte(")]}>", c) >= 0:
			depth--
		case depth == 0 && strings.IndexByte(seps, c) >= 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func indexTop(s string, sep byte) int {
	parts := splitTop(s, string(sep))
	if len(parts) < 2 {
		return -1
	}
	return len(parts[0])
}

func parseFields(body string) ([]tsField, error) {
	var out []tsField
	for _, part := range splitTop(body, ";\n,") {
		part = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ","))
		if part == "" {
			continue
		}
		name, typ, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("bad member %q", part)
		}
		name = strings.Trim(strings.TrimSpace(name), `"`)
		name = strings.Replace(name, `"?`, "?", 1)
		f := tsField{Name: strings.TrimSuffix(name, "?"), Optional: strings.HasSuffix(name, "?")}
		t, err := parseTSType(typ)
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", f.Name, err)
		}
		f.Type = t
		out = append(out, f)
	}
	return out, nil
}

func parseTSType(s string) (*tsType, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, `\|`, "|"))
	if s == "" {
		return nil, fmt.Errorf("empty type")
	}
	if alts := splitTop(s, "|"); len(alts) > 1 {
		u := &tsType{kind: "union"}
		for _, a := range alts {
			t, err := parseTSType(a)
			if err != nil {
				return nil, err
			}
			u.alts = append(u.alts, t)
		}
		return u, nil
	}
	if parts := splitTop(s, "&"); len(parts) > 1 {
		x := &tsType{kind: "intersection"}
		for _, p := range parts {
			t, err := parseTSType(p)
			if err != nil {
				return nil, err
			}
			x.alts = append(x.alts, t)
		}
		return x, nil
	}
	switch {
	case s == "true" || s == "false":
		return &tsType{kind: "boollit", blit: s == "true"}, nil
	case strings.HasSuffix(s, "[]"):
		e, err := parseTSType(s[:len(s)-2])
		return &tsType{kind: "array", elem: e}, err
	case strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")"):
		return parseTSType(s[1 : len(s)-1])
	case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
		f, err := parseFields(s[1 : len(s)-1])
		return &tsType{kind: "object", fields: f}, err
	case strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`):
		return &tsType{kind: "lit", lit: s[1 : len(s)-1]}, nil
	case strings.HasPrefix(s, "Record<"):
		return &tsType{kind: "record"}, nil
	case strings.HasPrefix(s, "Page<") && strings.HasSuffix(s, ">"):
		e, err := parseTSType(s[len("Page<") : len(s)-1])
		return &tsType{kind: "page", elem: e}, err
	case s == "string" || s == "number" || s == "boolean" || s == "unknown" || s == "null":
		return &tsType{kind: "prim", name: s}, nil
	case regexp.MustCompile(`^\w+$`).MatchString(s):
		return &tsType{kind: "ref", name: s}, nil
	}
	return nil, fmt.Errorf("unsupported type %q", s)
}

var (
	sectionRE = regexp.MustCompile("(?m)^## \\d+\\. .*?`(/api/[a-z0-9/]+)`")
	statusRE  = regexp.MustCompile(`^(\d{3})\s*(.*)$`)
	// routeCellRE matches the first cell of an endpoint row.
	routeCellRE = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) /`)
)

// parseRoutes reads the endpoint tables: | `METHOD path` | body | response |.
func (d *contractDoc) parseRoutes(md string) error {
	base := ""
	for _, line := range strings.Split(md, "\n") {
		if m := sectionRE.FindStringSubmatch(line); m != nil {
			base = m[1]
			continue
		}
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := splitRow(line)
		if len(cells) < 3 {
			continue
		}
		route := firstCode(cells[0])
		if !routeCellRE.MatchString(route) {
			continue // another table (e.g. the e-mail kinds)
		}
		method, path, ok := strings.Cut(route, " ")
		if !ok {
			return fmt.Errorf("bad route cell %q", cells[0])
		}
		if !strings.HasPrefix(path, "/api/") {
			path = base + path
		}
		respCell := cells[len(cells)-1]
		expr := firstCode(respCell)
		spec := routeSpec{Raw: respCell}
		if m := statusRE.FindStringSubmatch(expr); m != nil {
			fmt.Sscan(m[1], &spec.Status)
			expr = strings.TrimSpace(m[2])
		}
		if expr != "" {
			t, err := parseTSType(expr)
			if err != nil {
				return fmt.Errorf("%s %s: %w", method, path, err)
			}
			spec.Type = t
		}
		d.routes[method+" "+path] = spec
	}
	if len(d.routes) == 0 {
		return fmt.Errorf("no endpoint tables found")
	}
	return nil
}

// splitRow splits a markdown table row at unescaped pipes.
func splitRow(line string) []string {
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteString(`\|`)
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	// Drop the empty cells before the first and after the last pipe.
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	return cells
}

func firstCode(cell string) string {
	i := strings.IndexByte(cell, '`')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(cell[i+1:], '`')
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(cell[i+1 : i+1+j])
}

var errorEnvelopeRE = regexp.MustCompile("Errors: non-2xx with `([^`]+)`")

// errorEnvelope is the documented error body type.
func (d *contractDoc) errorEnvelope(t testing.TB) *tsType {
	t.Helper()
	raw, err := os.ReadFile(apiDocPath(t))
	require.NoError(t, err)
	m := errorEnvelopeRE.FindStringSubmatch(string(raw))
	require.NotNil(t, m, "error envelope in docs/api.md")
	typ, err := parseTSType(m[1])
	require.NoError(t, err)
	return typ
}

// fieldsOf returns the members of an interface including inherited ones.
func (d *contractDoc) fieldsOf(name string) ([]tsField, bool) {
	it, ok := d.ifaces[name]
	if !ok {
		return nil, false
	}
	var out []tsField
	for _, base := range it.Extends {
		f, ok := d.fieldsOf(base)
		if !ok {
			return nil, false
		}
		out = append(out, f...)
	}
	return append(out, it.Fields...), true
}

// pageFields are the members of `Page<T>` (docs/api.md conventions).
func pageFields(elem *tsType) []tsField {
	return []tsField{
		{Name: "items", Type: &tsType{kind: "array", elem: elem}},
		{Name: "total", Type: &tsType{kind: "prim", name: "number"}},
		{Name: "page", Type: &tsType{kind: "prim", name: "number"}},
		{Name: "page_size", Type: &tsType{kind: "prim", name: "number"}},
	}
}

// objectFields returns the members of an object type expression (an inline
// object, `Page<T>` or a reference to an interface / object alias).
func (d *contractDoc) objectFields(t *tsType) ([]tsField, bool) {
	switch t.kind {
	case "object":
		return t.fields, true
	case "page":
		return pageFields(t.elem), true
	case "ref":
		if a, ok := d.aliases[t.name]; ok {
			return d.objectFields(a)
		}
		return d.fieldsOf(t.name)
	}
	return nil, false
}

// check validates JSON value v (as decoded into any) against t and returns
// every mismatch, each prefixed with its JSON path.
func (d *contractDoc) check(path string, v any, t *tsType) []string {
	switch t.kind {
	case "union":
		var best []string
		var lits []string
		for _, alt := range t.alts {
			errs := d.check(path, v, alt)
			if len(errs) == 0 {
				return nil
			}
			if alt.kind == "lit" {
				lits = append(lits, alt.lit)
				continue
			}
			if alt.kind == "prim" && alt.name == "null" && v != nil {
				continue
			}
			if best == nil || len(errs) < len(best) {
				best = errs
			}
		}
		if best == nil {
			best = []string{fmt.Sprintf("%s: want one of %q, got %v", path, lits, v)}
		}
		return best
	case "prim":
		ok := false
		switch t.name {
		case "string":
			_, ok = v.(string)
		case "number":
			_, ok = v.(float64)
		case "boolean":
			_, ok = v.(bool)
		case "null":
			ok = v == nil
		case "unknown":
			ok = true
		}
		if !ok {
			return []string{fmt.Sprintf("%s: want %s, got %s", path, t.name, jsonKindOf(v))}
		}
		return nil
	case "lit":
		if s, ok := v.(string); !ok || s != t.lit {
			return []string{fmt.Sprintf("%s: want %q, got %v", path, t.lit, v)}
		}
		return nil
	case "boollit":
		if b, ok := v.(bool); !ok || b != t.blit {
			return []string{fmt.Sprintf("%s: want %v, got %v", path, t.blit, v)}
		}
		return nil
	case "intersection":
		// Every part must be an object type; the value has exactly the
		// union of their members.
		var all []tsField
		for _, part := range t.alts {
			f, ok := d.objectFields(part)
			if !ok {
				return []string{fmt.Sprintf("%s: intersection part is not an object type", path)}
			}
			all = append(all, f...)
		}
		return d.checkObject(path, v, all)
	case "record":
		if _, ok := v.(map[string]any); !ok {
			return []string{fmt.Sprintf("%s: want object, got %s", path, jsonKindOf(v))}
		}
		return nil
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want array, got %s", path, jsonKindOf(v))}
		}
		var errs []string
		for i, e := range arr {
			errs = append(errs, d.check(fmt.Sprintf("%s[%d]", path, i), e, t.elem)...)
		}
		return errs
	case "page":
		return d.checkObject(path, v, pageFields(t.elem))
	case "object":
		return d.checkObject(path, v, t.fields)
	case "ref":
		if a, ok := d.aliases[t.name]; ok {
			return d.check(path, v, a)
		}
		fields, ok := d.fieldsOf(t.name)
		if !ok {
			return []string{fmt.Sprintf("%s: unknown type %s in docs/api.md", path, t.name)}
		}
		return d.checkObject(path, v, fields)
	}
	return []string{fmt.Sprintf("%s: unsupported type kind %s", path, t.kind)}
}

func (d *contractDoc) checkObject(path string, v any, fields []tsField) []string {
	obj, ok := v.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: want object, got %s", path, jsonKindOf(v))}
	}
	var errs []string
	known := map[string]bool{}
	for _, f := range fields {
		known[f.Name] = true
		val, present := obj[f.Name]
		if !present {
			if !f.Optional {
				errs = append(errs, fmt.Sprintf("%s.%s: missing", path, f.Name))
			}
			continue
		}
		errs = append(errs, d.check(path+"."+f.Name, val, f.Type)...)
	}
	var extra []string
	for k := range obj {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		errs = append(errs, fmt.Sprintf("%s.%s: not in the contract", path, k))
	}
	return errs
}

func jsonKindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// TestContractDocParses needs no database: it makes sure docs/api.md can be
// read by the contract checker and that the checker catches mistakes.
func TestContractDocParses(t *testing.T) {
	doc := loadContract(t)
	for _, name := range []string{"Invoice", "Quote", "Transfer", "AssetInfo", "CreateInvoice", "CheckoutView",
		"CheckoutAsset", "AdminUser", "Merchant", "ApiKey", "Wallet", "MerchantWallet", "Chain", "Asset", "InvoiceDetail",
		"AdminInvoice", "WebhookEvent", "AuditLog", "Settings", "Overview", "MerchantOverview", "LoginChallenge", "AdminSession",
		"EmailMessage"} {
		_, ok := doc.fieldsOf(name)
		require.True(t, ok, "interface %s", name)
	}
	require.Contains(t, doc.aliases, "InvoiceStatus")
	require.Len(t, doc.routes, 5+2+56, "every endpoint row is parsed")
	require.Equal(t, "intersection", doc.routes["GET /api/admin/auth/me"].Type.kind)
	require.Equal(t, "intersection", doc.routes["GET /api/admin/email/outbox/{id}"].Type.kind)
	mo := doc.routes["GET /api/admin/overview/merchants"].Type
	require.Equal(t, "intersection", mo.kind)
	require.Equal(t, "page", mo.alts[0].kind)
	paged := map[string]any{"items": []any{}, "total": float64(0), "page": float64(1), "page_size": float64(20)}
	require.Len(t, doc.check("$", paged, mo), 1, "missing intersection member next to Page<T>")
	paged["range_days"] = float64(30)
	require.Empty(t, doc.check("$", paged, mo))
	r := doc.routes["POST /api/admin/merchants"]
	require.Equal(t, 201, r.Status)
	require.Equal(t, "object", r.Type.kind)
	require.Nil(t, doc.routes["DELETE /api/admin/wallets/{id}"].Type)
	require.Equal(t, 204, doc.routes["DELETE /api/admin/wallets/{id}"].Status)
	require.Equal(t, "page", doc.routes["GET /api/v1/invoices"].Type.kind)

	// The checker reports missing, extra, misnamed and mistyped fields.
	asset := map[string]any{"id": "x", "symbol": "X", "name": "X", "chain_id": "c", "chain_name": "C", "family": "evm",
		"network_id": "", "is_testnet": false, "contract_address": "", "decimals": float64(6), "is_stablecoin": true}
	require.Empty(t, doc.check("$", asset, &tsType{kind: "ref", name: "AssetInfo"}))
	bad := map[string]any{}
	for k, v := range asset {
		bad[k] = v
	}
	delete(bad, "is_stablecoin")
	bad["is_stable"] = true
	bad["decimals"] = "6"
	bad["family"] = "bitcoin"
	errs := doc.check("$", bad, &tsType{kind: "ref", name: "AssetInfo"})
	require.Len(t, errs, 4, "%v", errs)

	// Intersections and boolean literals.
	me, err := parseTSType("AssetInfo & { extra: boolean }")
	require.NoError(t, err)
	withExtra := map[string]any{"extra": true}
	for k, v := range asset {
		withExtra[k] = v
	}
	require.Empty(t, doc.check("$", withExtra, me))
	require.Len(t, doc.check("$", asset, me), 1, "missing intersection member")
	ch := &tsType{kind: "ref", name: "LoginChallenge"}
	good := map[string]any{"two_factor_required": true, "challenge_token": "t", "expires_at": "2026-01-01T00:00:00Z"}
	require.Empty(t, doc.check("$", good, ch))
	good["two_factor_required"] = false
	require.Len(t, doc.check("$", good, ch), 1, "true literal")
}
