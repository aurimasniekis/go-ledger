package httpapi

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
)

// MergePatchType is the media type of a partial update (RFC 7396, ADR-0015).
const MergePatchType = "application/merge-patch+json"

// MergePatch is a decoded JSON Merge Patch document: only the members the
// client sent. An absent member means "leave unchanged"; a member whose value
// is JSON null means "clear it" (allowed only for optional fields).
type MergePatch map[string]json.RawMessage

// Has reports whether the patch names field at all.
func (p MergePatch) Has(field string) bool { _, ok := p[field]; return ok }

// IsNull reports whether the patch sets field to JSON null.
func (p MergePatch) IsNull(field string) bool {
	raw, ok := p[field]
	return ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// Decode unmarshals the member field into dst. It reports a validation error
// naming field when the value has the wrong type.
func (p MergePatch) Decode(field string, dst any) error {
	if err := json.Unmarshal(p[field], dst); err != nil {
		return Validation(field, field+" has the wrong type")
	}
	return nil
}

// DecodeMergePatch reads the request body as a JSON Merge Patch. The
// Content-Type must be application/merge-patch+json (415 otherwise) and the
// body one JSON object. Members not in known are refused (400 naming the
// member), like DecodeJSON refuses unknown fields. The caller handles null per
// field: [MergePatch.IsNull].
func DecodeMergePatch(r *http.Request, known ...string) (MergePatch, error) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != MergePatchType {
		return nil, unsupportedMediaType("a partial update must be sent as " + MergePatchType)
	}
	var p MergePatch
	if err := DecodeJSON(r, &p); err != nil {
		return nil, err
	}
	if p == nil { // body was the literal null
		return nil, Validation("", "the patch must be a JSON object")
	}
	for k := range p {
		ok := false
		for _, f := range known {
			ok = ok || f == k
		}
		if !ok {
			return nil, Validation(k, "unknown field "+k)
		}
	}
	return p, nil
}
