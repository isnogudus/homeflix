// Package tmdb reads movie metadata from The Movie Database API (v3,
// authenticated with an API Read Access Token).
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Doer sends HTTP requests; in production an outbound.Client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Hosts are the hosts the client talks to.
var Hosts = []string{"api.themoviedb.org", "image.tmdb.org"}

const (
	apiBase   = "https://api.themoviedb.org/3"
	imageBase = "https://image.tmdb.org/t/p/"
	// minInterval spaces requests well below TMDB's rate limit.
	minInterval = 100 * time.Millisecond
)

// ErrNotFound is returned when TMDB has no such movie.
var ErrNotFound = errors.New("not found at TMDB")

// Client queries TMDB.
type Client struct {
	HTTP  Doer
	Token string
	// APIBase and ImageBase override the TMDB URLs in tests.
	APIBase, ImageBase string

	mu   sync.Mutex
	next time.Time
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	at := c.next
	if at.Before(now) {
		at = now
	}
	c.next = at.Add(minInterval)
	c.mu.Unlock()
	if d := time.Until(at); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// get fetches a URL, waiting for the rate limit and retrying once after
// "too many requests" or a server error.
func (c *Client) get(ctx context.Context, u string, auth bool) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if auth {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, errors.New("TMDB rejected the API Read Access Token")
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt == 0:
			delay := 2 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 60 {
				delay = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		return nil, fmt.Errorf("TMDB: HTTP %d", resp.StatusCode)
	}
}

func (c *Client) api(ctx context.Context, path string, q url.Values, v any) error {
	base := c.APIBase
	if base == "" {
		base = apiBase
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, err := c.get(ctx, u, true)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// FindByIMDb returns the TMDB ID of the movie with an IMDb ID.
func (c *Client) FindByIMDb(ctx context.Context, imdbID string) (int, error) {
	var r struct {
		MovieResults []struct {
			ID int `json:"id"`
		} `json:"movie_results"`
	}
	if err := c.api(ctx, "/find/"+url.PathEscape(imdbID), url.Values{"external_source": {"imdb_id"}}, &r); err != nil {
		return 0, err
	}
	if len(r.MovieResults) == 0 {
		return 0, ErrNotFound
	}
	return r.MovieResults[0].ID, nil
}

// SearchResult is a candidate of a title search.
type SearchResult struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	ReleaseDate   string  `json:"release_date"`
	Popularity    float64 `json:"popularity"`
}

// Year returns the release year, or 0.
func (r SearchResult) Year() int {
	y, _ := strconv.Atoi(strings.SplitN(r.ReleaseDate, "-", 2)[0])
	return y
}

// Search looks for movies by title; year 0 means any year.
func (c *Client) Search(ctx context.Context, title string, year int, language string) ([]SearchResult, error) {
	q := url.Values{"query": {title}, "include_adult": {"false"}, "language": {language}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var r struct {
		Results []SearchResult `json:"results"`
	}
	if err := c.api(ctx, "/search/movie", q, &r); err != nil {
		return nil, err
	}
	return r.Results, nil
}

// Person is a member of cast or crew.
type Person struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"` // character played
	Type string `json:"type"`           // Actor, Director or Writer
}

// Movie is the metadata homeflix keeps.
type Movie struct {
	ID            int      `json:"id"`
	IMDbID        string   `json:"imdb_id,omitempty"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Overview      string   `json:"overview,omitempty"`
	Tagline       string   `json:"tagline,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Studios       []string `json:"studios,omitempty"`
	People        []Person `json:"people,omitempty"`
	ReleaseDate   string   `json:"release_date,omitempty"`
	Rating        float64  `json:"rating,omitempty"`
	Certification string   `json:"certification,omitempty"`
	PosterPath    string   `json:"poster_path,omitempty"`
	BackdropPath  string   `json:"backdrop_path,omitempty"`
}

// maxCast limits the actors kept per movie.
const maxCast = 20

type movieResponse struct {
	ID            int     `json:"id"`
	IMDbID        string  `json:"imdb_id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Overview      string  `json:"overview"`
	Tagline       string  `json:"tagline"`
	ReleaseDate   string  `json:"release_date"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	Genres        []struct {
		Name string `json:"name"`
	} `json:"genres"`
	ProductionCompanies []struct {
		Name string `json:"name"`
	} `json:"production_companies"`
	Credits struct {
		Cast []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			Character string `json:"character"`
			Order     int    `json:"order"`
		} `json:"cast"`
		Crew []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Job  string `json:"job"`
		} `json:"crew"`
	} `json:"credits"`
	ReleaseDates struct {
		Results []struct {
			Country      string `json:"iso_3166_1"`
			ReleaseDates []struct {
				Certification string `json:"certification"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
}

// Movie fetches the details of a movie in language ("de-DE"). The age
// rating is taken for the language's country, else the US.
func (c *Client) Movie(ctx context.Context, id int, language string) (Movie, error) {
	var r movieResponse
	q := url.Values{"language": {language}, "append_to_response": {"credits,release_dates"}}
	if err := c.api(ctx, "/movie/"+strconv.Itoa(id), q, &r); err != nil {
		return Movie{}, err
	}
	m := Movie{
		ID: r.ID, IMDbID: r.IMDbID, Title: r.Title, OriginalTitle: r.OriginalTitle,
		Overview: strings.TrimSpace(r.Overview), Tagline: strings.TrimSpace(r.Tagline),
		ReleaseDate: r.ReleaseDate, PosterPath: r.PosterPath, BackdropPath: r.BackdropPath,
	}
	if r.VoteCount > 0 {
		m.Rating = r.VoteAverage
	}
	for _, g := range r.Genres {
		m.Genres = append(m.Genres, g.Name)
	}
	for _, s := range r.ProductionCompanies {
		m.Studios = append(m.Studios, s.Name)
	}
	for _, p := range r.Credits.Crew {
		switch p.Job {
		case "Director":
			m.People = append(m.People, Person{ID: p.ID, Name: p.Name, Type: "Director"})
		case "Screenplay", "Writer":
			m.People = append(m.People, Person{ID: p.ID, Name: p.Name, Type: "Writer"})
		}
	}
	for i, p := range r.Credits.Cast {
		if i >= maxCast {
			break
		}
		m.People = append(m.People, Person{ID: p.ID, Name: p.Name, Role: p.Character, Type: "Actor"})
	}
	country := "US"
	if _, cc, ok := strings.Cut(language, "-"); ok {
		country = cc
	}
	if cert := r.certification(country); cert != "" {
		m.Certification = certificationName(country, cert)
	} else if country != "US" {
		m.Certification = r.certification("US")
	}
	return m, nil
}

// certification returns the first age rating given for a country.
func (r movieResponse) certification(country string) string {
	for _, res := range r.ReleaseDates.Results {
		if res.Country != country {
			continue
		}
		for _, d := range res.ReleaseDates {
			if !NotRated(d.Certification) {
				return d.Certification
			}
		}
	}
	return ""
}

// NotRated reports whether a certification says nothing: empty, or "not
// rated" in one of its spellings.
func NotRated(cert string) bool {
	switch strings.ToUpper(strings.TrimSpace(cert)) {
	case "", "NR", "UNRATED", "NOT RATED":
		return true
	}
	return false
}

// certificationName writes age ratings as clients show them, e.g. FSK-12.
func certificationName(country, cert string) string {
	switch country {
	case "DE":
		return "FSK-" + cert
	case "AT":
		return "AT-" + cert
	case "CH":
		return "CH-" + cert
	}
	return cert
}

// Image fetches an image in a TMDB size such as "w780" or "original".
func (c *Client) Image(ctx context.Context, path, size string) ([]byte, error) {
	base := c.ImageBase
	if base == "" {
		base = imageBase
	}
	return c.get(ctx, base+size+path, false)
}
