package sqlmeta

import (
	"sort"
	"strings"
)

// DefaultMaxBodyBytes is the default cap on the number of SQL bytes analysed.
// Bodies larger than this are truncated before analysis; the head is still
// scanned (so the statement type and leading table refs are recovered) but the
// scan can never run longer than O(maxBodyBytes). 256 KiB comfortably covers
// real queries while bounding worst-case work on a hostile body.
const DefaultMaxBodyBytes = 256 * 1024

// Heuristic is the default best-effort SQLAnalyzer: a single-pass, comment- and
// string-literal aware tokenizer. It is stateless and safe for concurrent use.
// See docs/CONVENTIONS.md for the design rationale and the upgrade seam.
type Heuristic struct {
	// maxBodyBytes caps the analysed length. Zero means DefaultMaxBodyBytes.
	maxBodyBytes int
	// truncated is set by Analyze (via the returned flag, not stored) — kept out
	// of the struct so Heuristic stays stateless/concurrent-safe.
}

// NewHeuristic returns a Heuristic with the given byte cap. A non-positive cap
// falls back to DefaultMaxBodyBytes.
func NewHeuristic(maxBodyBytes int) *Heuristic {
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	return &Heuristic{maxBodyBytes: maxBodyBytes}
}

// Analyze implements SQLAnalyzer. It never returns an error and never panics.
func (h *Heuristic) Analyze(sql, defaultCatalog, defaultSchema string) QueryMeta {
	meta, _ := h.analyze(sql, defaultCatalog, defaultSchema)
	return meta
}

// AnalyzeWithTruncation is like Analyze but also reports whether the body was
// truncated to the byte cap before analysis. Used by the metrics wiring to
// increment the truncation counter without re-measuring the body.
func (h *Heuristic) AnalyzeWithTruncation(sql, defaultCatalog, defaultSchema string) (QueryMeta, bool) {
	return h.analyze(sql, defaultCatalog, defaultSchema)
}

func (h *Heuristic) analyze(sql, defaultCatalog, defaultSchema string) (QueryMeta, bool) {
	limit := h.maxBodyBytes
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	truncated := false
	if len(sql) > limit {
		sql = sql[:limit]
		truncated = true
	}

	toks := tokenize(sql)
	if len(toks) == 0 {
		return emptyMeta(), truncated
	}

	queryType, ok := statementType(toks)
	if !ok {
		return emptyMeta(), truncated
	}

	cats := newStringSet()
	schemas := newStringSet()
	catSchemas := newStringSet()
	tables := newStringSet()

	for _, ref := range extractTableRefs(toks) {
		c, s, t := qualify(ref, defaultCatalog, defaultSchema)
		if t == "" {
			continue
		}
		if c != "" {
			cats.add(c)
		}
		if s != "" {
			schemas.add(s)
		}
		if c != "" && s != "" {
			catSchemas.add(c + "." + s)
		}
		// Tables holds the most-qualified form we can build.
		tables.add(joinName(c, s, t))
	}

	return QueryMeta{
		QueryType:      queryType,
		Category:       categoryFor(queryType),
		Catalogs:       cats.sorted(),
		Schemas:        schemas.sorted(),
		CatalogSchemas: catSchemas.sorted(),
		Tables:         tables.sorted(),
		ParseOK:        true,
	}, truncated
}

// --- tokenization ---

// token is a single significant lexical element: either a bare/keyword word, a
// quoted identifier (the surrounding quotes already stripped and escapes
// resolved), or punctuation (".", "(", ")", ","). Comments and string literals
// are dropped entirely during tokenization.
type token struct {
	text   string
	quoted bool // true for "double-quoted" or `backtick`/[bracket] identifiers
}

// tokenize scans sql once, dropping line comments (-- …), block comments
// (/* … */) and string literals ('…'), and emitting words, quoted identifiers,
// and the structural punctuation we care about (".", "(", ")", ",").
func tokenize(sql string) []token {
	var toks []token
	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '-' && i+1 < n && sql[i+1] == '-':
			// line comment to end of line
			i += 2
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			// block comment to */
			i += 2
			for i+1 < n && (sql[i] != '*' || sql[i+1] != '/') {
				i++
			}
			i += 2
			if i > n {
				i = n
			}
		case c == '\'':
			// single-quoted string literal; '' is an escaped quote.
			i++
			for i < n {
				if sql[i] == '\'' {
					if i+1 < n && sql[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"':
			id, next := scanDelimited(sql, i, '"', '"')
			toks = append(toks, token{text: id, quoted: true})
			i = next
		case c == '`':
			id, next := scanDelimited(sql, i, '`', '`')
			toks = append(toks, token{text: id, quoted: true})
			i = next
		case c == '[':
			id, next := scanDelimited(sql, i, '[', ']')
			toks = append(toks, token{text: id, quoted: true})
			i = next
		case c == '.' || c == '(' || c == ')' || c == ',':
			toks = append(toks, token{text: string(c)})
			i++
		case isIdentChar(c):
			start := i
			for i < n && isIdentChar(sql[i]) {
				i++
			}
			toks = append(toks, token{text: sql[start:i]})
		default:
			// whitespace and any other punctuation: skip.
			i++
		}
	}
	return toks
}

// scanDelimited reads a delimited identifier starting at sql[start] (which is
// the open delimiter). For symmetric delimiters ('"', '`') a doubled delimiter
// is an escaped literal. Returns the inner text and the index just past the
// closing delimiter.
func scanDelimited(sql string, start int, open, close byte) (string, int) {
	n := len(sql)
	var b strings.Builder
	i := start + 1
	for i < n {
		ch := sql[i]
		if ch == close {
			// Doubled closing delimiter (only meaningful when open==close) is an escape.
			if open == close && i+1 < n && sql[i+1] == close {
				b.WriteByte(close)
				i += 2
				continue
			}
			i++
			return b.String(), i
		}
		b.WriteByte(ch)
		i++
	}
	return b.String(), i
}

// isIdentChar reports whether c can be part of a bare identifier or keyword.
// Trino bare identifiers allow letters, digits and underscore; we also accept
// '@' and '#' defensively (some sources prefix temp names) but they are rare.
func isIdentChar(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// --- statement classification ---

// statementType returns the upper-case leading keyword that determines the
// query type, skipping an optional leading WITH … CTE block to find the main
// statement keyword (the keyword after the CTE definitions). Returns ok=false
// when the first significant token is not a recognised statement keyword.
func statementType(toks []token) (string, bool) {
	idx := 0
	// Skip a leading EXPLAIN — but report EXPLAIN as the type.
	first := keywordAt(toks, idx)
	if first == "EXPLAIN" {
		return "EXPLAIN", true
	}
	if first == "WITH" {
		// Skip the CTE block to the main statement. The main statement keyword
		// is the first top-level SELECT/INSERT/… after the CTE list, found by
		// scanning past balanced parentheses.
		mk, mok := mainAfterWith(toks)
		if mok {
			return mk, true
		}
		// WITH with no recognisable follow-on statement: treat as a read (CTE
		// queries are SELECT-shaped) so routing still classifies it.
		return "SELECT", true
	}
	if isStatementKeyword(first) {
		return first, true
	}
	return "", false
}

// mainAfterWith scans past a leading WITH … CTE list (handling nested parens)
// and returns the main statement keyword that follows.
func mainAfterWith(toks []token) (string, bool) {
	depth := 0
	// Start after the WITH token.
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		switch t.text {
		case "(":
			depth++
			continue
		case ")":
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 || t.quoted {
			continue
		}
		kw := strings.ToUpper(t.text)
		if isStatementKeyword(kw) {
			return kw, true
		}
	}
	return "", false
}

// keywordAt returns the upper-cased text of the first non-quoted token at/after
// idx, or "" if none.
func keywordAt(toks []token, idx int) string {
	for i := idx; i < len(toks); i++ {
		if toks[i].quoted {
			continue
		}
		return strings.ToUpper(toks[i].text)
	}
	return ""
}

// statementKeywords is the set of leading keywords we recognise as a statement
// type. Multi-word statements (CREATE TABLE, DROP SCHEMA) collapse to the head.
var statementKeywords = map[string]struct{}{
	"SELECT": {}, "INSERT": {}, "UPDATE": {}, "DELETE": {}, "MERGE": {},
	"CREATE": {}, "DROP": {}, "ALTER": {}, "TRUNCATE": {}, "EXPLAIN": {},
	"SHOW": {}, "DESCRIBE": {}, "USE": {}, "CALL": {}, "SET": {}, "RESET": {},
	"GRANT": {}, "REVOKE": {}, "COMMENT": {}, "ANALYZE": {}, "REFRESH": {},
	"VALUES": {}, "PREPARE": {}, "EXECUTE": {}, "DEALLOCATE": {}, "START": {},
	"COMMIT": {}, "ROLLBACK": {},
}

func isStatementKeyword(kw string) bool {
	_, ok := statementKeywords[kw]
	return ok
}

// CategoryForType maps an upper-case statement type to its coarse routing
// category. Exported so callers that already have a QueryType (e.g. one provided
// by a future SQL-aware gateway in the proto) can derive the category without
// re-running the tokenizer.
func CategoryForType(queryType string) Category {
	return categoryFor(strings.ToUpper(strings.TrimSpace(queryType)))
}

// categoryFor maps a statement type to a coarse routing category.
func categoryFor(queryType string) Category {
	switch queryType {
	case "SELECT", "VALUES", "DESCRIBE", "SHOW":
		return CategoryRead
	case "INSERT", "UPDATE", "DELETE", "MERGE", "TRUNCATE":
		return CategoryWrite
	case "CREATE", "DROP", "ALTER", "COMMENT", "REFRESH", "ANALYZE":
		return CategoryDDL
	case "EXPLAIN":
		return CategoryExplain
	default:
		return CategoryOther
	}
}

// --- table-reference extraction ---

// tableKeywords marks the keywords after which a table reference is expected.
// "MERGE INTO" and "INSERT INTO" both surface as INTO; UPDATE/FROM/JOIN are the
// other anchors. TABLE (CREATE/DROP/ALTER TABLE, INSERT INTO TABLE) is included
// so DDL targets are captured.
var tableKeywords = map[string]struct{}{
	"FROM": {}, "JOIN": {}, "INTO": {}, "UPDATE": {}, "TABLE": {},
}

// refStopKeywords end a table-reference position: once we see one of these, the
// identifier list that followed a FROM/JOIN/… anchor is over.
var refStopKeywords = map[string]struct{}{
	"WHERE": {}, "ON": {}, "USING": {}, "GROUP": {}, "ORDER": {}, "HAVING": {},
	"LIMIT": {}, "UNION": {}, "INTERSECT": {}, "EXCEPT": {}, "AS": {}, "SET": {},
	"VALUES": {}, "SELECT": {}, "WITH": {}, "LEFT": {}, "RIGHT": {}, "INNER": {},
	"OUTER": {}, "CROSS": {}, "FULL": {}, "NATURAL": {}, "AND": {}, "OR": {},
	"OFFSET": {}, "FETCH": {}, "WINDOW": {}, "QUALIFY": {}, "TABLESAMPLE": {},
}

// rawRef is a 1/2/3-part identifier collected from a table position, preserving
// whether each part was quoted (so we don't mistake a quoted "FROM" for the
// keyword).
type rawRef struct {
	parts []string
}

// extractTableRefs walks the tokens and collects identifier references that
// appear immediately after a table-position keyword (FROM/JOIN/INTO/UPDATE/
// TABLE). It handles "FROM a, b" lists and skips subquery-introducing "FROM (".
func extractTableRefs(toks []token) []rawRef {
	var refs []rawRef
	i := 0
	n := len(toks)
	for i < n {
		t := toks[i]
		if t.quoted {
			i++
			continue
		}
		kw := strings.ToUpper(t.text)
		if _, anchor := tableKeywords[kw]; !anchor {
			i++
			continue
		}
		// Special-case: CREATE/DROP/ALTER ... but ensure FROM/INTO etc. lead a ref.
		i++
		// A "FROM (" introduces a derived table / subquery, not a named table.
		// Skip the whole parenthesised group; its inner FROMs are caught by the
		// outer scan continuing past it.
		for i < n {
			// allow an optional leading "ONLY" / "TABLE" qualifier after INTO/FROM
			if !toks[i].quoted {
				w := strings.ToUpper(toks[i].text)
				if w == "ONLY" {
					i++
					continue
				}
			}
			break
		}
		if i < n && toks[i].text == "(" {
			// derived table — let the scan continue; inner refs handled by outer loop
			continue
		}
		// Collect a comma-separated list of references at this anchor.
		for i < n {
			ref, next, ok := readRef(toks, i)
			if !ok {
				break
			}
			refs = append(refs, ref)
			i = next
			// Skip an alias (optional "AS" + identifier) until a comma or stop.
			i = skipAlias(toks, i)
			if i < n && toks[i].text == "," {
				i++
				// allow "FROM a, (subquery)" — if next is "(", stop list.
				if i < n && toks[i].text == "(" {
					break
				}
				continue
			}
			break
		}
	}
	return refs
}

// readRef reads a dotted identifier reference (a, a.b, a.b.c) starting at idx.
// Returns the ref, the index after it, and ok=false if idx is not an identifier
// (e.g. it's a keyword, punctuation, or a subquery paren).
func readRef(toks []token, idx int) (rawRef, int, bool) {
	if idx >= len(toks) {
		return rawRef{}, idx, false
	}
	first := toks[idx]
	if !first.quoted {
		w := strings.ToUpper(first.text)
		if first.text == "(" || first.text == "," || first.text == ")" {
			return rawRef{}, idx, false
		}
		if _, stop := refStopKeywords[w]; stop {
			return rawRef{}, idx, false
		}
	}
	parts := []string{partText(first)}
	i := idx + 1
	for i+1 < len(toks) && toks[i].text == "." {
		next := toks[i+1]
		parts = append(parts, partText(next))
		i += 2
	}
	return rawRef{parts: parts}, i, true
}

// skipAlias advances past an optional table alias: an optional "AS" keyword
// followed by an identifier, but never consuming a comma, a stop keyword, or a
// new anchor keyword.
func skipAlias(toks []token, i int) int {
	if i >= len(toks) {
		return i
	}
	// Optional AS.
	if !toks[i].quoted && strings.ToUpper(toks[i].text) == "AS" {
		i++
		if i < len(toks) {
			i++ // consume alias identifier
		}
		return i
	}
	// Bare alias: an identifier that is not a keyword that would continue the
	// statement. Only consume it if it is plainly an alias (not a stop/anchor).
	if i < len(toks) {
		t := toks[i]
		if t.text == "," || t.text == "(" || t.text == ")" || t.text == "." {
			return i
		}
		if !t.quoted {
			w := strings.ToUpper(t.text)
			if _, stop := refStopKeywords[w]; stop {
				return i
			}
			if _, anchor := tableKeywords[w]; anchor {
				return i
			}
		}
		// Treat as a bare alias and consume it.
		return i + 1
	}
	return i
}

// partText returns the identifier text for a token, lower-casing bare
// identifiers (Trino folds unquoted names to lower case) but preserving the
// exact case of quoted identifiers.
func partText(t token) string {
	if t.quoted {
		return t.text
	}
	return strings.ToLower(t.text)
}

// --- name qualification ---

// qualify resolves a 1/2/3-part rawRef into (catalog, schema, table) using the
// defaults for any missing leading parts. Bare 1-part defaults fill schema and
// catalog when known; a 2-part ref fills only the catalog. Empty parts stay
// empty when no default is available.
func qualify(ref rawRef, defaultCatalog, defaultSchema string) (catalog, schema, table string) {
	dc := strings.ToLower(strings.TrimSpace(defaultCatalog))
	ds := strings.ToLower(strings.TrimSpace(defaultSchema))
	switch len(ref.parts) {
	case 1:
		return dc, ds, ref.parts[0]
	case 2:
		return dc, ref.parts[0], ref.parts[1]
	default: // 3 or more — take the last three
		p := ref.parts
		k := len(p)
		return p[k-3], p[k-2], p[k-1]
	}
}

// joinName joins the non-empty parts of a (catalog, schema, table) into a
// dotted name. Missing leading parts are dropped so an unqualified table still
// yields a usable "table" entry.
func joinName(catalog, schema, table string) string {
	parts := make([]string, 0, 3)
	if catalog != "" {
		parts = append(parts, catalog)
	}
	if schema != "" {
		parts = append(parts, schema)
	}
	parts = append(parts, table)
	return strings.Join(parts, ".")
}

// --- string set ---

type stringSet struct {
	m map[string]struct{}
}

func newStringSet() *stringSet { return &stringSet{m: map[string]struct{}{}} }

func (s *stringSet) add(v string) {
	if v == "" {
		return
	}
	s.m[v] = struct{}{}
}

func (s *stringSet) sorted() []string {
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
