package jellyfin

import (
	"net/http"
	"runtime"
)

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

// SystemInfo is the server description for signed-in clients. Filesystem
// paths are left empty on purpose.
type SystemInfo struct {
	PublicSystemInfo
	OperatingSystemDisplayName           string `json:"OperatingSystemDisplayName"`
	PackageName                          string `json:"PackageName"`
	HasPendingRestart                    bool   `json:"HasPendingRestart"`
	IsShuttingDown                       bool   `json:"IsShuttingDown"`
	SupportsLibraryMonitor               bool   `json:"SupportsLibraryMonitor"`
	WebSocketPortNumber                  int    `json:"WebSocketPortNumber"`
	CompletedInstallations               []any  `json:"CompletedInstallations"`
	CanSelfRestart                       bool   `json:"CanSelfRestart"`
	CanLaunchWebBrowser                  bool   `json:"CanLaunchWebBrowser"`
	ProgramDataPath                      string `json:"ProgramDataPath"`
	WebPath                              string `json:"WebPath"`
	ItemsByNamePath                      string `json:"ItemsByNamePath"`
	CachePath                            string `json:"CachePath"`
	LogPath                              string `json:"LogPath"`
	InternalMetadataPath                 string `json:"InternalMetadataPath"`
	TranscodingTempPath                  string `json:"TranscodingTempPath"`
	CastReceiverApplications             []any  `json:"CastReceiverApplications"`
	HasUpdateAvailable                   bool   `json:"HasUpdateAvailable"`
	EncoderLocation                      string `json:"EncoderLocation"`
	SystemArchitecture                   string `json:"SystemArchitecture"`
	HardwareAccelerationRequiresPremiere bool   `json:"HardwareAccelerationRequiresPremiere"`
}

// QuickConnect is not offered: homeflix signs in with name and password only.
const quickConnectEnabled = false

func (s *Server) registerSystem(rt *Router) {
	rt.HandleFunc(http.MethodGet, "/System/Info/Public", s.publicSystemInfo)
	rt.HandleFunc(http.MethodGet, "/System/Info", s.authed(s.systemInfo))
	rt.HandleFunc(http.MethodGet, "/System/Ping", s.ping)
	rt.HandleFunc(http.MethodPost, "/System/Ping", s.ping)
	rt.HandleFunc(http.MethodGet, "/Branding/Configuration", s.brandingConfiguration)
	rt.HandleFunc(http.MethodGet, "/QuickConnect/Enabled", s.quickConnectEnabled)
}

func (s *Server) publicInfo(r *http.Request) PublicSystemInfo {
	return PublicSystemInfo{
		LocalAddress:           localAddress(r),
		ServerName:             s.ServerName,
		Version:                CompatVersion,
		ProductName:            ProductName,
		ID:                     s.ServerID,
		StartupWizardCompleted: true,
	}
}

func (s *Server) publicSystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.publicInfo(r))
}

func (s *Server) systemInfo(w http.ResponseWriter, r *http.Request, sess *Session) {
	writeJSON(w, http.StatusOK, SystemInfo{
		PublicSystemInfo:           s.publicInfo(r),
		OperatingSystemDisplayName: runtime.GOOS,
		PackageName:                "homeflix",
		CompletedInstallations:     []any{},
		CastReceiverApplications:   []any{},
		SystemArchitecture:         runtime.GOARCH,
	})
}

func (s *Server) quickConnectEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, quickConnectEnabled)
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
