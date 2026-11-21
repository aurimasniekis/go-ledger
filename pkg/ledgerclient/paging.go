package ledgerclient

import (
	"context"
	"iter"
	"net/url"
	"strconv"
)

// Page is one page of a list. NextCursor is nil on the last page.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// ListOptions are the paging parameters shared by every list.
type ListOptions struct {
	// Limit is the page size, 1-200 (0: the server default of 50).
	Limit int
	// Cursor is the previous page's NextCursor. It is valid only with the
	// same filters; the server refuses it otherwise.
	Cursor string
}

func (o ListOptions) apply(q url.Values) {
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
}

// Walk returns every item of a list, fetching pages lazily. fetch gets the
// cursor of the page wanted ("" for the first) and must keep the filters
// fixed, as the server requires. Iteration stops at the first error, which is
// yielded once. Per ADR-0015 a walk is exact for items that existed when it
// began; items created during it may or may not appear.
func Walk[T any](ctx context.Context, fetch func(ctx context.Context, cursor string) (Page[T], error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		cursor := ""
		for {
			page, err := fetch(ctx, cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, it := range page.Items {
				if !yield(it, nil) {
					return
				}
			}
			if page.NextCursor == nil || *page.NextCursor == "" {
				return
			}
			cursor = *page.NextCursor
		}
	}
}

// Collect gathers every item of seq, or returns the first error.
func Collect[T any](seq iter.Seq2[T, error]) ([]T, error) {
	var out []T
	for it, err := range seq {
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}
