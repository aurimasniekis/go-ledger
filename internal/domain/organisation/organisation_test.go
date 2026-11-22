package organisation

import (
	"strings"
	"testing"

	"github.com/aurimasniekis/go-ledger/internal/domain/resource"
)

func TestCanReplaceKeepsTheUsersWord(t *testing.T) {
	cases := []struct {
		existing, incoming Origin
		want               bool
	}{
		{OriginUser, OriginUser, true},
		{OriginUser, OriginRule, false},
		{OriginUser, OriginModel, false},
		{OriginRule, OriginUser, true},
		{OriginRule, OriginRule, true},
		{OriginRule, OriginModel, false},
		{OriginModel, OriginUser, true},
		{OriginModel, OriginRule, true},
		{OriginModel, OriginModel, true},
	}
	for _, c := range cases {
		if got := CanReplace(c.existing, c.incoming); got != c.want {
			t.Errorf("CanReplace(%s, %s) = %v, want %v", c.existing, c.incoming, got, c.want)
		}
	}
}

func TestFieldsValidate(t *testing.T) {
	parent := resource.NewID()
	cases := []struct {
		name string
		kind Kind
		f    Fields
		want string // field of the first error, "" for none
	}{
		{"ok", KindCategory, Fields{Name: "Housing", ParentID: &parent}, ""},
		{"empty", KindLabel, Fields{}, "name"},
		{"long", KindLabel, Fields{Name: strings.Repeat("x", MaxName+1)}, "name"},
		{"control", KindLabel, Fields{Name: "a\x00b"}, "name"},
		{"bidi override", KindLabel, Fields{Name: "a\xe2\x80\xaeb"}, "name"},
		{"label with parent", KindLabel, Fields{Name: "x", ParentID: &parent}, "parent_id"},
		{"bad parent", KindCategory, Fields{Name: "x", ParentID: new(string)}, "parent_id"},
	}
	for _, c := range cases {
		errs := c.f.Normalized().Validate(c.kind)
		switch {
		case c.want == "" && len(errs) > 0, c.want != "" && (len(errs) == 0 || errs[0].Field != c.want):
			t.Errorf("%s: errors %v, want field %q", c.name, errs, c.want)
		}
	}
	if got := (Fields{Name: "  Eating   out "}).Normalized().Name; got != "Eating out" {
		t.Errorf("normalized name = %q", got)
	}
	if NameKey("Eating  OUT") != NameKey("eating out") {
		t.Error("names differing in case or spacing have different keys")
	}
}

func TestObjectRefAndMerchantKey(t *testing.T) {
	if errs := (ObjectRef{ObjectTransaction, resource.NewID()}).Validate(); len(errs) != 0 {
		t.Errorf("valid transaction rejected: %v", errs)
	}
	if errs := (ObjectRef{ObjectTransaction, "nope"}).Validate(); len(errs) == 0 {
		t.Error("a malformed transaction id was accepted")
	}
	if errs := (ObjectRef{"widget", resource.NewID()}).Validate(); len(errs) == 0 {
		t.Error("an unknown object type was accepted")
	}
	k, ok := MerchantKey("  Acme   Shop ")
	if !ok || k != "acme shop" {
		t.Errorf("MerchantKey = %q, %v", k, ok)
	}
	if errs := (ObjectRef{ObjectMerchant, "Acme Shop"}).Validate(); len(errs) == 0 {
		t.Error("a merchant that is not a key was accepted")
	}
	if errs := (ObjectRef{ObjectMerchant, k}).Validate(); len(errs) != 0 {
		t.Errorf("a merchant key was rejected: %v", errs)
	}
	if _, ok := MerchantKey(" "); ok {
		t.Error("an empty merchant key was accepted")
	}
	if ObjectMerchant.BucketMember() || ObjectTransaction.BucketMember() || !ObjectCommitment.BucketMember() || !ObjectAccount.BucketMember() {
		t.Error("buckets hold commitments and accounts only")
	}
}

func TestAutomaticValidate(t *testing.T) {
	good := Automatic{Origin: OriginRule, Source: "rule:rent"}
	if errs := good.Validate(); len(errs) != 0 {
		t.Errorf("valid proposal rejected: %v", errs)
	}
	for _, a := range []Automatic{{Origin: OriginUser, Source: "x"}, {Origin: OriginModel}, {Origin: OriginRule, Source: "x", LabelIDs: []string{"bad"}}} {
		if len(a.Validate()) == 0 {
			t.Errorf("%+v accepted", a)
		}
	}
}
