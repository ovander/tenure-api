package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/templates"
)

// TemplateVersionView is one version of a document template (RENT-BE-001
// §6, Q-106): its SHA-256 (of its document.html), whether new documents use
// it, and how many of the scope's documents were rendered with it.
type TemplateVersionView struct {
	Version string `json:"version" example:"v2"`
	// Current is the version new documents are rendered with: the latest,
	// since a template change only ever adds a version.
	Current   bool       `json:"current"`
	SHA256    string     `json:"sha256"`
	Documents int        `json:"documents"`
	FirstAt   *time.Time `json:"first_issued_at,omitempty"`
	LastAt    *time.Time `json:"last_issued_at,omitempty"`
}

// TemplateView is a document template and its versions, oldest first. Name is
// the template's directory (contract, deposit_receipt, rent_call…); Class is
// the class of the documents it renders.
type TemplateView struct {
	Name     string                `json:"name" example:"contract"`
	Class    Class                 `json:"class" example:"contract"`
	Versions []TemplateVersionView `json:"versions"`
}

// templateClass is the class a template renders, when it differs from its
// name.
var templateClass = map[string]Class{"deposit_receipt": ClassDeposit}

// TemplateUsage is how many of the scope's documents a template version
// rendered, and when the first and the last were issued.
type TemplateUsage struct {
	TemplateVersion string    `gorm:"column:template_version"`
	Documents       int       `gorm:"column:documents"`
	FirstAt         time.Time `gorm:"column:first_at"`
	LastAt          time.Time `gorm:"column:last_at"`
}

// Catalogue lists the templates of fsys (laid out as <name>/<version>/
// document.html), each with its versions in order, the latest current. The
// shared directory (style and footer of every document) is not a template.
func Catalogue(fsys fs.FS) ([]TemplateView, error) {
	names, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []TemplateView
	for _, n := range names {
		if !n.IsDir() || n.Name() == "shared" {
			continue
		}
		vs, err := fs.ReadDir(fsys, n.Name())
		if err != nil {
			return nil, err
		}
		t := TemplateView{Name: n.Name(), Class: Class(n.Name())}
		if c, ok := templateClass[n.Name()]; ok {
			t.Class = c
		}
		for _, v := range vs {
			if !v.IsDir() {
				continue
			}
			if _, err := versionNumber(v.Name()); err != nil {
				return nil, fmt.Errorf("document: template %s: %w", n.Name(), err)
			}
			b, err := fs.ReadFile(fsys, n.Name()+"/"+v.Name()+"/document.html")
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(b)
			t.Versions = append(t.Versions, TemplateVersionView{Version: v.Name(), SHA256: hex.EncodeToString(sum[:])})
		}
		if len(t.Versions) == 0 {
			continue
		}
		slices.SortFunc(t.Versions, func(a, b TemplateVersionView) int {
			x, _ := versionNumber(a.Version)
			y, _ := versionNumber(b.Version)
			return x - y
		})
		t.Versions[len(t.Versions)-1].Current = true
		out = append(out, t)
	}
	return out, nil
}

// versionNumber reads a version directory name, v1, v2….
func versionNumber(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(v, "v"))
	if err != nil || !strings.HasPrefix(v, "v") || n < 1 {
		return 0, fmt.Errorf("version %q is not v<n>", v)
	}
	return n, nil
}

// Templates lists the embedded document templates with how many of the
// scope's documents each version rendered (S14, Q-106). An accountant sees
// the templates of financial classes only. Read only: templates are part of
// the release, never edited at run time.
func (s Service) Templates(ctx context.Context, p *auth.Principal, requestID string) ([]TemplateView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	all, err := Catalogue(templates.FS)
	if err != nil {
		return nil, err
	}
	var usage []TemplateUsage
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		usage, err = s.Repo.TemplateUsage(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	used := map[string]TemplateUsage{}
	for _, u := range usage {
		used[u.TemplateVersion] = u
	}
	out := []TemplateView{}
	for _, t := range all {
		// A cancellation only ever cancels a billing document (Q-49).
		if p.Active.Role == auth.RoleAccountant && !Financial(t.Class) && t.Name != string(ClassCancellation) {
			continue
		}
		for i, v := range t.Versions {
			if u, ok := used[t.Name+"/"+v.Version]; ok {
				first, last := u.FirstAt, u.LastAt
				t.Versions[i].Documents, t.Versions[i].FirstAt, t.Versions[i].LastAt = u.Documents, &first, &last
			}
		}
		out = append(out, t)
	}
	return out, nil
}
