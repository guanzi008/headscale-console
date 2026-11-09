package idp

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed templates/*.html
var templateFiles embed.FS

func parseTemplates() (*template.Template, error) {
	tpl, err := template.New("idp").Funcs(template.FuncMap{
		"providerName": func(name string) string {
			if name == "" {
				return ""
			}
			return strings.ToUpper(name[:1]) + name[1:]
		},
	}).ParseFS(templateFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse identity templates: %w", err)
	}
	return tpl, nil
}
