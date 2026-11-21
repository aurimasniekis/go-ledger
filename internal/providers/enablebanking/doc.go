// Package enablebanking is the Enable Banking adapter: it implements
// internal/domain/banking.Provider against the Enable Banking API for one
// configured application (ADR-0023).
//
// Terminology follows decision D10. The operator defined the two modes;
// Enable Banking's documentation uses neither term:
//
//   - app mode ([banking.ModeApp]): an unrestricted production application
//     (signed contract and KYB with Enable Banking). Every account the user
//     authorises is accessible.
//   - developer mode ([banking.ModeDeveloper]): a restricted production
//     application, activated by linking the operator's own accounts in the
//     Enable Banking Control Panel. Only those linked accounts are returned,
//     and an unlinked account shows up as an empty account list, not an
//     error.
//
// Neither mode is the sandbox. Enable Banking binds the environment (SANDBOX
// or PRODUCTION) to the application, so it is set separately
// ([Config.Environment]) and checked by [Provider.Verify].
//
// Everything Enable Banking-specific stays in this package: RS256 JWT
// application authentication, the consent and session flow, paging with
// continuation keys, strategy=longest for the first import, the mapping of
// ISO 20022 codes to the normalized model, and the classification of Enable
// Banking error codes and HTTP statuses into the banking error taxonomy.
//
// The adapter never logs. Its errors carry the operation, the error kind,
// the reason and Enable Banking's error code only: never the application
// key, the JWT, session ids, account uids, identifiers, URLs or response
// bodies.
package enablebanking
