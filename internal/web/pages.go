package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"time"
)

// funcs are the template functions for a language.
func funcs(lang string) template.FuncMap {
	text := func(key string) string {
		if v, ok := texts[lang][key]; ok {
			return v
		}
		return texts["en"][key]
	}
	return template.FuncMap{
		"t": text,
		// date writes a time in the server's time zone, "" for none.
		"date": func(t time.Time) string {
			if t.IsZero() {
				return text("never")
			}
			if lang == "de" {
				return t.Local().Format("02.01.2006 15:04")
			}
			return t.Local().Format("2006-01-02 15:04")
		},
		// dur writes a duration in days, hours and minutes.
		"dur": func(d time.Duration) string {
			d = d.Round(time.Minute)
			days, hours, mins := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
			switch {
			case days > 0:
				return fmt.Sprintf("%d d %d h", days, hours)
			case hours > 0:
				return fmt.Sprintf("%d h %d min", hours, mins)
			}
			return fmt.Sprintf("%d min", mins)
		},
		"secs": func(d time.Duration) string {
			if d < time.Second {
				return d.Round(time.Millisecond).String()
			}
			return d.Round(100 * time.Millisecond).String()
		},
	}
}

// parsePages builds one template set per page from the layout and the
// page's own file.
func parsePages() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "home", "error", "admin", "admin_users", "admin_user", "admin_library",
		"admin_metadata", "admin_movie", "admin_settings"} {
		t := template.New("layout.html").Funcs(funcs("en"))
		pages[name] = template.Must(t.ParseFS(templateFiles, "templates/layout.html", "templates/partials.html",
			"templates/"+name+".html"))
	}
	return pages
}

// render writes a page. data is available as .Data, the session as .Session.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, sess *session, data map[string]any) {
	lang := s.Language
	if _, ok := texts[lang]; !ok {
		lang = "en"
	}
	t, err := s.pages[page].Clone()
	if err != nil {
		s.fail(w, err)
		return
	}
	t.Funcs(funcs(lang))
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"Lang": lang, "Server": s.ServerName, "Session": sess, "Data": data}); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}
