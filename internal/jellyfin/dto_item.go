package jellyfin

import (
	"strconv"
	"strings"
	"time"

	"homeflix/internal/catalog"
	"homeflix/internal/images"
	"homeflix/internal/media"
	"homeflix/internal/playstate"
	"homeflix/internal/store"
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
	People                  []PersonDto                  `json:"People"`
	Studios                 []NameIDPair                 `json:"Studios"`
	GenreItems              []NameIDPair                 `json:"GenreItems"`
	CommunityRating         *float64                     `json:"CommunityRating,omitempty"`
	OfficialRating          string                       `json:"OfficialRating,omitempty"`
	PremiereDate            *time.Time                   `json:"PremiereDate,omitempty"`
	EndDate                 *time.Time                   `json:"EndDate,omitempty"`
	ProductionLocations     []string                     `json:"ProductionLocations,omitempty"`
	LocalTrailerCount       int                          `json:"LocalTrailerCount"`
	UserData                UserItemDataDto              `json:"UserData"`
	ChildCount              *int                         `json:"ChildCount,omitempty"`
	RecursiveItemCount      *int                         `json:"RecursiveItemCount,omitempty"`
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

	// Music and audiobooks.
	Album                    string       `json:"Album,omitempty"`
	AlbumID                  string       `json:"AlbumId,omitempty"`
	AlbumPrimaryImageTag     string       `json:"AlbumPrimaryImageTag,omitempty"`
	AlbumArtist              string       `json:"AlbumArtist,omitempty"`
	AlbumArtists             []NameIDPair `json:"AlbumArtists,omitempty"`
	Artists                  []string     `json:"Artists,omitempty"`
	ArtistItems              []NameIDPair `json:"ArtistItems,omitempty"`
	IndexNumber              *int         `json:"IndexNumber,omitempty"`
	ParentIndexNumber        *int         `json:"ParentIndexNumber,omitempty"`
	SongCount                *int         `json:"SongCount,omitempty"`
	AlbumCount               *int         `json:"AlbumCount,omitempty"`
	ParentPrimaryImageItemID string       `json:"ParentPrimaryImageItemId,omitempty"`
	ParentPrimaryImageTag    string       `json:"ParentPrimaryImageTag,omitempty"`
	ParentBackdropItemID     string       `json:"ParentBackdropItemId,omitempty"`
	ParentBackdropImageTags  []string     `json:"ParentBackdropImageTags,omitempty"`
	ParentLogoItemID         string       `json:"ParentLogoItemId,omitempty"`
	ParentLogoImageTag       string       `json:"ParentLogoImageTag,omitempty"`
	SeriesName               string       `json:"SeriesName,omitempty"`
	HasLyrics                bool         `json:"HasLyrics,omitempty"`
	NormalizationGain        *float64     `json:"NormalizationGain,omitempty"`
	PlaylistItemID           string       `json:"PlaylistItemId,omitempty"`
}

// NameIDPair names a genre or studio.
type NameIDPair struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

// PersonDto is a member of cast or crew.
type PersonDto struct {
	Name            string `json:"Name"`
	ID              string `json:"Id"`
	Role            string `json:"Role,omitempty"`
	Type            string `json:"Type"`
	PrimaryImageTag string `json:"PrimaryImageTag,omitempty"`
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
	UnplayedItemCount     *int       `json:"UnplayedItemCount,omitempty"`
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
	DirectStreamURL            string            `json:"DirectStreamUrl,omitempty"`
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
	TranscodingURL             string            `json:"TranscodingUrl,omitempty"`
	TranscodingContainer       string            `json:"TranscodingContainer,omitempty"`
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
	// NormalizationGain is the ReplayGain track gain in dB of a track.
	NormalizationGain *float64 `json:"NormalizationGain,omitempty"`
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

// sourceDto describes one file of a version as a media source. The first
// part of a version is the version itself; later parts are sources of
// their own, under their file ID. showPath reveals the file system path,
// which only administrators get to see.
func sourceDto(m *catalog.Movie, v *catalog.Version, index int, showPath bool) MediaSourceInfo {
	part := v.Parts[index]
	info := part.Info
	id := v.ID
	if index > 0 {
		id = part.File.ID
	}
	name := v.Label
	if name == "" {
		name = m.Title
	}
	src := MediaSourceInfo{
		Protocol:               "File",
		ID:                     id,
		Type:                   "Default",
		Container:              info.Container,
		Size:                   part.File.Size,
		Name:                   name,
		RunTimeTicks:           ticks(info.Duration),
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
		src.Path = part.File.Path
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

// userDataDto describes the user's state of an item that runs for runtime.
func userDataDto(id string, d store.UserData, runtime time.Duration) UserItemDataDto {
	u := UserItemDataDto{
		PlaybackPositionTicks: ticks(d.Position),
		PlayCount:             d.PlayCount,
		IsFavorite:            d.IsFavorite,
		Played:                d.Played,
		LastPlayedDate:        timePtr(d.LastPlayed),
		Key:                   id,
		ItemID:                id,
	}
	if d.Position > 0 && runtime > 0 {
		u.PlayedPercentage = 100 * float64(d.Position) / float64(runtime)
	}
	return u
}

// runtimeOf is the duration playback positions of a movie refer to.
func runtimeOf(m *catalog.Movie) time.Duration { return playstate.Runtime(m) }

func chapterDtos(info media.Info) []ChapterInfoDto {
	out := []ChapterInfoDto{}
	for _, c := range info.Chapters {
		out = append(out, ChapterInfoDto{StartPositionTicks: ticks(c.Start), Name: c.Name})
	}
	return out
}

// baseDto holds the fields movies, extras and parts share.
func (s *Server) baseDto(m *catalog.Movie, id string) BaseItemDto {
	d := BaseItemDto{
		Name:              m.Title,
		ServerID:          s.ServerID,
		ID:                id,
		DateCreated:       timePtr(m.DateCreated),
		SortName:          m.SortTitle,
		ExternalUrls:      []ExternalURL{},
		Taglines:          []string{},
		Genres:            []string{},
		ProductionYear:    m.Year,
		ProviderIDs:       map[string]string{},
		ParentID:          m.LibraryID,
		Type:              m.Type,
		People:            []PersonDto{},
		Studios:           []NameIDPair{},
		GenreItems:        []NameIDPair{},
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
		d.ExternalUrls = append(d.ExternalUrls, ExternalURL{Name: "TMDB", URL: "https://www.themoviedb.org/movie/" + m.TMDbID})
	}
	d.Name = m.Name()
	if tag := images.Tag(m, images.Primary); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = images.AspectRatio(m)
		if !m.IsExtra() {
			backdrop := images.Tag(m, images.Backdrop)
			d.BackdropImageTags = []string{backdrop}
			d.ImageTags[string(images.Thumb)] = backdrop
		}
	}
	return d
}

// describeMeta adds what TMDB knows; fields gates the larger parts. The
// snapshot provides the portraits of the people.
func describeMeta(d *BaseItemDto, m *catalog.Movie, fields fieldSet, snap *catalog.Snapshot) {
	meta := m.Meta
	if meta == nil {
		return
	}
	if meta.OriginalTitle != "" && meta.OriginalTitle != d.Name {
		d.OriginalTitle = meta.OriginalTitle
	}
	if fields.has("Overview") {
		d.Overview = meta.Overview
	}
	if fields.has("Taglines") && meta.Tagline != "" {
		d.Taglines = []string{meta.Tagline}
	}
	if fields.has("Genres") {
		d.Genres = append([]string{}, meta.Genres...)
		for _, g := range meta.Genres {
			d.GenreItems = append(d.GenreItems, NameIDPair{Name: g, ID: catalog.GenreID(g)})
		}
	}
	if fields.has("People") {
		for _, p := range meta.People {
			dto := PersonDto{Name: p.Name, ID: catalog.PersonID(p.ID), Role: p.Role, Type: p.Type}
			if f, ok := snap.Person(dto.ID); ok {
				dto.PrimaryImageTag = images.PhotoTag(f)
			}
			d.People = append(d.People, dto)
		}
	}
	if fields.has("Studios") {
		for _, st := range meta.Studios {
			d.Studios = append(d.Studios, NameIDPair{Name: st, ID: catalog.StudioID(st)})
		}
	}
	if meta.Rating > 0 {
		r := meta.Rating
		d.CommunityRating = &r
	}
	d.OfficialRating = meta.Certification
	d.PremiereDate = timePtr(meta.Premiere())
}

func describeFile(d *BaseItemDto, info media.Info) {
	d.Container = info.Container
	d.RunTimeTicks = ticks(info.Duration)
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
}

// itemDto describes a movie or extra. A nil fieldSet means all fields, as
// for single-item requests. user holds the requesting user's state.
func (s *Server) itemDto(m *catalog.Movie, fields fieldSet, sess *Session, user map[string]store.UserData) BaseItemDto {
	switch {
	case m.Set != nil:
		return s.collectionDto(m.Set, fields, user)
	case m.Track != nil:
		return s.trackDto(m, fields, sess, user)
	case m.Book != nil:
		return s.bookDto(m, fields, sess, user)
	}
	d := s.baseDto(m, m.ID)
	d.CanDownload = sess != nil && sess.User.CanDownload
	describeMeta(&d, m, fields, s.Catalog.Snapshot())
	d.UserData = userDataDto(m.ID, user[m.ID], runtimeOf(m))
	for _, x := range m.Extras {
		if x.ExtraType == "Trailer" {
			d.LocalTrailerCount++
		} else {
			d.SpecialFeatureCount++
		}
	}
	if len(m.Versions) == 0 {
		return d
	}
	info := m.Versions[0].Info()
	describeFile(&d, info)
	if n := m.PartCount(); n > 1 {
		d.PartCount = n
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
			d.MediaSources = append(d.MediaSources, sourceDto(m, v, 0, showPath))
		}
	}
	if fields.has("MediaStreams") {
		d.MediaStreams = streamDtos(info)
	}
	if fields.has("Chapters") {
		d.Chapters = chapterDtos(info)
	}
	return d
}

// partDto describes a later part of a split movie as an item of its own,
// as Jellyfin lists additional parts.
func (s *Server) partDto(r catalog.PartRef, fields fieldSet, sess *Session, user map[string]store.UserData) BaseItemDto {
	part := r.Part()
	id := part.File.ID
	d := s.baseDto(r.Movie, id)
	d.CanDownload = sess != nil && sess.User.CanDownload
	describeMeta(&d, r.Movie, fields, s.Catalog.Snapshot())
	d.Name = r.Movie.Name() + " – " + s.Catalog.Locale.PartName(r.Index+1)
	d.UserData = userDataDto(id, user[id], part.Info.Duration)
	describeFile(&d, part.Info)
	showPath := sess.User.IsAdmin && fields.has("Path")
	if showPath {
		d.Path = part.File.Path
	}
	if fields.has("MediaSources") {
		d.MediaSources = []MediaSourceInfo{sourceDto(r.Movie, r.Version, r.Index, showPath)}
	}
	if fields.has("MediaStreams") {
		d.MediaStreams = streamDtos(part.Info)
	}
	if fields.has("Chapters") {
		d.Chapters = chapterDtos(part.Info)
	}
	return d
}

// facetDto describes a genre, person or studio as an item of its own;
// fields gates the biography of a person.
func (s *Server) facetDto(f *catalog.Facet, typ string, fields fieldSet) BaseItemDto {
	n := f.Movies
	d := BaseItemDto{
		Name:              f.Name,
		ServerID:          s.ServerID,
		ID:                f.ID,
		SortName:          strings.ToLower(f.Name),
		ExternalUrls:      []ExternalURL{},
		Taglines:          []string{},
		Genres:            []string{},
		ProviderIDs:       map[string]string{},
		IsFolder:          true,
		Type:              typ,
		People:            []PersonDto{},
		Studios:           []NameIDPair{},
		GenreItems:        []NameIDPair{},
		UserData:          UserItemDataDto{Key: f.ID, ItemID: f.ID},
		ChildCount:        &n,
		Tags:              []string{},
		ImageTags:         map[string]string{},
		BackdropImageTags: []string{},
		ImageBlurHashes:   map[string]map[string]string{},
		LocationType:      "Virtual",
		MediaType:         "Unknown",
		RemoteTrailers:    []any{},
		PlayAccess:        "Full",
	}
	if f.Person != nil {
		describePerson(&d, f, fields)
	}
	return d
}

// describePerson adds what TMDB knows about a person.
func describePerson(d *BaseItemDto, f *catalog.Facet, fields fieldSet) {
	p := f.Person
	if fields.has("Overview") {
		d.Overview = p.Biography
	}
	if t := p.Born(); !t.IsZero() {
		d.PremiereDate = &t
		d.ProductionYear = t.Year()
	}
	if t := p.Died(); !t.IsZero() {
		d.EndDate = &t
	}
	if p.PlaceOfBirth != "" {
		d.ProductionLocations = []string{p.PlaceOfBirth}
	}
	d.ProviderIDs["Tmdb"] = strconv.Itoa(f.TMDbID)
	d.ExternalUrls = append(d.ExternalUrls, ExternalURL{Name: "TMDB", URL: "https://www.themoviedb.org/person/" + strconv.Itoa(f.TMDbID)})
	if p.IMDbID != "" {
		d.ProviderIDs["Imdb"] = p.IMDbID
		d.ExternalUrls = append(d.ExternalUrls, ExternalURL{Name: "IMDb", URL: "https://www.imdb.com/name/" + p.IMDbID})
	}
	if tag := images.PhotoTag(f); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = images.PosterAspect
	}
}

// collectionDto describes a collection (film series) as a Jellyfin box set.
func (s *Server) collectionDto(c *catalog.Collection, fields fieldSet, user map[string]store.UserData) BaseItemDto {
	n := len(c.Movies)
	e := c.Entry()
	tmdbID := strconv.Itoa(c.TMDbID)
	d := BaseItemDto{
		Name:               c.Name,
		ServerID:           s.ServerID,
		ID:                 c.ID,
		DateCreated:        timePtr(e.DateCreated),
		SortName:           e.SortTitle,
		ExternalUrls:       []ExternalURL{{Name: "TMDB", URL: "https://www.themoviedb.org/collection/" + tmdbID}},
		Taglines:           []string{},
		Genres:             []string{},
		ProductionYear:     e.Year,
		ProviderIDs:        map[string]string{"Tmdb": tmdbID},
		IsFolder:           true,
		ParentID:           catalog.CollectionsViewID,
		Type:               "BoxSet",
		People:             []PersonDto{},
		Studios:            []NameIDPair{},
		GenreItems:         []NameIDPair{},
		UserData:           collectionUserData(c, user),
		ChildCount:         &n,
		RecursiveItemCount: &n,
		Tags:               []string{},
		ImageTags:          map[string]string{},
		BackdropImageTags:  []string{},
		ImageBlurHashes:    map[string]map[string]string{},
		LocationType:       "FileSystem",
		MediaType:          "Unknown",
		RemoteTrailers:     []any{},
		PlayAccess:         "Full",
	}
	if fields.has("Overview") {
		d.Overview = c.Overview
	}
	if t := e.Meta; t != nil {
		if p := t.Premiere(); !p.IsZero() {
			d.PremiereDate = &p
		}
	}
	if tag := images.CollectionTag(c, images.Primary); tag != "" {
		d.ImageTags[string(images.Primary)] = tag
		d.PrimaryImageAspectRatio = images.PosterAspect
	}
	if tag := images.CollectionTag(c, images.Backdrop); tag != "" {
		d.BackdropImageTags = []string{tag}
		d.ImageTags[string(images.Thumb)] = tag
	}
	return d
}

// collectionUserData is the user's state of a collection: played when all
// its movies are, a favourite of its own.
func collectionUserData(c *catalog.Collection, user map[string]store.UserData) UserItemDataDto {
	unplayed := 0
	for _, m := range c.Movies {
		if !user[m.ID].Played {
			unplayed++
		}
	}
	own := user[c.ID]
	u := UserItemDataDto{
		IsFavorite:        own.IsFavorite,
		Played:            unplayed == 0,
		UnplayedItemCount: &unplayed,
		Key:               c.ID,
		ItemID:            c.ID,
	}
	if played := len(c.Movies) - unplayed; played > 0 {
		u.PlayedPercentage = 100 * float64(played) / float64(len(c.Movies))
	}
	return u
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
		People:               []PersonDto{},
		Studios:              []NameIDPair{},
		GenreItems:           []NameIDPair{},
		UserData:             UserItemDataDto{Key: l.ID, ItemID: l.ID},
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
