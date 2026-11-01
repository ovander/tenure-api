// Package templates embeds the document templates (RENT-BE-001 §6): Go
// html/template files under <class>/<version>/, French only. A document
// records the version it was rendered with; a template change never re-renders
// an issued document, it adds a new version directory.
package templates

import "embed"

// FS holds every template.
//
//go:embed */*/*.html
var FS embed.FS
