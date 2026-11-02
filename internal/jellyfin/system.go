package jellyfin

import "net/http"

// PublicSystemInfo is the unauthenticated server description clients probe
// before login.
type PublicSystemInfo struct {
	LocalAddress           string `json:"LocalAddress"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem"`
	ID                     string `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// BrandingOptions is returned by /Branding/Configuration.
type BrandingOptions struct {
	LoginDisclaimer     string `json:"LoginDisclaimer"`
	CustomCss           string `json:"CustomCss"`
	SplashscreenEnabled bool   `json:"SplashscreenEnabled"`
}

func (s *Server) registerSystem(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/System/Info/Public", s.publicSystemInfo)
	rt.HandleFunc(http.MethodGet, "/System/Ping", s.ping)
	rt.HandleFunc(http.MethodPost, "/System/Ping", s.ping)
	rt.HandleFunc(http.MethodGet, "/Branding/Configuration", s.brandingConfiguration)
}

func (s *Server) publicSystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, PublicSystemInfo{
		LocalAddress:           localAddress(r),
		ServerName:             s.ServerName,
		Version:                CompatVersion,
		ProductName:            ProductName,
		ID:                     s.ServerID,
		StartupWizardCompleted: true,
	})
}

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ProductName)
}

func (s *Server) brandingConfiguration(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, BrandingOptions{})
}

// localAddress is the base URL the client reached this server on.
func localAddress(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
