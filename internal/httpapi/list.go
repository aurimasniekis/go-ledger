package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

// List conventions (ADR-0011): cursor pagination and filtering.
//
//	GET /api/v1/things?limit=20&kind=a&kind=b&name[prefix]=Net&cursor=...
//
// limit is 1..[MaxLimit] (default [DefaultLimit]). cursor is the opaque
// next_cursor of the previous page and is valid only with the same filters.
// Each other parameter is a filter: "field=value" tests equality (repeat it
// for any of several values) and "field[op]=value" applies op, one of the
// [FilterOp] values the endpoint documents for that field. Unknown
// parameters, operators and repeated non-equality filters are 400 errors, so a
// typo never silently widens a result. Items come in a fixed documented order
// (by ID, which is creation order, unless the endpoint says otherwise).

// Page size limits.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// FilterOp is a filter operator.
type FilterOp string

// Filter operators. Eq is written without brackets ("field=value").
const (
	OpEq     FilterOp = "eq"
	OpPrefix FilterOp = "prefix"
	OpGt     FilterOp = "gt"
	OpGte    FilterOp = "gte"
	OpLt     FilterOp = "lt"
	OpLte    FilterOp = "lte"
)

// FilterSpec declares one filterable field: the operators it allows and an
// optional value check, whose error message is sent to the client.
type FilterSpec struct {
	Ops   []FilterOp
	Check func(value string) error
}

// ListSpec declares an endpoint's filters by field name.
type ListSpec struct {
	Filters map[string]FilterSpec
}

// Filter is one parsed filter. Values has one entry, except for OpEq, where
// it lists the accepted values (any of them matches), sorted.
type Filter struct {
	Field  string
	Op     FilterOp
	Values []string
}

// ListQuery is a parsed list request.
type ListQuery struct {
	// Limit is the page size. Fetch Limit+1 rows so [NewPage] can tell
	// whether another page follows.
	Limit int
	// After is the ID of the last item of the previous page ("" for the
	// first page): return only items after it in the list order.
	After string
	// Filters are sorted by field, then operator.
	Filters []Filter
}

// Filter returns the values of the field's filter with op, or nil.
func (q ListQuery) Filter(field string, op FilterOp) []string {
	for _, f := range q.Filters {
		if f.Field == field && f.Op == op {
			return f.Values
		}
	}
	return nil
}

var filterKey = regexp.MustCompile(`^([a-z][a-z0-9_]*)(?:\[([a-z]+)\])?$`)

// ParseList parses limit, cursor and the filters spec allows from the query
// string. Errors are 400s naming the offending parameter.
func ParseList(r *http.Request, spec ListSpec) (ListQuery, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ListQuery{}, Validation("", "the query string is malformed")
	}
	q := ListQuery{Limit: DefaultLimit}
	byKey := map[[2]string]*Filter{}
	var cursor string
	for key, vals := range values {
		switch key {
		case "limit":
			if len(vals) != 1 {
				return ListQuery{}, Validation("limit", "limit must be given once")
			}
			n, err := strconv.Atoi(vals[0])
			if err != nil || n < 1 || n > MaxLimit {
				return ListQuery{}, Validation("limit", fmt.Sprintf("limit must be an integer from 1 to %d", MaxLimit))
			}
			q.Limit = n
			continue
		case "cursor":
			if len(vals) != 1 || vals[0] == "" {
				return ListQuery{}, Validation("cursor", "cursor must be given once and not be empty")
			}
			cursor = vals[0]
			continue
		}
		m := filterKey.FindStringSubmatch(key)
		if m == nil {
			return ListQuery{}, Validation(key, "unknown query parameter")
		}
		field, op := m[1], FilterOp(m[2])
		if op == "" {
			op = OpEq
		}
		fs, ok := spec.Filters[field]
		if !ok {
			return ListQuery{}, Validation(key, "unknown query parameter")
		}
		if !slices.Contains(fs.Ops, op) {
			return ListQuery{}, Validation(key, fmt.Sprintf("operator %q is not supported for %s", op, field))
		}
		if op != OpEq && len(vals) > 1 {
			return ListQuery{}, Validation(key, "this filter must be given once")
		}
		for _, v := range vals {
			if v == "" {
				return ListQuery{}, Validation(key, "filter value must not be empty")
			}
			if fs.Check != nil {
				if err := fs.Check(v); err != nil {
					return ListQuery{}, Validation(key, err.Error())
				}
			}
		}
		k := [2]string{field, string(op)}
		f := byKey[k]
		if f == nil {
			f = &Filter{Field: field, Op: op}
			byKey[k] = f
		}
		f.Values = append(f.Values, vals...)
	}
	for _, f := range byKey {
		sort.Strings(f.Values)
		f.Values = slices.Compact(f.Values)
		q.Filters = append(q.Filters, *f)
	}
	sort.Slice(q.Filters, func(i, j int) bool {
		if q.Filters[i].Field != q.Filters[j].Field {
			return q.Filters[i].Field < q.Filters[j].Field
		}
		return q.Filters[i].Op < q.Filters[j].Op
	})
	if cursor != "" {
		after, err := decodeCursor(cursor, q.filterHash())
		if err != nil {
			return ListQuery{}, err
		}
		q.After = after
	}
	return q, nil
}

// cursorBody is the decoded form of a cursor. Clients must treat cursors as
// opaque; the format may change without notice.
type cursorBody struct {
	After  string `json:"a"`
	Filter string `json:"f"`
}

const maxCursorLen = 512

// filterHash fingerprints the filters, so a cursor is refused with different
// filters (that would skip or repeat items silently).
func (q ListQuery) filterHash() string {
	var b strings.Builder
	for _, f := range q.Filters {
		b.WriteString(url.QueryEscape(f.Field))
		b.WriteByte('[')
		b.WriteString(string(f.Op))
		b.WriteByte(']')
		for _, v := range f.Values {
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
		b.WriteByte('&')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

// Cursor returns the cursor for the page after the item with ID lastID.
func (q ListQuery) Cursor(lastID string) string {
	raw, _ := json.Marshal(cursorBody{After: lastID, Filter: q.filterHash()}) // cannot fail
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s, filterHash string) (string, error) {
	bad := Validation("cursor", "cursor is not valid; start again from the first page")
	if len(s) > maxCursorLen {
		return "", bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", bad
	}
	var c cursorBody
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || !resource.ValidID(c.After) {
		return "", bad
	}
	if c.Filter != filterHash {
		return "", Validation("cursor", "cursor belongs to a list with different filters; start again from the first page")
	}
	return c.After, nil
}

// Page is the body of every list response. NextCursor is null on the last
// page.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// NewPage builds a page from up to q.Limit+1 items fetched in list order: when
// there are more than q.Limit, the extra one is dropped and NextCursor points
// after the last item kept.
func NewPage[T any](items []T, q ListQuery, id func(T) string) Page[T] {
	p := Page[T]{Items: items}
	if p.Items == nil {
		p.Items = []T{}
	}
	if len(p.Items) > q.Limit {
		p.Items = p.Items[:q.Limit]
		c := q.Cursor(id(p.Items[len(p.Items)-1]))
		p.NextCursor = &c
	}
	return p
}

// WriteJSON sends v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
