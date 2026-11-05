// Package esigntest is a fake of the Yousign API v3 routes Tenure calls
// (internal/esign), for tests only: requests, documents, signers, activation,
// status, download and cancel, with the signer's signing simulated by Sign.
package esigntest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Signer is a signer as the fake keeps it.
type Signer struct {
	ID, FirstName, LastName, Email, Status string
}

// Request is a signature request as the fake keeps it.
type Request struct {
	ID, Name, ExternalID, Status, DeliveryMode string
	Document                                   []byte
	ParseAnchors                               bool
	Signers                                    []*Signer
}

// Server is the fake. Key is the API key it accepts.
type Server struct {
	*httptest.Server
	Key string
	mu  sync.Mutex
	seq int
	req map[string]*Request
	// Fail makes the next call to a path containing it answer 500.
	Fail string
}

// New starts a fake accepting key.
func New(key string) *Server {
	s := &Server{Key: key, req: map[string]*Request{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Requests returns the requests made so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Request{}
	for _, r := range s.req {
		out = append(out, *r)
	}
	return out
}

// Sign marks a signer as signed, and the request done once all are.
func (s *Server) Sign(signerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.req {
		all := true
		for _, sg := range r.Signers {
			if sg.ID == signerID {
				sg.Status = "signed"
			}
			all = all && sg.Status == "signed"
		}
		if all && r.Status == "ongoing" {
			r.Status = "done"
		}
	}
}

// Decline marks a request declined.
func (s *Server) Decline(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.req[requestID]; r != nil {
		r.Status = "declined"
	}
}

func (s *Server) id(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", prefix, s.seq)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+s.Key {
		http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if s.Fail != "" && strings.Contains(r.URL.Path, s.Fail) {
		s.Fail = ""
		http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v3"), "/"), "/")
	if len(parts) == 0 || parts[0] != "signature_requests" {
		http.NotFound(w, r)
		return
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	if len(parts) == 1 && r.Method == http.MethodPost {
		var b struct {
			Name, ExternalID, DeliveryMode string
		}
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		b.Name, b.ExternalID, b.DeliveryMode = raw["name"], raw["external_id"], raw["delivery_mode"]
		req := &Request{ID: s.id("req"), Name: b.Name, ExternalID: b.ExternalID, DeliveryMode: b.DeliveryMode, Status: "draft"}
		s.req[req.ID] = req
		reply(map[string]string{"id": req.ID, "status": req.Status})
		return
	}
	req := s.req[parts[1]]
	if req == nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		signers := []map[string]string{}
		for _, sg := range req.Signers {
			signers = append(signers, map[string]string{"id": sg.ID, "status": sg.Status})
		}
		reply(map[string]any{"id": req.ID, "status": req.Status, "signers": signers})
	case len(parts) == 3 && parts[2] == "documents" && r.Method == http.MethodPost:
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			switch p.FormName() {
			case "file":
				req.Document = data
			case "parse_anchors":
				req.ParseAnchors = string(data) == "true"
			}
		}
		reply(map[string]string{"id": s.id("doc")})
	case len(parts) == 3 && parts[2] == "signers" && r.Method == http.MethodPost:
		var b struct {
			Info struct {
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
				Email     string `json:"email"`
			} `json:"info"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		sg := &Signer{ID: s.id("sig"), FirstName: b.Info.FirstName, LastName: b.Info.LastName, Email: b.Info.Email, Status: "initiated"}
		req.Signers = append(req.Signers, sg)
		reply(map[string]string{"id": sg.ID})
	case len(parts) == 3 && parts[2] == "activate" && r.Method == http.MethodPost:
		req.Status = "ongoing"
		signers := []map[string]string{}
		for _, sg := range req.Signers {
			sg.Status = "notified"
			signers = append(signers, map[string]string{"id": sg.ID, "status": sg.Status, "signature_link": "https://sign.example.test/" + sg.ID})
		}
		reply(map[string]any{"id": req.ID, "status": req.Status, "signers": signers})
	case len(parts) == 4 && parts[2] == "documents" && parts[3] == "download" && r.Method == http.MethodGet:
		if req.Status != "done" {
			http.Error(w, `{"detail":"not done"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(bytes.Replace(req.Document, []byte("%%EOF"), []byte("% signed by the fake provider\n%%EOF"), 1))
	case len(parts) == 3 && parts[2] == "cancel" && r.Method == http.MethodPost:
		req.Status = "canceled"
		reply(map[string]string{"id": req.ID, "status": req.Status})
	default:
		http.NotFound(w, r)
	}
}
