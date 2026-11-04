package web

import (
	"bytes"
	"html/template"
	"net/http"
)

// texts are the words of the pages, by language.
var texts = map[string]map[string]string{
	"en": {
		"title":        "homeflix",
		"username":     "User name",
		"password":     "Password",
		"signIn":       "Sign in",
		"signOut":      "Sign out",
		"signedInAs":   "Signed in as",
		"admin":        "Administration",
		"home":         "Home",
		"welcome":      "Welcome, %s.",
		"comingSoon":   "The library comes with the next stage of the web client.",
		"adminSoon":    "Users, library and metadata come with the next stage of the admin UI.",
		"yourRequest":  "Your request",
		"address":      "Address",
		"via":          "Through a proxy",
		"origin":       "Address used",
		"yes":          "yes",
		"no":           "no",
		"loginMissing": "Enter your user name.",
		"loginFailed":  "Wrong user name or password.",
		"loginLocked":  "Too many failed logins. Try again in 15 minutes.",
		"adminNetwork": "The administration can only be reached from the home network and the tailnet.",
		"adminOnly":    "The administration is for administrators only.",
		"notAllowed":   "Not allowed",
	},
	"de": {
		"title":        "homeflix",
		"username":     "Benutzername",
		"password":     "Passwort",
		"signIn":       "Anmelden",
		"signOut":      "Abmelden",
		"signedInAs":   "Angemeldet als",
		"admin":        "Verwaltung",
		"home":         "Start",
		"welcome":      "Willkommen, %s.",
		"comingSoon":   "Die Bibliothek kommt mit dem nächsten Ausbau des Web-Clients.",
		"adminSoon":    "Benutzer, Bibliothek und Metadaten kommen mit dem nächsten Ausbau der Verwaltung.",
		"yourRequest":  "Deine Anfrage",
		"address":      "Adresse",
		"via":          "Über einen Proxy",
		"origin":       "Aufgerufene Adresse",
		"yes":          "ja",
		"no":           "nein",
		"loginMissing": "Bitte den Benutzernamen eingeben.",
		"loginFailed":  "Benutzername oder Passwort falsch.",
		"loginLocked":  "Zu viele fehlgeschlagene Anmeldungen. Bitte in 15 Minuten erneut versuchen.",
		"adminNetwork": "Die Verwaltung ist nur aus dem Heimnetz und dem Tailnet erreichbar.",
		"adminOnly":    "Die Verwaltung ist nur für Administratoren.",
		"notAllowed":   "Nicht erlaubt",
	},
}

// parsePages builds one template set per page from the layout and the
// page's own file.
func parsePages() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "home", "admin", "error"} {
		t := template.New("layout.html").Funcs(template.FuncMap{
			// Filled in per request by render.
			"t": func(string) string { return "" },
		})
		pages[name] = template.Must(t.ParseFS(templateFiles, "templates/layout.html", "templates/"+name+".html"))
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
	t.Funcs(template.FuncMap{"t": func(key string) string {
		if v, ok := texts[lang][key]; ok {
			return v
		}
		return texts["en"][key]
	}})
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
