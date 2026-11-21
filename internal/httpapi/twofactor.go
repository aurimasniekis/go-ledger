package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-ledger/internal/domain/auth"
)

// TwoFactorPath is the base of the second-factor routes (ADR-0030).
const TwoFactorPath = AuthPath + "/two-factor"

// secondFactorJSON is sign-in's 202 answer for a user with TOTP enabled.
type secondFactorJSON struct {
	SecondFactorRequired bool      `json:"second_factor_required"`
	Challenge            string    `json:"challenge"`
	ExpiresAt            time.Time `json:"expires_at"`
	Methods              []string  `json:"methods"`
}

type twoFactorStatusJSON struct {
	Enabled                bool       `json:"enabled"`
	Method                 *string    `json:"method"`
	EnabledAt              *time.Time `json:"enabled_at"`
	RecoveryCodesRemaining int        `json:"recovery_codes_remaining"`
	Required               bool       `json:"required"`
	// PendingEnrollmentUntil is when a started enrolment stops being
	// confirmable; null when none is pending.
	PendingEnrollmentUntil *time.Time `json:"pending_enrollment_until"`
}

type enrollmentJSON struct {
	Secret     string    `json:"secret"`
	OTPAuthURI string    `json:"otpauth_uri"`
	Algorithm  string    `json:"algorithm"`
	Digits     int       `json:"digits"`
	Period     int       `json:"period"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type recoveryCodesJSON struct {
	RecoveryCodes []string `json:"recovery_codes"`
	// SessionsEnded is set when enabling TOTP ended the user's other sessions.
	SessionsEnded *int `json:"sessions_ended,omitempty"`
}

// currentFactorJSON is the current second factor that replacing, disabling
// or regenerating 2FA needs while TOTP is enabled (decision D26-1a): exactly
// one of a code from the enabled authenticator or an unused recovery code.
type currentFactorJSON struct {
	CurrentCode         *string `json:"current_code"`
	CurrentRecoveryCode *string `json:"current_recovery_code"`
}

func (s *Server) currentFactor(r *http.Request, in currentFactorJSON) auth.CurrentFactor {
	cf := auth.CurrentFactor{Address: s.ClientAddress(r)}
	if in.CurrentCode != nil {
		cf.Code = *in.CurrentCode
	}
	if in.CurrentRecoveryCode != nil {
		cf.RecoveryCode = *in.CurrentRecoveryCode
	}
	return cf
}

// decodeOptionalJSON is [DecodeJSON] that accepts an empty body (no current
// factor given), for routes whose body used to be empty.
func decodeOptionalJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return tooLarge(maxErr.Limit)
		}
		return Validation("", "request body is not valid JSON for this endpoint")
	case dec.More():
		return Validation("", "request body must contain a single JSON value")
	}
	return nil
}

// twoFactorChangeError maps a 2FA change's errors: a missing current factor
// is 400 validation_failed and a wrong one 400 invalid_second_factor, both on
// field current_code (or current_recovery_code when that was given).
func twoFactorChangeError(w http.ResponseWriter, err error, in currentFactorJSON) error {
	field := "current_code"
	if in.CurrentRecoveryCode != nil && in.CurrentCode == nil {
		field = "current_recovery_code"
	}
	if errors.Is(err, auth.ErrCurrentFactorRequired) {
		return Validation("current_code", err.Error())
	}
	e := authError(w, err, http.StatusBadRequest)
	var ae *Error
	if errors.Is(err, auth.ErrInvalidCurrentFactor) && errors.As(e, &ae) {
		ae.Field = field
		ae.Message = auth.ErrInvalidCurrentFactor.Error()
	}
	return e
}

// registerTwoFactor adds the signed-in user's own second-factor routes. All
// of them act on the caller's own credentials, so they are session routes;
// changes need recent authentication. Status, enrolment and confirmation stay
// reachable while auth.require_2fa confines the session to enrolment.
func registerTwoFactor(s *Server, svc *auth.Service) {
	// Replacing, disabling or regenerating 2FA while TOTP is enabled needs a
	// current TOTP or recovery code in the body as well as recent
	// re-authentication, whatever the session method (decision D26-1a).
	s.HandleAPI("GET "+TwoFactorPath, SessionOnly().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, err := svc.TwoFactorStatus(r.Context(), principal(r).UserID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := twoFactorStatusJSON{Enabled: st.Enabled, RecoveryCodesRemaining: st.RecoveryCodesRemaining, Required: st.Required}
		if st.Enabled {
			m, t := auth.MethodTOTP, st.EnabledAt.UTC()
			out.Method, out.EnabledAt = &m, &t
		}
		if !st.PendingUntil.IsZero() {
			t := st.PendingUntil.UTC()
			out.PendingEnrollmentUntil = &t
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, out)
	}))

	// Start (or restart) a TOTP enrolment. The secret is returned here only.
	s.HandleAPI("POST "+TwoFactorPath+"/totp", SessionOnly().WithRecentAuth().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, err := svc.BeginTOTP(r.Context(), principal(r))
		if err != nil {
			WriteError(w, r, authError(w, err, http.StatusBadRequest))
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusCreated, enrollmentJSON{Secret: e.Secret, OTPAuthURI: e.URI, Algorithm: "SHA1", Digits: auth.TOTPDigits,
			Period: int(auth.TOTPPeriod / time.Second), ExpiresAt: e.ExpiresAt.UTC()})
	}))

	// Confirm the enrolment with a code from the authenticator: TOTP is then
	// enabled, new recovery codes are returned once, other sessions end.
	s.HandleAPI("POST "+TwoFactorPath+"/totp/confirm", SessionOnly().WithRecentAuth().DuringEnrollment(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Code *string `json:"code"`
			currentFactorJSON
		}
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		if in.Code == nil {
			WriteError(w, r, Validation("code", "code is required"))
			return
		}
		codes, ended, err := svc.ConfirmTOTP(r.Context(), principal(r), *in.Code, s.currentFactor(r, in.currentFactorJSON))
		if err != nil {
			e := twoFactorChangeError(w, err, in.currentFactorJSON)
			var ae *Error
			if errors.As(e, &ae) && ae.Code == CodeInvalidSecondFactor && !errors.Is(err, auth.ErrInvalidCurrentFactor) {
				ae.Field = "code"
			}
			WriteError(w, r, e)
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes, SessionsEnded: &ended})
	}))

	s.HandleAPI("POST "+TwoFactorPath+"/recovery-codes", SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in currentFactorJSON
		if err := decodeOptionalJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		codes, err := svc.RegenerateRecoveryCodes(r.Context(), principal(r), s.currentFactor(r, in))
		if err != nil {
			WriteError(w, r, twoFactorChangeError(w, err, in))
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
	}))

	s.HandleAPI("DELETE "+TwoFactorPath, SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in currentFactorJSON
		if err := decodeOptionalJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		p := principal(r)
		if err := svc.DisableTwoFactor(r.Context(), p, false, p.UserID, auth.ByUser, s.currentFactor(r, in)); err != nil {
			WriteError(w, r, twoFactorChangeError(w, err, in))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
