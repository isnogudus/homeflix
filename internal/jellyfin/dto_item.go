package jellyfin

import (
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/media"
)

// BaseItemDto describes a library view, movie or extra.
type BaseItemDto struct {
	Name                    string                       `json:"Name"`
	OriginalTitle           string                       `json:"OriginalTitle,omitempty"`
	ServerID                string                       `json:"ServerId"`
	ID                      string                       `json:"Id"`
	DateCreated             *time.Time                   `json:"DateCreated,omitempty"`
	CanDelete               bool                         `json:"CanDelete"`
	CanDownload             bool                         `json:"CanDownload"`
	HasSubtitles            bool                         `json:"HasSubtitles"`
	Container               string                       `json:"Container,omitempty"`
	SortName                string                       `json:"SortName,omitempty"`
	ExternalUrls            []ExternalURL                `json:"ExternalUrls"`
	MediaSources            []MediaSourceInfo            `json:"MediaSources,omitempty"`
	Path                    string                       `json:"Path,omitempty"`
	Overview                string                       `json:"Overview,omitempty"`
	Taglines                []string                     `json:"Taglines"`
	Genres                  []string                     `json:"Genres"`
	RunTimeTicks            int64                        `json:"RunTimeTicks,omitempty"`
	ProductionYear          int                          `json:"ProductionYear,omitempty"`
	ProviderIDs             map[string]string            `json:"ProviderIds"`
	IsHD                    *bool                        `json:"IsHD,omitempty"`
	IsFolder                bool                         `json:"IsFolder"`
	ParentID                string                       `json:"ParentId,omitempty"`
	Type                    string                       `json:"Type"`
	People                  []any                        `json:"People"`
	Studios                 []any                        `json:"Studios"`
	GenreItems              []any                        `json:"GenreItems"`
	LocalTrailerCount       int                          `json:"LocalTrailerCount"`
	UserData                UserItemDataDto              `json:"UserData"`
	ChildCount              *int                         `json:"ChildCount,omitempty"`
	SpecialFeatureCount     int                          `json:"SpecialFeatureCount"`
	DisplayPreferencesID    string                       `json:"DisplayPreferencesId,omitempty"`
	Tags                    []string                     `json:"Tags"`
	PrimaryImageAspectRatio float64                      `json:"PrimaryImageAspectRatio,omitempty"`
	MediaStreams            []MediaStreamDto             `json:"MediaStreams,omitempty"`
	VideoType               string                       `json:"VideoType,omitempty"`
	PartCount               int                          `json:"PartCount,omitempty"`
	MediaSourceCount        int                          `json:"MediaSourceCount,omitempty"`
	ImageTags               map[string]string            `json:"ImageTags"`
	BackdropImageTags       []string                     `json:"BackdropImageTags"`
	ImageBlurHashes         map[string]map[string]string `json:"ImageBlurHashes"`
	Chapters                []ChapterInfoDto             `json:"Chapters,omitempty"`
	LocationType            string                       `json:"LocationType"`
	MediaType               string                       `json:"MediaType"`
	CollectionType          string                       `json:"CollectionType,omitempty"`
	ExtraType               string                       `json:"ExtraType,omitempty"`
	Width                   int                          `json:"Width,omitempty"`
	Height                  int                          `json:"Height,omitempty"`
	RemoteTrailers          []any                        `json:"RemoteTrailers"`
	PlayAccess              string                       `json:"PlayAccess"`
}

// ExternalURL is a link shown by clients; the server never fetches it.
type ExternalURL struct {
	Name string `json:"Name"`
	URL  string `json:"Url"`
}

// UserItemDataDto is the per-user state of an item.
type UserItemDataDto struct {
	PlaybackPositionTicks int64      `json:"PlaybackPositionTicks"`
	PlayCount             int        `json:"PlayCount"`
	IsFavorite            bool       `json:"IsFavorite"`
	Played                bool       `json:"Played"`
	LastPlayedDate        *time.Time `json:"LastPlayedDate,omitempty"`
	PlayedPercentage      float64    `json:"PlayedPercentage,omitempty"`
	Key                   string     `json:"Key"`
	ItemID                string     `json:"ItemId"`
}

// MediaSourceInfo describes one playable version.
type MediaSourceInfo struct {
	Protocol                   string            `json:"Protocol"`
	ID                         string            `json:"Id"`
	Path                       string            `json:"Path,omitempty"`
	Type                       string            `json:"Type"`
	Container                  string            `json:"Container,omitempty"`
	Size                       int64             `json:"Size,omitempty"`
	Name                       string            `json:"Name"`
	IsRemote                   bool              `json:"IsRemote"`
	ETag                       string            `json:"ETag,omitempty"`
	RunTimeTicks               int64             `json:"RunTimeTicks,omitempty"`
	ReadAtNativeFramerate      bool              `json:"ReadAtNativeFramerate"`
	IgnoreDts                  bool              `json:"IgnoreDts"`
	IgnoreIndex                bool              `json:"IgnoreIndex"`
	GenPtsInput                bool              `json:"GenPtsInput"`
	SupportsTranscoding        bool              `json:"SupportsTranscoding"`
	SupportsDirectStream       bool              `json:"SupportsDirectStream"`
	SupportsDirectPlay         bool              `json:"SupportsDirectPlay"`
	IsInfiniteStream           bool              `json:"IsInfiniteStream"`
	RequiresOpening            bool              `json:"RequiresOpening"`
	RequiresClosing            bool              `json:"RequiresClosing"`
	RequiresLooping            bool              `json:"RequiresLooping"`
	SupportsProbing            bool              `json:"SupportsProbing"`
	VideoType                  string            `json:"VideoType"`
	MediaStreams               []MediaStreamDto  `json:"MediaStreams"`
	MediaAttachments           []any             `json:"MediaAttachments"`
	Formats                    []string          `json:"Formats"`
	Bitrate                    int64             `json:"Bitrate,omitempty"`
	RequiredHTTPHeaders        map[string]string `json:"RequiredHttpHeaders"`
	TranscodingSubProtocol     string            `json:"TranscodingSubProtocol"`
	DefaultAudioStreamIndex    *int              `json:"DefaultAudioStreamIndex,omitempty"`
	DefaultSubtitleStreamIndex *int              `json:"DefaultSubtitleStreamIndex,omitempty"`
	HasSegments                bool              `json:"HasSegments"`
}

// MediaStreamDto is a stream as Jellyfin reports it.
type MediaStreamDto struct {
	Codec                     string  `json:"Codec,omitempty"`
	CodecTag                  string  `json:"CodecTag,omitempty"`
	Language                  string  `json:"Language,omitempty"`
	ColorTransfer             string  `json:"ColorTransfer,omitempty"`
	ColorPrimaries            string  `json:"ColorPrimaries,omitempty"`
	ColorSpace                string  `json:"ColorSpace,omitempty"`
	TimeBase                  string  `json:"TimeBase,omitempty"`
	Title                     string  `json:"Title,omitempty"`
	VideoRange                string  `json:"VideoRange,omitempty"`
	VideoRangeType            string  `json:"VideoRangeType,omitempty"`
	VideoDoViTitle            string  `json:"VideoDoViTitle,omitempty"`
	AudioSpatialFormat        string  `json:"AudioSpatialFormat"`
	DisplayTitle              string  `json:"DisplayTitle,omitempty"`
	NalLengthSize             string  `json:"NalLengthSize,omitempty"`
	IsInterlaced              bool    `json:"IsInterlaced"`
	IsAVC                     *bool   `json:"IsAVC,omitempty"`
	ChannelLayout             string  `json:"ChannelLayout,omitempty"`
	BitRate                   int64   `json:"BitRate,omitempty"`
	BitDepth                  int     `json:"BitDepth,omitempty"`
	RefFrames                 int     `json:"RefFrames,omitempty"`
	IsDefault                 bool    `json:"IsDefault"`
	IsForced                  bool    `json:"IsForced"`
	IsHearingImpaired         bool    `json:"IsHearingImpaired"`
	IsOriginal                bool    `json:"IsOriginal"`
	Height                    int     `json:"Height,omitempty"`
	Width                     int     `json:"Width,omitempty"`
	AverageFrameRate          float64 `json:"AverageFrameRate,omitempty"`
	RealFrameRate             float64 `json:"RealFrameRate,omitempty"`
	ReferenceFrameRate        float64 `json:"ReferenceFrameRate,omitempty"`
	Profile                   string  `json:"Profile,omitempty"`
	Type                      string  `json:"Type"`
	AspectRatio               string  `json:"AspectRatio,omitempty"`
	Index                     int     `json:"Index"`
	IsExternal                bool    `json:"IsExternal"`
	IsTextSubtitleStream      bool    `json:"IsTextSubtitleStream"`
	SupportsExternalStream    bool    `json:"SupportsExternalStream"`
	PixelFormat               string  `json:"PixelFormat,omitempty"`
	Level                     float64 `json:"Level,omitempty"`
	IsAnamorphic              *bool   `json:"IsAnamorphic,omitempty"`
	Channels                  int     `json:"Channels,omitempty"`
	SampleRate                int     `json:"SampleRate,omitempty"`
	DvVersionMajor            *int    `json:"DvVersionMajor,omitempty"`
	DvVersionMinor            *int    `json:"DvVersionMinor,omitempty"`
	DvProfile                 *int    `json:"DvProfile,omitempty"`
	DvLevel                   *int    `json:"DvLevel,omitempty"`
	RpuPresentFlag            *int    `json:"RpuPresentFlag,omitempty"`
	ElPresentFlag             *int    `json:"ElPresentFlag,omitempty"`
	BlPresentFlag             *int    `json:"BlPresentFlag,omitempty"`
	DvBlSignalCompatibilityID *int    `json:"DvBlSignalCompatibilityId,omitempty"`
	DeliveryMethod            string  `json:"DeliveryMethod,omitempty"`
	DeliveryURL               string  `json:"DeliveryUrl,omitempty"`
}

// ChapterInfoDto is a chapter mark.
type ChapterInfoDto struct {
	StartPositionTicks int64     `json:"StartPositionTicks"`
	Name               string    `json:"Name"`
	ImageDateModified  time.Time `json:"ImageDateModified"`
}

func ticks(d time.Duration) int64 { return int64(d / 100) }

func intPtr(v int) *int { return &v }

func streamDto(s media.Stream) MediaStreamDto {
	d := MediaStreamDto{
		Codec: s.Codec, CodecTag: s.CodecTag, Language: s.Language,
		ColorTransfer: s.ColorTransfer, ColorPrimaries: s.ColorPrimaries, ColorSpace: s.ColorSpace,
		TimeBase: s.TimeBase, Title: s.Title, VideoRange: s.VideoRange, VideoRangeType: s.VideoRangeType,
		AudioSpatialFormat: s.AudioSpatialFormat, DisplayTitle: s.DisplayTitle, NalLengthSize: s.NalLengthSize,
		IsInterlaced: s.IsInterlaced, IsAVC: s.IsAVC, ChannelLayout: s.ChannelLayout, BitRate: s.BitRate,
		BitDepth: s.BitDepth, RefFrames: s.RefFrames,
		IsDefault: s.IsDefault, IsForced: s.IsForced, IsHearingImpaired: s.IsHearingImpaired, IsOriginal: s.IsOriginal,
		Height: s.Height, Width: s.Width, AverageFrameRate: s.AverageFrameRate, RealFrameRate: s.RealFrameRate,
		Profile: s.Profile, Type: s.Type, AspectRatio: s.AspectRatio, Index: s.Index,
		IsTextSubtitleStream: s.IsTextSubtitleStream, SupportsExternalStream: s.SupportsExternalStream,
		PixelFormat: s.PixelFormat, Level: s.Level, Channels: s.Channels, SampleRate: s.SampleRate,
	}
	if d.AudioSpatialFormat == "" {
		d.AudioSpatialFormat = "None"
	}
	if s.Type == "Video" {
		d.IsAnamorphic = &s.IsAnamorphic
		d.ReferenceFrameRate = s.AverageFrameRate
		if s.AverageFrameRate == 0 || s.AverageFrameRate >= 1000 {
			d.ReferenceFrameRate = s.RealFrameRate
		}
	}
	if dv := s.DV; dv != nil {
		d.DvVersionMajor, d.DvVersionMinor = intPtr(dv.VersionMajor), intPtr(dv.VersionMinor)
		d.DvProfile, d.DvLevel = intPtr(dv.Profile), intPtr(dv.Level)
		d.RpuPresentFlag, d.ElPresentFlag, d.BlPresentFlag = intPtr(dv.RPUPresent), intPtr(dv.ELPresent), intPtr(dv.BLPresent)
		d.DvBlSignalCompatibilityID = intPtr(dv.BLSignalCompatibilityID)
	}
	return d
}

func streamDtos(info media.Info) []MediaStreamDto {
	out := make([]MediaStreamDto, 0, len(info.Streams))
	for _, s := range info.Streams {
		out = append(out, streamDto(s))
	}
	return out
}

// sourceDto describes one version. showPath reveals the file system path,
// which only administrators get to see.
func sourceDto(m *catalog.Movie, v *catalog.Version, showPath bool) MediaSourceInfo {
	info := v.Info()
	name := v.Label
	if name == "" {
		name = m.Title
	}
	src := MediaSourceInfo{
		Protocol:               "File",
		ID:                     v.ID,
		Type:                   "Default",
		Container:              info.Container,
		Size:                   v.Size,
		Name:                   name,
		RunTimeTicks:           ticks(v.Duration),
		SupportsDirectStream:   true,
		SupportsDirectPlay:     true,
		SupportsProbing:        true,
		VideoType:              "VideoFile",
		MediaStreams:           streamDtos(info),
		MediaAttachments:       []any{},
		Formats:                []string{},
		Bitrate:                info.BitRate,
		RequiredHTTPHeaders:    map[string]string{},
		TranscodingSubProtocol: "http",
	}
	if showPath {
		src.Path = v.Parts[0].File.Path
	}
	if a := info.DefaultAudioIndex(); a >= 0 {
		src.DefaultAudioStreamIndex = intPtr(a)
	}
	src.DefaultSubtitleStreamIndex = intPtr(info.DefaultSubtitleIndex())
	return src
}

// fieldSet is the parsed Fields query parameter.
type fieldSet map[string]bool

func parseFields(r string) fieldSet {
	f := fieldSet{}
	for _, p := range strings.Split(r, ",") {
		if p = strings.TrimSpace(p); p != "" {
			f[strings.ToLower(p)] = true
		}
	}
	return f
}

func (f fieldSet) has(name string) bool { return f == nil || f[strings.ToLower(name)] }

func (s *Server) userData(id string) UserItemDataDto {
	return UserItemDataDto{Key: id, ItemID: id}
}

// itemDto describes a movie or extra. A nil fieldSet means all fields, as
// for single-item requests.
func (s *Server) itemDto(m *catalog.Movie, fields fieldSet, sess *Session) BaseItemDto {
	d := BaseItemDto{
		Name:              m.Title,
		ServerID:          s.ServerID,
		ID:                m.ID,
		DateCreated:       timePtr(m.DateCreated),
		CanDownload:       true,
		SortName:          m.SortTitle,
		ExternalUrls:      []ExternalURL{},
		Taglines:          []string{},
		Genres:            []string{},
		ProductionYear:    m.Year,
		ProviderIDs:       map[string]string{},
		ParentID:          m.LibraryID,
		Type:              m.Type,
		People:            []any{},
		Studios:           []any{},
		GenreItems:        []any{},
		UserData:          s.userData(m.ID),
		Tags:              []string{},
		VideoType:         "VideoFile",
		ImageTags:         map[string]string{},
		BackdropImageTags: []string{},
		ImageBlurHashes:   map[string]map[string]string{},
		LocationType:      "FileSystem",
		MediaType:         "Video",
		ExtraType:         m.ExtraType,
		RemoteTrailers:    []any{},
		PlayAccess:        "Full",
	}
	if m.Parent != nil {
		d.ParentID = m.Parent.ID
	}
	if m.IMDbID != "" {
		d.ProviderIDs["Imdb"] = m.IMDbID
		d.ExternalUrls = append(d.ExternalUrls, ExternalURL{Name: "IMDb", URL: "https://www.imdb.com/title/" + m.IMDbID})
	}
	if m.TMDbID != "" {
		d.ProviderIDs["Tmdb"] = m.TMDbID
	}
	for _, x := range m.Extras {
		if x.ExtraType == "Trailer" {
			d.LocalTrailerCount++
		} else {
			d.SpecialFeatureCount++
		}
	}
	if tag := images.Tag(m, images.Primary); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = images.AspectRatio(m)
		if !m.IsExtra() {
			backdrop := images.Tag(m, images.Backdrop)
			d.BackdropImageTags = []string{backdrop}
			d.ImageTags[string(images.Thumb)] = backdrop
		}
	}
	if len(m.Versions) > 0 {
		def := m.Versions[0]
		info := def.Info()
		d.Container = info.Container
		d.RunTimeTicks = ticks(def.Duration)
		for _, st := range info.Streams {
			if st.Type == "Subtitle" {
				d.HasSubtitles = true
			}
		}
		if v, ok := info.Video(); ok {
			d.Width, d.Height = v.Width, v.Height
			hd := v.Height >= 700 || v.Width >= 1260
			d.IsHD = &hd
		}
		if len(def.Parts) > 1 {
			d.PartCount = len(def.Parts)
		}
		if len(m.Versions) > 1 {
			d.MediaSourceCount = len(m.Versions)
		}
		showPath := sess.User.IsAdmin && fields.has("Path")
		if showPath {
			d.Path = m.Path
		}
		if fields.has("MediaSources") {
			for _, v := range m.Versions {
				d.MediaSources = append(d.MediaSources, sourceDto(m, v, showPath))
			}
		}
		if fields.has("MediaStreams") {
			d.MediaStreams = streamDtos(info)
		}
		if fields.has("Chapters") {
			d.Chapters = []ChapterInfoDto{}
			for _, c := range info.Chapters {
				d.Chapters = append(d.Chapters, ChapterInfoDto{StartPositionTicks: ticks(c.Start), Name: c.Name,
					ImageDateModified: time.Time{}})
			}
		}
	}
	return d
}

// viewDto describes a library as a collection folder.
func (s *Server) viewDto(l catalog.Library) BaseItemDto {
	n := l.Movies
	return BaseItemDto{
		Name:                 l.Name,
		ServerID:             s.ServerID,
		ID:                   l.ID,
		SortName:             strings.ToLower(l.Name),
		ExternalUrls:         []ExternalURL{},
		Taglines:             []string{},
		Genres:               []string{},
		ProviderIDs:          map[string]string{},
		IsFolder:             true,
		Type:                 "CollectionFolder",
		People:               []any{},
		Studios:              []any{},
		GenreItems:           []any{},
		UserData:             s.userData(l.ID),
		ChildCount:           &n,
		DisplayPreferencesID: l.ID,
		Tags:                 []string{},
		ImageTags:            map[string]string{},
		BackdropImageTags:    []string{},
		ImageBlurHashes:      map[string]map[string]string{},
		LocationType:         "FileSystem",
		MediaType:            "Unknown",
		CollectionType:       l.CollectionType,
		RemoteTrailers:       []any{},
		PlayAccess:           "Full",
	}
}
