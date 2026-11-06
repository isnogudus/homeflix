package jellyfin

import (
	"encoding/json"
	"time"

	"homeflix/internal/store"
)

// UserDto describes a user to clients. Clients require Configuration to be
// an object.
type UserDto struct {
	Name                      string         `json:"Name"`
	ServerID                  string         `json:"ServerId"`
	ID                        string         `json:"Id"`
	HasPassword               bool           `json:"HasPassword"`
	HasConfiguredPassword     bool           `json:"HasConfiguredPassword"`
	HasConfiguredEasyPassword bool           `json:"HasConfiguredEasyPassword"`
	EnableAutoLogin           bool           `json:"EnableAutoLogin"`
	LastLoginDate             *time.Time     `json:"LastLoginDate,omitempty"`
	LastActivityDate          *time.Time     `json:"LastActivityDate,omitempty"`
	Configuration             map[string]any `json:"Configuration"`
	Policy                    UserPolicy     `json:"Policy"`
}

// UserPolicy lists what a user may do. homeflix has no per-user
// restrictions beyond the administrator flag and the conversion level;
// features the server does not offer are reported as disabled.
type UserPolicy struct {
	IsAdministrator                  bool     `json:"IsAdministrator"`
	IsHidden                         bool     `json:"IsHidden"`
	EnableCollectionManagement       bool     `json:"EnableCollectionManagement"`
	EnableSubtitleManagement         bool     `json:"EnableSubtitleManagement"`
	EnableLyricManagement            bool     `json:"EnableLyricManagement"`
	IsDisabled                       bool     `json:"IsDisabled"`
	BlockedTags                      []string `json:"BlockedTags"`
	AllowedTags                      []string `json:"AllowedTags"`
	EnableUserPreferenceAccess       bool     `json:"EnableUserPreferenceAccess"`
	AccessSchedules                  []any    `json:"AccessSchedules"`
	BlockUnratedItems                []string `json:"BlockUnratedItems"`
	EnableRemoteControlOfOtherUsers  bool     `json:"EnableRemoteControlOfOtherUsers"`
	EnableSharedDeviceControl        bool     `json:"EnableSharedDeviceControl"`
	EnableRemoteAccess               bool     `json:"EnableRemoteAccess"`
	EnableLiveTvManagement           bool     `json:"EnableLiveTvManagement"`
	EnableLiveTvAccess               bool     `json:"EnableLiveTvAccess"`
	EnableMediaPlayback              bool     `json:"EnableMediaPlayback"`
	EnableAudioPlaybackTranscoding   bool     `json:"EnableAudioPlaybackTranscoding"`
	EnableVideoPlaybackTranscoding   bool     `json:"EnableVideoPlaybackTranscoding"`
	EnablePlaybackRemuxing           bool     `json:"EnablePlaybackRemuxing"`
	ForceRemoteSourceTranscoding     bool     `json:"ForceRemoteSourceTranscoding"`
	EnableContentDeletion            bool     `json:"EnableContentDeletion"`
	EnableContentDeletionFromFolders []string `json:"EnableContentDeletionFromFolders"`
	EnableContentDownloading         bool     `json:"EnableContentDownloading"`
	EnableSyncTranscoding            bool     `json:"EnableSyncTranscoding"`
	EnableMediaConversion            bool     `json:"EnableMediaConversion"`
	EnabledDevices                   []string `json:"EnabledDevices"`
	EnableAllDevices                 bool     `json:"EnableAllDevices"`
	EnabledChannels                  []string `json:"EnabledChannels"`
	EnableAllChannels                bool     `json:"EnableAllChannels"`
	EnabledFolders                   []string `json:"EnabledFolders"`
	EnableAllFolders                 bool     `json:"EnableAllFolders"`
	InvalidLoginAttemptCount         int      `json:"InvalidLoginAttemptCount"`
	LoginAttemptsBeforeLockout       int      `json:"LoginAttemptsBeforeLockout"`
	MaxActiveSessions                int      `json:"MaxActiveSessions"`
	EnablePublicSharing              bool     `json:"EnablePublicSharing"`
	BlockedMediaFolders              []string `json:"BlockedMediaFolders"`
	BlockedChannels                  []string `json:"BlockedChannels"`
	RemoteClientBitrateLimit         int      `json:"RemoteClientBitrateLimit"`
	AuthenticationProviderID         string   `json:"AuthenticationProviderId"`
	PasswordResetProviderID          string   `json:"PasswordResetProviderId"`
	SyncPlayAccess                   string   `json:"SyncPlayAccess"`
}

func policyFor(u store.User) UserPolicy {
	return UserPolicy{
		IsAdministrator:            u.IsAdmin,
		BlockedTags:                []string{},
		AllowedTags:                []string{},
		EnableUserPreferenceAccess: true,
		AccessSchedules:            []any{},
		BlockUnratedItems:          []string{},
		EnableRemoteAccess:         true,
		EnableMediaPlayback:        true,
		// As in Jellyfin: remuxing is changing the container, and with
		// video copied the audio may be converted; video transcoding is
		// the level above.
		EnablePlaybackRemuxing:           u.Conversion.Remux(),
		EnableAudioPlaybackTranscoding:   u.Conversion.Remux(),
		EnableVideoPlaybackTranscoding:   u.Conversion.Transcode(),
		EnableContentDeletionFromFolders: []string{},
		EnableContentDownloading:         u.CanDownload,
		EnabledDevices:                   []string{},
		EnableAllDevices:                 true,
		EnabledChannels:                  []string{},
		EnableAllChannels:                true,
		EnabledFolders:                   []string{},
		EnableAllFolders:                 true,
		LoginAttemptsBeforeLockout:       -1,
		BlockedMediaFolders:              []string{},
		BlockedChannels:                  []string{},
		AuthenticationProviderID:         "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
		PasswordResetProviderID:          "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
		SyncPlayAccess:                   "None",
	}
}

// defaultUserConfiguration mirrors Jellyfin's defaults for a new user.
func defaultUserConfiguration() map[string]any {
	return map[string]any{
		"AudioLanguagePreference":    "",
		"PlayDefaultAudioTrack":      true,
		"SubtitleLanguagePreference": "",
		"DisplayMissingEpisodes":     false,
		"GroupedFolders":             []string{},
		"SubtitleMode":               "Default",
		"DisplayCollectionsView":     false,
		"EnableLocalPassword":        false,
		"OrderedViews":               []string{},
		"LatestItemsExcludes":        []string{},
		"MyMediaExcludes":            []string{},
		"HidePlayedInLatest":         true,
		"RememberAudioSelections":    true,
		"RememberSubtitleSelections": true,
		"EnableNextEpisodeAutoPlay":  true,
		"CastReceiverId":             "",
	}
}

// overlayJSON copies the keys of a stored JSON object over base. Invalid
// stored JSON leaves base unchanged.
func overlayJSON(base map[string]any, stored string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(stored), &m) == nil {
		for k, v := range m {
			base[k] = v
		}
	}
	return base
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Server) userDto(u store.User) UserDto {
	return UserDto{
		Name:                  u.Name,
		ServerID:              s.ServerID,
		ID:                    u.ID,
		HasPassword:           true,
		HasConfiguredPassword: true,
		LastLoginDate:         timePtr(u.LastLoginAt),
		LastActivityDate:      timePtr(u.LastActivityAt),
		Configuration:         overlayJSON(defaultUserConfiguration(), u.Configuration),
		Policy:                policyFor(u),
	}
}

// SessionInfoDto describes one signed-in device.
type SessionInfoDto struct {
	PlayState                PlayState    `json:"PlayState"`
	AdditionalUsers          []any        `json:"AdditionalUsers"`
	Capabilities             Capabilities `json:"Capabilities"`
	RemoteEndPoint           string       `json:"RemoteEndPoint"`
	PlayableMediaTypes       []string     `json:"PlayableMediaTypes"`
	ID                       string       `json:"Id"`
	UserID                   string       `json:"UserId"`
	UserName                 string       `json:"UserName"`
	Client                   string       `json:"Client"`
	LastActivityDate         time.Time    `json:"LastActivityDate"`
	DeviceName               string       `json:"DeviceName"`
	DeviceID                 string       `json:"DeviceId"`
	ApplicationVersion       string       `json:"ApplicationVersion"`
	IsActive                 bool         `json:"IsActive"`
	SupportsMediaControl     bool         `json:"SupportsMediaControl"`
	SupportsRemoteControl    bool         `json:"SupportsRemoteControl"`
	NowPlayingQueue          []any        `json:"NowPlayingQueue"`
	NowPlayingQueueFullItems []any        `json:"NowPlayingQueueFullItems"`
	HasCustomDeviceName      bool         `json:"HasCustomDeviceName"`
	ServerID                 string       `json:"ServerId"`
	SupportedCommands        []string     `json:"SupportedCommands"`
}

// PlayState is the playback state of a session.
type PlayState struct {
	CanSeek       bool   `json:"CanSeek"`
	IsPaused      bool   `json:"IsPaused"`
	IsMuted       bool   `json:"IsMuted"`
	RepeatMode    string `json:"RepeatMode"`
	PlaybackOrder string `json:"PlaybackOrder"`
}

// Capabilities are what a client device announced it can do.
type Capabilities struct {
	PlayableMediaTypes           []string `json:"PlayableMediaTypes"`
	SupportedCommands            []string `json:"SupportedCommands"`
	SupportsMediaControl         bool     `json:"SupportsMediaControl"`
	SupportsPersistentIdentifier bool     `json:"SupportsPersistentIdentifier"`
}

func (s *Server) sessionDto(t store.Token, u store.User) SessionInfoDto {
	return SessionInfoDto{
		PlayState:                PlayState{RepeatMode: "RepeatNone", PlaybackOrder: "Default"},
		AdditionalUsers:          []any{},
		Capabilities:             Capabilities{PlayableMediaTypes: []string{}, SupportedCommands: []string{}, SupportsPersistentIdentifier: true},
		RemoteEndPoint:           t.RemoteAddr,
		PlayableMediaTypes:       []string{},
		ID:                       t.SessionID,
		UserID:                   u.ID,
		UserName:                 u.Name,
		Client:                   t.Client,
		LastActivityDate:         t.LastSeenAt.UTC(),
		DeviceName:               t.DeviceName,
		DeviceID:                 t.DeviceID,
		ApplicationVersion:       t.ClientVersion,
		IsActive:                 true,
		NowPlayingQueue:          []any{},
		NowPlayingQueueFullItems: []any{},
		ServerID:                 s.ServerID,
		SupportedCommands:        []string{},
	}
}
