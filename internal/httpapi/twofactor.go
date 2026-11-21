package httpapi

import (
	"errors"
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

// registerTwoFactor adds the signed-in user's own second-factor routes. All
// of them act on the caller's own credentials, so they are session routes;
// changes need recent authentication. Status, enrolment and confirmation stay
// reachable while auth.require_2fa confines the session to enrolment.
func registerTwoFactor(s *Server, svc *auth.Service) {
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
		}
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		if in.Code == nil {
			WriteError(w, r, Validation("code", "code is required"))
			return
		}
		codes, ended, err := svc.ConfirmTOTP(r.Context(), principal(r), *in.Code)
		if err != nil {
			e := authError(w, err, http.StatusBadRequest)
			var ae *Error
			if errors.As(e, &ae) && ae.Code == CodeInvalidSecondFactor {
				ae.Field = "code"
			}
			WriteError(w, r, e)
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes, SessionsEnded: &ended})
	}))

	s.HandleAPI("POST "+TwoFactorPath+"/recovery-codes", SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		codes, err := svc.RegenerateRecoveryCodes(r.Context(), principal(r))
		if err != nil {
			WriteError(w, r, authError(w, err, http.StatusBadRequest))
			return
		}
		noStore(w)
		WriteJSON(w, http.StatusOK, recoveryCodesJSON{RecoveryCodes: codes})
	}))

	s.HandleAPI("DELETE "+TwoFactorPath, SessionOnly().WithRecentAuth(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if err := svc.DisableTwoFactor(r.Context(), p, false, p.UserID, auth.ByUser); err != nil {
			WriteError(w, r, authError(w, err, http.StatusBadRequest))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
