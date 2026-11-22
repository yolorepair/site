package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
)

// Where the browser goes next. Both are pages on the site, so the redirect
// stays on the origin the form was posted from.
const (
	thanksPath  = "/request/thanks/"
	problemPath = "/request/problem/"
)

// maxFormBytes caps the form body. Percent-encoding makes a form bigger than
// its text, so this is looser than intake's cap; the JSON built from the form
// is checked against maxJSONBytes before it is sent.
const maxFormBytes = 64 << 10

type server struct {
	intake *intakeClient
	log    *slog.Logger
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", s.submit)
	mux.HandleFunc("/healthz", s.healthz)
	return mux
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// submit takes the form, forwards it to intake and answers with a redirect.
// Nothing from the form is stored or logged: the log line carries an outcome
// and, when intake answered, its status.
func (s *server) submit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	outcome, status := s.relay(r)
	target := problemPath
	if outcome == "accepted" {
		target = thanksPath
	}

	level := slog.LevelInfo
	switch outcome {
	case "intake_unauthorized", "intake_rejected", "intake_error", "intake_unreachable":
		// The form was fine and the request was lost anyway. Someone
		// should look.
		level = slog.LevelError
	}
	s.log.Log(r.Context(), level, "submit", "outcome", outcome, "intake_status", status)

	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *server) relay(r *http.Request) (outcome string, intakeStatus int) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return "not_a_form", 0
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxFormBytes+1))
	if err != nil {
		return "unreadable", 0
	}
	if len(raw) > maxFormBytes {
		return "too_large", 0
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return "unreadable", 0
	}
	req, ok := parseForm(form)
	if !ok {
		return "invalid", 0
	}
	body, err := json.Marshal(req)
	if err != nil || len(body) > maxJSONBytes {
		return "too_large", 0
	}
	key, err := newKey()
	if err != nil {
		return "internal_error", 0
	}

	status, err := s.intake.send(r.Context(), body, key)
	switch {
	case err != nil:
		return "intake_unreachable", 0
	case status == http.StatusAccepted:
		return "accepted", status
	case status == http.StatusTooManyRequests:
		return "intake_full", status
	case status == http.StatusUnauthorized:
		return "intake_unauthorized", status
	case status >= 500:
		return "intake_error", status
	default:
		// intake turned down something this relay thought was valid: the
		// two have drifted from the contract.
		return "intake_rejected", status
	}
}
