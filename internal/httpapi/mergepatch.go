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

// Apply merges the member field of the patch into current, a JSON document
// (nil or JSON null when the target has no value yet), with the full RFC 7396
// MergePatch algorithm, and returns the merged document. Use it for a member
// that holds a nested object: objects merge recursively and null deletes a
// member, while arrays and scalars replace the target whole. It reports a
// validation error naming field when the patch value or current is not valid
// JSON.
func (p MergePatch) Apply(field string, current json.RawMessage) (json.RawMessage, error) {
	out, err := ApplyMergePatch(current, p[field])
	if err != nil {
		return nil, Validation(field, field+" is not valid JSON")
	}
	return out, nil
}

// ApplyMergePatch is RFC 7396 MergePatch(target, patch) on JSON documents.
// When patch is an object, target is treated as an empty object unless it is
// one too; each patch member is deleted from the result when null and merged
// recursively otherwise. When patch is anything else (array, scalar, null) it
// replaces target. An empty target means "no value" (JSON null). Keys come out
// sorted (encoding/json map order); duplicate keys in a document take the last.
func ApplyMergePatch(target, patch json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(target)) == 0 {
		target = json.RawMessage("null")
	}
	var t, p any
	if err := json.Unmarshal(target, &t); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	return json.Marshal(mergeValue(t, p))
}

func mergeValue(target, patch any) any {
	po, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	to, ok := target.(map[string]any)
	if !ok {
		to = map[string]any{}
	}
	for k, v := range po {
		if v == nil {
			delete(to, k)
			continue
		}
		to[k] = mergeValue(to[k], v)
	}
	return to
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
	var raw json.RawMessage
	if err := DecodeJSON(r, &raw); err != nil {
		return nil, err
	}
	var p MergePatch
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || json.Unmarshal(raw, &p) != nil {
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
