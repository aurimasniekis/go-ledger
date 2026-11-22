package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/audit"
	"github.com/aurimasniekis/go-ledger/internal/domain/matches"
	moneypkg "github.com/aurimasniekis/go-ledger/internal/domain/money"
	"github.com/aurimasniekis/go-ledger/internal/store"
	matchstore "github.com/aurimasniekis/go-ledger/internal/store/matches"
)

type replacedMatchJSON struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Version int64  `json:"version"`
	History []struct {
		Action   string   `json:"action"`
		Replaced []string `json:"replaced_manual_payment_ids"`
		Restored []string `json:"restored_manual_payment_ids"`
		By       struct {
			Via *string `json:"via"`
		} `json:"by"`
	} `json:"history"`
}

func (m replacedMatchJSON) last() (action string, replaced, restored []string, via string) {
	if len(m.History) == 0 {
		return "", nil, nil, ""
	}
	l := m.History[len(m.History)-1]
	v := ""
	if l.By.Via != nil {
		v = *l.By.Via
	}
	return l.Action, l.Replaced, l.Restored, v
}

type manualJSON struct {
	ID                string  `json:"id"`
	ReplacedByMatchID *string `json:"replaced_by_match_id"`
}

// TestPaymentMatchConfirmReplacingAPI: POST /payment-matches/{id}/confirm-replacing
// (D45, ADR-0049) in a session. The occurrence is paid by the person's manual
// record; confirming the bank's suggestion "as the payment I recorded"
// replaces the record (kept, listed with replaced_by_match_id, not counted);
// unconfirm restores it and the occurrence's payment state. Refusals: no grant,
// another occurrence's record, a missing list, stale and missing versions.
func TestPaymentMatchConfirmReplacingAPI(t *testing.T) {
	f := newAPIFixture(t)
	f.now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	alice := map[string]string{"X-Test-User": f.ids["alice"]}
	reader := map[string]string{"X-Test-User": f.ids["bob"]}
	decode := func(body []byte, v any) {
		t.Helper()
		if err := json.Unmarshal(body, v); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
	}

	bank := importFakeBank(t, f)
	streamID := f.createCommitmentAt(t, earlier, `{"name":"Streaming","kind":"subscription","amount":{"amount":"13.49","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-01-01","match_tolerance":{"amount":"0.50","currency":"EUR"}}`)
	occs := f.occurrences(t, "month=2026-10&commitment_id="+streamID, "alice").Items
	oct := occs[0].ID
	nov := f.occurrences(t, "month=2026-11&commitment_id="+streamID, "alice").Items[0].ID
	ms := matchstore.New(f.db, store.NewAuditLog(f.db), func() time.Time { return f.now }, nil)
	sug, err := ms.Create(ctx, audit.System, matches.New{TransactionID: bank.tx(t, "STREAMING SERVICE").ID, OccurrenceID: oct, Amount: moneypkg.MustParse("12.99", "EUR"),
		Origin: matches.OriginAutoRule, State: matches.StateSuggested})
	if err != nil {
		t.Fatal(err)
	}
	record := func(occ, amount string) manualJSON {
		t.Helper()
		rec := f.do("POST", "/api/v1/occurrences/"+occ+"/manual-payments", `{"amount":{"amount":"`+amount+`","currency":"EUR"},"paid_on":"2026-10-05"}`, alice)
		if rec.Code != 201 {
			t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
		}
		var p manualJSON
		decode(rec.Body.Bytes(), &p)
		return p
	}
	p := record(oct, "13.49")
	other := record(nov, "13.49")
	before := f.getOccurrence(t, oct).Payment
	if before.Status != "paid" {
		t.Fatalf("paid by the manual record: %+v", before)
	}
	listed := func() manualJSON {
		t.Helper()
		rec := f.do("GET", "/api/v1/occurrences/"+oct+"/manual-payments", "", reader)
		var page struct{ Items []manualJSON }
		decode(rec.Body.Bytes(), &page)
		for _, m := range page.Items {
			if m.ID == p.ID {
				return m
			}
		}
		t.Fatalf("record not listed: %s", rec.Body.String())
		return manualJSON{}
	}
	base := "/api/v1/payment-matches/" + sug.Match.ID

	// Refusals change nothing.
	for name, c := range map[string]struct {
		body string
		hdr  map[string]string
		code int
		want string
	}{
		"read-only":          {`{"version":1,"manual_payment_ids":["` + p.ID + `"]}`, reader, 403, `"code":"forbidden"`},
		"no list":            {`{"version":1}`, alice, 400, `"field":"manual_payment_ids"`},
		"not identifiers":    {`{"version":1,"manual_payment_ids":["x"]}`, alice, 400, `"field":"manual_payment_ids"`},
		"another occurrence": {`{"version":1,"manual_payment_ids":["` + other.ID + `"]}`, alice, 409, `"code":"manual_payment_other_occurrence"`},
		"no version":         {`{"manual_payment_ids":["` + p.ID + `"]}`, alice, 428, ""},
		"stale version":      {`{"version":2,"manual_payment_ids":["` + p.ID + `"]}`, alice, 409, `"code":"version_conflict"`},
	} {
		rec := f.do("POST", base+"/confirm-replacing", c.body, c.hdr)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if l := listed(); l.ReplacedByMatchID != nil {
		t.Fatalf("a refusal replaced the record: %+v", l)
	}
	// A plain confirmation would count the payment twice.
	if rec := f.do("POST", base+"/confirm", `{"version":1}`, alice); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"occurrence_overpaid"`) {
		t.Errorf("plain confirm: %d %s", rec.Code, rec.Body.String())
	}

	rec := f.do("POST", base+"/confirm-replacing", `{"version":1,"manual_payment_ids":["`+p.ID+`"],"reason":"the payment I recorded"}`, alice)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("confirm-replacing: %d %s", rec.Code, rec.Body.String())
	}
	var c replacedMatchJSON
	decode(rec.Body.Bytes(), &c)
	if a, rep, _, v := c.last(); c.State != "confirmed" || a != "confirmed" || strings.Join(rep, ",") != p.ID || v != "session" {
		t.Fatalf("confirmed replacing: %s", rec.Body.String())
	}
	if pay := f.getOccurrence(t, oct).Payment; pay.Status != "paid" || pay.EvidenceCount != 1 {
		t.Errorf("paid by the bank match: %+v", pay)
	}
	if l := listed(); l.ReplacedByMatchID == nil || *l.ReplacedByMatchID != sug.Match.ID {
		t.Errorf("listed as replaced: %+v", l)
	}
	recs, err := store.NewAuditLog(f.db).List(ctx, audit.Filter{Actions: []audit.Action{audit.ManualPaymentReplaced}, Limit: 10})
	if err != nil || len(recs) != 1 || recs[0].Details["payment_id"] != p.ID || recs[0].Details["via"] != "session" || recs[0].TargetID != oct {
		t.Errorf("manual_payment.replaced audit: %+v %v", recs, err)
	}

	// Undo restores the record and the payment state.
	rec = f.do("POST", base+"/unconfirm", `{"version":2}`, alice)
	if rec.Code != 200 {
		t.Fatalf("unconfirm: %d %s", rec.Code, rec.Body.String())
	}
	var u replacedMatchJSON
	decode(rec.Body.Bytes(), &u)
	if a, _, res, _ := u.last(); u.State != "suggested" || a != "unconfirmed" || strings.Join(res, ",") != p.ID {
		t.Fatalf("unconfirmed: %s", rec.Body.String())
	}
	if pay := f.getOccurrence(t, oct).Payment; pay.Status != before.Status || pay.Outstanding.Amount != before.Outstanding.Amount || pay.EvidenceCount != before.EvidenceCount {
		t.Errorf("payment state not restored: before %+v after %+v", before, pay)
	}
	if l := listed(); l.ReplacedByMatchID != nil {
		t.Errorf("still replaced after the undo: %+v", l)
	}
	recs, err = store.NewAuditLog(f.db).List(ctx, audit.Filter{Actions: []audit.Action{audit.ManualPaymentRestored}, Limit: 10})
	if err != nil || len(recs) != 1 || recs[0].Details["by"] != "unconfirmed" || recs[0].Details["via"] != "session" {
		t.Errorf("manual_payment.restored audit: %+v %v", recs, err)
	}
}

// TestMCPConfirmReplacing (D45, D42 3a): over MCP a token acts as its owner:
// it confirms a suggestion as the recorded payment and takes it back, and
// every step records that it came through MCP.
func TestMCPConfirmReplacing(t *testing.T) {
	f := newMCPFixture(t, mcpOpts{})
	importFakeBankInto(t, f.db)
	secret, _ := f.token(t, f.admin, "commitments:write")
	c := f.connect(t, secret)
	ro, _ := f.token(t, f.ro, "commitments:read")
	rc := f.connect(t, ro)

	args := map[string]any{}
	if err := json.Unmarshal([]byte(`{"name":"Streaming","kind":"subscription","amount":{"amount":"13.49","currency":"EUR"},"schedule":{"version":1,"frequency":"monthly","interval":1,"day":5},"start_date":"2026-01-01"}`), &args); err != nil {
		t.Fatal(err)
	}
	if r := call(t, c, "create_commitment", args); r.IsError {
		t.Fatal(resultText(r))
	}
	if r := call(t, c, "run_payment_matching", nil); r.IsError {
		t.Fatal(resultText(r))
	}
	queue := decodeJSONText[struct {
		Items []struct {
			ID           string `json:"id"`
			OccurrenceID string `json:"occurrence_id"`
			Version      int64  `json:"version"`
		}
	}](t, call(t, c, "list_payment_matches", map[string]any{"state": []string{"suggested"}}))
	if len(queue.Items) != 1 {
		t.Fatalf("queue: %+v", queue)
	}
	s := queue.Items[0]
	p := decodeJSONText[manualJSON](t, call(t, c, "record_manual_payment", map[string]any{"id": s.OccurrenceID, "amount": map[string]any{"amount": "13.49", "currency": "EUR"}, "paid_on": "2026-10-05"}))
	type occ struct {
		Payment struct {
			Status        string `json:"status"`
			EvidenceCount int    `json:"evidence_count"`
		} `json:"payment"`
	}
	before := decodeJSONText[occ](t, call(t, c, "get_occurrence", map[string]any{"id": s.OccurrenceID}))

	replaceArgs := map[string]any{"id": s.ID, "version": s.Version, "manual_payment_ids": []string{p.ID}}
	if r := call(t, rc, "confirm_payment_match_replacing", replaceArgs); !r.IsError {
		t.Errorf("a read-only token replaced: %s", resultText(r))
	}
	if r := call(t, c, "confirm_payment_match_replacing", map[string]any{"id": s.ID, "version": s.Version + 1, "manual_payment_ids": []string{p.ID}}); !r.IsError || !strings.Contains(resultText(r), "version_conflict") {
		t.Errorf("stale version: %q", resultText(r))
	}
	m := decodeJSONText[replacedMatchJSON](t, call(t, c, "confirm_payment_match_replacing", replaceArgs))
	if a, rep, _, v := m.last(); m.State != "confirmed" || a != "confirmed" || strings.Join(rep, ",") != p.ID || v != "mcp" {
		t.Fatalf("confirmed replacing over MCP: %+v", m)
	}
	if o := decodeJSONText[occ](t, call(t, c, "get_occurrence", map[string]any{"id": s.OccurrenceID})); o.Payment.EvidenceCount != 1 {
		t.Errorf("evidence after the replacement: %+v", o)
	}
	u := decodeJSONText[replacedMatchJSON](t, call(t, c, "unconfirm_payment_match", map[string]any{"id": s.ID, "version": m.Version}))
	if a, _, res, v := u.last(); u.State != "suggested" || a != "unconfirmed" || strings.Join(res, ",") != p.ID || v != "mcp" {
		t.Fatalf("unconfirmed over MCP: %+v", u)
	}
	if o := decodeJSONText[occ](t, call(t, c, "get_occurrence", map[string]any{"id": s.OccurrenceID})); o.Payment != before.Payment {
		t.Errorf("payment state not restored: before %+v after %+v", before, o)
	}
	for _, a := range []audit.Action{audit.ManualPaymentReplaced, audit.ManualPaymentRestored} {
		es := f.auditEntries(t, a)
		if len(es) != 1 || es[0].Details["via"] != "mcp" || es[0].Details["payment_id"] != p.ID {
			t.Errorf("%s: %+v", a, es)
		}
	}
	tools := 0
	for _, e := range f.auditEntries(t, audit.MCPToolCalled) {
		if e.Details["tool"] == "confirm_payment_match_replacing" {
			tools++
		}
	}
	if tools == 0 {
		t.Error("no mcp.tool_called entry for confirm_payment_match_replacing")
	}
}
