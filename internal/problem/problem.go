// Package problem writes RFC 9457 problem details (RENT-BE-001 §3, §8). Every
// refusal has a stable type, a business rule the front-end can show; its title
// is French by default, English when the caller's language is English
// (RENT-SPEC non-functional requirements, localisation). No stack trace or
// internal message ever reaches the body of a 5xx.
package problem

import (
	"encoding/json"
	"net/http"
)

// Problem types used across the API. Module-specific types (for example
// lease.TypeConsistentShares) are registered with Register.
const (
	Unauthenticated      = "auth/unauthenticated"
	StepUpRequired       = "auth/step-up-required"
	MFARequired          = "auth/mfa-required"
	Forbidden            = "auth/forbidden"
	ScopeForbidden       = "auth/scope-forbidden"
	NoScope              = "auth/no-scope"
	TooManyFailedAuth    = "auth/too-many-failures"
	BadRequest           = "http/bad-request"
	NotFound             = "http/not-found"
	MethodNotAllowed     = "http/method-not-allowed"
	UnsupportedMedia     = "http/unsupported-media-type"
	PayloadTooLarge      = "http/payload-too-large"
	PreconditionRequired = "http/precondition-required"
	PreconditionFailed   = "http/precondition-failed"
	TooManyRequests      = "http/too-many-requests"
	Unavailable          = "http/unavailable"
	Internal             = "http/internal"
	Validation           = "validation/invalid"
)

// Message is the status and the localised titles of a problem type.
type Message struct {
	Status int
	FR, EN string
}

var catalogue = map[string]Message{
	Unauthenticated:      {401, "Authentification requise.", "Authentication required."},
	StepUpRequired:       {401, "Veuillez vous authentifier à nouveau avec un second facteur.", "Please authenticate again with a second factor."},
	MFARequired:          {403, "Cette action exige une connexion avec double authentification, que votre compte Socrate n’a pas utilisée : activez-la dans votre compte Socrate si ce n’est pas déjà fait, puis recommencez.", "This action needs a sign-in with a second factor, which your Socrate account did not use: turn it on in your Socrate account if it is not, then try again."},
	Forbidden:            {403, "Action non autorisée pour votre rôle.", "Action not allowed for your role."},
	ScopeForbidden:       {403, "Vous n’avez pas accès à ce portefeuille.", "You have no access to this portfolio."},
	NoScope:              {403, "Aucun portefeuille n’est associé à votre compte.", "No portfolio is linked to your account."},
	TooManyFailedAuth:    {429, "Trop de tentatives d’authentification échouées. Réessayez plus tard.", "Too many failed authentication attempts. Try again later."},
	BadRequest:           {400, "Requête invalide.", "Invalid request."},
	NotFound:             {404, "Ressource introuvable.", "Resource not found."},
	MethodNotAllowed:     {405, "Méthode non autorisée.", "Method not allowed."},
	UnsupportedMedia:     {415, "Le corps de la requête doit être au format JSON.", "The request body must be JSON."},
	PayloadTooLarge:      {413, "Requête trop volumineuse.", "Request too large."},
	PreconditionRequired: {428, "L’en-tête If-Match est obligatoire pour modifier une ressource.", "The If-Match header is required to change a resource."},
	PreconditionFailed:   {412, "La ressource a été modifiée entre-temps ; rechargez-la.", "The resource changed in the meantime; reload it."},
	TooManyRequests:      {429, "Trop de requêtes. Réessayez dans un instant.", "Too many requests. Try again shortly."},
	Unavailable:          {503, "Service momentanément indisponible.", "Service temporarily unavailable."},
	Internal:             {500, "Erreur interne.", "Internal error."},
	Validation:           {422, "Certaines données sont invalides.", "Some data is invalid."},
}

// M returns the message of a problem type.
func M(status int, fr, en string) Message { return Message{Status: status, FR: fr, EN: en} }

// Register adds or replaces problem types; modules call it from init.
func Register(m map[string]Message) {
	for k, v := range m {
		catalogue[k] = v
	}
}

// Lookup returns the message of type t, and false for an unknown type.
func Lookup(t string) (Message, bool) {
	m, ok := catalogue[t]
	return m, ok
}

// Types returns every registered type (for the catalogue test).
func Types() map[string]Message {
	out := make(map[string]Message, len(catalogue))
	for k, v := range catalogue {
		out[k] = v
	}
	return out
}

// FieldError is one invalid field, in the errors[] extension the front-end
// shows inline (RENT-FE-001 §6).
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Body is the JSON body of a problem.
type Body struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Errors   []FieldError `json:"errors,omitempty"`
}

// Language returns "en" when lang is English, "fr" otherwise.
func Language(lang string) string {
	if lang == "en" {
		return "en"
	}
	return "fr"
}

// Write writes the problem of type t. An unknown type is written as an
// internal error so that no unregistered message leaks.
func Write(w http.ResponseWriter, r *http.Request, t, lang, detail string, errs []FieldError) {
	m, ok := catalogue[t]
	if !ok {
		t, m, detail, errs = Internal, catalogue[Internal], "", nil
	}
	title := m.FR
	if Language(lang) == "en" {
		title = m.EN
	}
	if m.Status >= 500 {
		detail, errs = "", nil
	}
	b := Body{Type: t, Title: title, Status: m.Status, Detail: detail, Errors: errs}
	if r != nil {
		if id := r.Header.Get("X-Request-ID"); id != "" {
			b.Instance = "urn:request:" + id
		}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Content-Language", Language(lang))
	w.WriteHeader(m.Status)
	_ = json.NewEncoder(w).Encode(b)
}
