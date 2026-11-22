package decisions

import (
	"slices"
	"strings"
)

// Field names one piece of data a request may carry to a provider.
type Field string

// Fields Ledger knows how to send. Decision D13 permits description, amount,
// currency, booking date and merchant name; candidate names are the choices a
// decide task offers (category, label or commitment names the person defined),
// without which a closed-set decision cannot be asked.
const (
	FieldDescription    Field = "description"
	FieldAmount         Field = "amount"
	FieldCurrency       Field = "currency"
	FieldBookingDate    Field = "booking_date"
	FieldMerchantName   Field = "merchant_name"
	FieldCandidateNames Field = "candidate_names"
)

// Fields decision D13 excludes. They are named so a caller that holds them
// can be refused loudly; they are never sent, whatever the configuration.
const (
	FieldIBAN             Field = "iban"
	FieldAccountNumber    Field = "account_number"
	FieldCounterpartyName Field = "counterparty_name"
	FieldBalance          Field = "balance"
)

// Permitted returns every field configuration may allow (decision D13 plus
// candidate names).
func Permitted() []Field {
	return []Field{FieldDescription, FieldAmount, FieldCurrency, FieldBookingDate, FieldMerchantName, FieldCandidateNames}
}

// Excluded returns the fields that are never sent (decision D13).
func Excluded() []Field {
	return []Field{FieldIBAN, FieldAccountNumber, FieldCounterpartyName, FieldBalance}
}

// IsPermitted reports whether f may be allowed at all.
func IsPermitted(f Field) bool { return slices.Contains(Permitted(), f) }

// ProviderTypeSafe is the TypeSafe System One provider name. Its default
// allowlist is narrower (control default, from the R2 research: the model is
// weak at numbers and dates, and inputs may be kept for telemetry).
const ProviderTypeSafe = "typesafe"

// DefaultFields returns the fields a provider may receive for a task when the
// task's configuration names none. TypeSafe gets only the description, the
// merchant name and the candidate names: amount, currency and date checks stay
// in Ledger code (D9a). Every other provider gets the D13 list; a generate
// task gets no candidate names because it offers none.
func DefaultFields(provider string, task TaskName) []Field {
	t, _ := LookupTask(task)
	var out []Field
	if provider == ProviderTypeSafe {
		out = []Field{FieldDescription, FieldMerchantName, FieldCandidateNames}
	} else {
		out = Permitted()
	}
	if t.Capability == Generate {
		out = slices.DeleteFunc(out, func(f Field) bool { return f == FieldCandidateNames })
	}
	return out
}

// ParseFields checks configured field names: every one must be permitted, and
// an excluded field is refused by name. It returns the fields in the order of
// [Permitted], without duplicates.
func ParseFields(names []string) ([]Field, []string) {
	var problems []string
	set := map[Field]bool{}
	for _, n := range names {
		f := Field(strings.TrimSpace(n))
		switch {
		case slices.Contains(Excluded(), f):
			problems = append(problems, string(f)+" is never sent to a decision provider (decision D13)")
		case !IsPermitted(f):
			problems = append(problems, "unknown field "+string(f)+"; permitted: "+joinFields(Permitted()))
		default:
			set[f] = true
		}
	}
	var out []Field
	for _, f := range Permitted() {
		if set[f] {
			out = append(out, f)
		}
	}
	return out, problems
}

func joinFields(fs []Field) string {
	s := make([]string, len(fs))
	for i, f := range fs {
		s[i] = string(f)
	}
	return strings.Join(s, ", ")
}
