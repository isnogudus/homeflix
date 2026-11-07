// Package probe reads stream information from media files with ffprobe.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"homeflix/internal/store"
)

// FFprobe runs the ffprobe binary.
type FFprobe struct {
	Path    string        // binary; looked up in PATH unless absolute
	Timeout time.Duration // per file; 0 means two minutes
}

// maxErrorLen caps the stored error text.
const maxErrorLen = 500

type summary struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []json.RawMessage `json:"streams"`
}

// Probe returns the compacted ffprobe JSON of path with format, streams,
// chapters and codec extradata, or the reason it failed in Err. Extradata
// is needed because current ffprobe no longer reports H.264 reference
// frames; they are read from the sequence parameter set instead.
func (p FFprobe) Probe(ctx context.Context, path string) store.ProbeResult {
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Path, "-v", "error", "-hide_banner", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters", "-show_data", "-i", "file:"+path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return store.ProbeResult{Err: fmt.Sprintf("ffprobe timed out after %s", timeout)}
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return store.ProbeResult{Err: truncate(msg)}
	}
	return parse(stdout.Bytes())
}

func parse(out []byte) store.ProbeResult {
	var s summary
	if err := json.Unmarshal(out, &s); err != nil {
		return store.ProbeResult{Err: truncate("invalid ffprobe output: " + err.Error())}
	}
	if len(s.Streams) == 0 {
		return store.ProbeResult{Err: "no streams found"}
	}
	out = trimTags(out)
	var compact bytes.Buffer
	if err := json.Compact(&compact, out); err != nil {
		return store.ProbeResult{Err: truncate("invalid ffprobe output: " + err.Error())}
	}
	r := store.ProbeResult{JSON: compact.String(), Container: s.Format.FormatName}
	if d, err := strconv.ParseFloat(s.Format.Duration, 64); err == nil {
		r.Duration = time.Duration(d * float64(time.Second))
	}
	if b, err := strconv.ParseInt(s.Format.BitRate, 10, 64); err == nil {
		r.BitRate = b
	}
	return r
}

// maxTagLen caps tag values kept from a probe; lyrics are kept whole.
const maxTagLen = 4096

// trimTags drops what music files carry in their tags but nobody reads:
// private ID3 frames (shop receipts, player databases) and overlong
// values. Lyrics stay, up to 64 KiB.
func trimTags(out []byte) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(out, &doc) != nil {
		return out
	}
	changed := false
	trim := func(raw json.RawMessage) (json.RawMessage, bool) {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return raw, false
		}
		var tags map[string]string
		if json.Unmarshal(obj["tags"], &tags) != nil || len(tags) == 0 {
			return raw, false
		}
		dropped := false
		for k, v := range tags {
			lk := strings.ToLower(k)
			limit := maxTagLen
			if strings.Contains(lk, "lyrics") {
				limit = 64 << 10
			}
			if strings.HasPrefix(lk, "id3v2_priv") || len(v) > limit {
				delete(tags, k)
				dropped = true
			}
		}
		if !dropped {
			return raw, false
		}
		obj["tags"], _ = json.Marshal(tags)
		b, err := json.Marshal(obj)
		return b, err == nil
	}
	if f, ok := trim(doc["format"]); ok {
		doc["format"], changed = f, true
	}
	var streams []json.RawMessage
	if json.Unmarshal(doc["streams"], &streams) == nil {
		sc := false
		for i, st := range streams {
			if t, ok := trim(st); ok {
				streams[i], sc = t, true
			}
		}
		if sc {
			doc["streams"], _ = json.Marshal(streams)
			changed = true
		}
	}
	if !changed {
		return out
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return out
	}
	return b
}

func truncate(s string) string {
	if len(s) > maxErrorLen {
		return s[:maxErrorLen] + "…"
	}
	return s
}
