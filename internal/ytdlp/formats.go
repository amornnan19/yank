package ytdlp

import (
	"math"
	"slices"
	"strconv"
)

// Row is one line of the format picker: the text a person reads, and the exact
// argv fragment that downloading that line runs.
//
// D1 — the row you see is the format you get. Args name the concrete format id
// the ranking chose, so the codec preference and the size in Label describe the
// file that actually lands on disk. Nothing here contains a quote character;
// Args is spliced straight into an exec argv (D4).
type Row struct {
	// Key is unique within one Rank result and stable across re-renders, so the
	// UI can key a list on it. Video rows key on their normalised height, which
	// is unique by construction; the audio row keys on "audio-mp3".
	Key string
	// Label is the rendered line, e.g. "1080p60  mp4  ~142 MB".
	Label string
	// Args is the argv fragment for this choice, e.g.
	// {"-f", "137+ba/137/b[height<=1080]"}.
	Args []string
	// FormatID is the representative yt-dlp format id the row stands for, or ""
	// for the audio row, which asks yt-dlp for the best audio of the moment.
	FormatID string
	// Height is the normalised ladder height, 0 for the audio row.
	Height int
	// FPS is the representative's frame rate, 0 when unknown or audio-only.
	FPS float64
	// Ext is the representative's container. A merged download can land in a
	// different one, so this is the best guess rather than a promise.
	Ext string
	// Bytes is the size shown in Label, including the audio that will be merged
	// in. It is meaningful only when SizeKnown is true.
	Bytes int64
	// SizeKnown is false when nothing in the format info pinned a size down; the
	// label then reads "~?".
	SizeKnown bool
	// AudioOnly marks the extract-audio row.
	AudioOnly bool
}

const (
	// modernCodecSizeAdvantage is D3's number. avc1/h264 wins by default because
	// it plays everywhere; vp9/av01 only takes the row when it is at least this
	// much smaller by the size the label would show.
	modernCodecSizeAdvantage = 0.25

	// maxRows caps the whole picker. maxVideoRows leaves room for the audio row.
	maxRows      = 7
	maxVideoRows = 6

	// maxRenderableFPS bounds what may go in a label. A frame rate past this is
	// not a frame rate, and an out-of-range float64 does not convert to an int
	// in any defined way.
	maxRenderableFPS = 1000.0

	// heightSnapTolerance is how far off a ladder height a format may be and
	// still count as that height. 1078 and 1082 are both 1080; 1920 (a portrait
	// video) is nobody's ladder rung and keeps its own row.
	heightSnapTolerance = 0.05

	// defaultAudioBitrateKbps is the last-resort bitrate for the audio track a
	// merged row will carry, used only when no audio format in the list has a
	// size of its own. Deliberately near the top of what YouTube serves so the
	// estimate errs large: understating a merged row is the failure to avoid.
	defaultAudioBitrateKbps = 128.0
)

// standardHeights is the ladder odd heights snap onto before grouping.
var standardHeights = []int{144, 240, 360, 480, 720, 1080, 1440, 2160, 4320}

// preferredHeights is the shortlist a person actually recognises. When more
// than maxVideoRows heights survive grouping, these are kept first — after the
// highest available, which is always kept even when it is off-ladder.
var preferredHeights = []int{2160, 1440, 1080, 720, 480, 360}

// codecClass groups vcodec strings by how a chooser should treat them.
type codecClass int

const (
	// codecOther is anything unrecognised: it never beats avc1, and only takes a
	// row when it is the sole candidate.
	codecOther codecClass = iota
	// codecModern is vp9/av01 — smaller, but not universally playable.
	codecModern
	// codecLegacy is avc1/h264 — the default winner.
	codecLegacy
)

// protocolClass groups yt-dlp's protocol strings by what a format's tbr means,
// which is what decides whether two of them may be compared.
type protocolClass int

const (
	// protoUnknown is an empty or unrecognised protocol. It is no evidence
	// either way, which is not the same as evidence of a direct stream.
	protoUnknown protocolClass = iota
	// protoDirect is a whole file or a stream fetched in ranges or segments —
	// "https", "http", "http_dash_segments". Its tbr is the bitrate the stream
	// was encoded at, and it usually states a filesize as well.
	protoDirect
	// protoHLS is "m3u8" or "m3u8_native", where tbr comes from the playlist's
	// BANDWIDTH attribute: the peak a player must sustain, not the average the
	// stream holds. It is normally sizeless.
	protoHLS
)

// candidate is one eligible format plus the size its row would display.
type candidate struct {
	f RawFormat
	// height is the normalised ladder height, not f.Height.
	height int
	// bytes is the displayed size: the stream itself plus, for a video-only
	// format that will be merged, the audio track that comes with it.
	bytes int64
	// known is false when no field and no estimate pinned bytes down.
	known bool
	// stated records that the format said how big it is, rather than leaving
	// bytes to be estimated from a bitrate.
	stated bool
	// merged records that this row runs a merge, which is what makes bytes
	// larger than the video stream alone.
	merged bool
}

// Rank reduces the hundred-odd formats of a `yt-dlp -J` payload to at most
// maxRows lines a person can choose between.
//
// hasFFmpeg is an input, not a detail (D2): without ffmpeg nothing can be
// merged and nothing can be transcoded, so only formats that already carry both
// streams are eligible and no audio row is offered.
//
// It is pure — no exec, no files, no network — and nil-safe.
func Rank(info *VideoInfo, hasFFmpeg bool) []Row {
	if info == nil {
		return nil
	}

	audioBytes, audioKnown := mergeAudioSize(info)

	groups := map[int][]candidate{}
	for _, f := range info.Formats {
		c, ok := newCandidate(f, info.Duration, hasFFmpeg, audioBytes, audioKnown)
		if !ok {
			continue
		}
		groups[c.height] = append(groups[c.height], c)
	}

	heights := make([]int, 0, len(groups))
	for h := range groups {
		heights = append(heights, h)
	}
	slices.SortFunc(heights, func(a, b int) int { return b - a })
	heights = selectHeights(heights)

	rows := make([]Row, 0, maxRows)
	for _, h := range heights {
		rows = append(rows, videoRow(pickRepresentative(groups[h]), hasFFmpeg))
	}
	if hasFFmpeg {
		rows = append(rows, audioRow())
	}
	if len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

// newCandidate decides whether one raw format may become a video row, and if so
// works out the size its row would show.
func newCandidate(f RawFormat, duration *float64, hasFFmpeg bool, audioBytes int64, audioKnown bool) (candidate, bool) {
	// Only formats with a real height are video rows. This is also what keeps
	// storyboards out sideways: they report a height but vcodec "none".
	if f.Height <= 0 || !hasVideoStream(f) {
		return candidate{}, false
	}
	// D2: with no ffmpeg there is no merge, so a video-only stream is not a
	// download anyone would want.
	merged := !hasAudioStream(f)
	if !hasFFmpeg && merged {
		return candidate{}, false
	}

	bytes, known := rawSize(f, duration)
	if merged {
		// A merged row that quotes only the video stream understates the file
		// the user ends up with. If the audio side cannot be estimated at all,
		// say "~?" rather than a number that is knowingly too small.
		if known && audioKnown && bytes <= math.MaxInt64-audioBytes {
			bytes += audioBytes
		} else {
			bytes, known = 0, false
		}
	}

	return candidate{
		f:      f,
		height: normaliseHeight(f.Height),
		bytes:  bytes,
		known:  known,
		stated: hasStatedSize(f),
		merged: merged,
	}, true
}

// hasStatedSize reports whether the format said how big it is instead of
// leaving a bitrate to be multiplied out.
func hasStatedSize(f RawFormat) bool { return f.Filesize > 0 || f.FilesizeApprox > 0 }

// hasVideoStream reports whether f carries video. An empty vcodec is treated as
// unknown-but-present: some extractors omit it, and yt-dlp spells the negative
// case out as "none".
func hasVideoStream(f RawFormat) bool { return f.VCodec != "none" }

// hasAudioStream reports whether f already carries audio, i.e. whether playing
// it needs no merge. An empty acodec is unknown, and unknown reads as present,
// the same way yt-dlp's own "b" selector treats it.
func hasAudioStream(f RawFormat) bool { return f.ACodec != "none" }

// isAudioOnlyTrack reports positive evidence that f is an audio stream and
// nothing else.
//
// This is a different question from hasAudioStream, which is why it is a
// different predicate rather than the same expression written twice. There,
// an unknown acodec has to read as present or a progressive file from an
// extractor that omits the field would be sent off to merge an audio track it
// already has. Here we are choosing which format "+ba" will actually pull down
// and adding its bytes to a size we then show the user, and "the field was
// absent" is no evidence that there is any audio in it at all. Unknown is not
// good enough to nominate; the bitrate estimate is the safer answer.
func isAudioOnlyTrack(f RawFormat) bool {
	return f.VCodec == "none" && f.ACodec != "none" && f.ACodec != ""
}

// normaliseHeight snaps a height onto the standard ladder when it is within
// heightSnapTolerance of a rung, so 1078 and 1082 do not both survive grouping
// as their own near-identical rows. A height that is nowhere near a rung is
// returned unchanged and keeps a row of its own.
func normaliseHeight(h int) int {
	best, bestDiff := h, math.Inf(1)
	for _, rung := range standardHeights {
		d := math.Abs(float64(h-rung)) / float64(rung)
		if d <= heightSnapTolerance && d < bestDiff {
			best, bestDiff = rung, d
		}
	}
	return best
}

// selectHeights trims a descending list of heights to maxVideoRows, keeping the
// highest available first — even when it is off-ladder, because "the best this
// video has" is the one row nobody wants silently dropped — then the recognised
// ladder rungs, then whatever is left, highest first.
func selectHeights(desc []int) []int {
	if len(desc) <= maxVideoRows {
		return desc
	}

	keep := make(map[int]bool, maxVideoRows)
	keep[desc[0]] = true
	for _, h := range preferredHeights {
		if len(keep) == maxVideoRows {
			break
		}
		if slices.Contains(desc, h) {
			keep[h] = true
		}
	}
	for _, h := range desc {
		if len(keep) == maxVideoRows {
			break
		}
		keep[h] = true
	}

	out := make([]int, 0, len(keep))
	for _, h := range desc {
		if keep[h] {
			out = append(out, h)
		}
	}
	return out
}

// pickRepresentative applies D3 to one height group: the best avc1/h264 stream
// wins unless the best vp9/av01 stream is at least modernCodecSizeAdvantage
// smaller by the size the row would display. Within a codec class, higher TBR
// wins. A size that is not known cannot clear the threshold, so an unknown size
// never unseats avc1.
//
// The group is never empty: groups exist only because a candidate was appended
// to them.
func pickRepresentative(group []candidate) candidate {
	var legacy, modern, other *candidate
	for i := range group {
		c := &group[i]
		switch classifyCodec(c.f.VCodec) {
		case codecLegacy:
			legacy = betterQuality(legacy, c)
		case codecModern:
			modern = betterQuality(modern, c)
		default:
			other = betterQuality(other, c)
		}
	}

	if legacy == nil {
		if modern != nil {
			return *modern
		}
		return *other
	}
	// The threshold compares two sizes, so the two sizes have to be measuring
	// the same thing. Provenance is readable now, so the guard names the one
	// pair that is not comparable rather than refusing every stated-against-
	// estimated pair: a size estimated off an m3u8 variant's peak bandwidth is
	// inflated by whatever headroom the playlist advertises, and a 25% gap
	// between that and anything else is as likely to be the headroom as the
	// codec. A stated filesize against an estimate off a direct stream's own
	// encoded bitrate is two measurements of the same quantity, one of them
	// rounder than the other, and D3 may run on those.
	if modern != nil && legacy.known && modern.known && peakEstimate(*legacy) == peakEstimate(*modern) &&
		float64(modern.bytes) <= float64(legacy.bytes)*(1-modernCodecSizeAdvantage) {
		return *modern
	}
	return *legacy
}

// peakEstimate reports that a candidate's displayed size was multiplied out of
// an m3u8 variant's peak bandwidth, which overstates it by however much
// headroom the playlist advertises. Such a number may be compared with another
// of its kind and with nothing else.
func peakEstimate(c candidate) bool {
	return !c.stated && classifyProtocol(c.f.Protocol) == protoHLS
}

// betterQuality returns whichever of two same-class candidates should represent
// the height.
//
// An m3u8 variant loses to a sibling served as a direct stream. Sites list the
// same rendition twice, once as a file or a DASH stream that states an exact
// filesize and once as an HLS variant that states none and reports a tbr taken
// from the playlist's BANDWIDTH attribute — the peak a player must sustain, not
// the bitrate the stream was encoded at. On the YouTube list that motivated
// this, one 1080p avc1 stream is format 137 at 3038 kbit/s and format 270 at
// 4688. Ranking on tbr alone hands the row to the m3u8 copy and prints a size
// guessed off a bitrate that was never the file's, which is exactly the
// decoration D1 exists to stop. Two entries this close in the list — same
// height, same codec class — are that rendition listed twice far more often
// than they are two different encodes, and the direct one is the one whose size
// yank can state and whose bytes it can fetch without reassembling a playlist.
//
// The comparison needs positive evidence on both sides, so it fires only
// between a known m3u8 entry and a known direct one. An absent or unrecognised
// protocol is unknown, not "not HLS": read as direct it would let a missing
// field outrank a real m3u8 sibling on no evidence at all, and read as HLS it
// would demote every format from an extractor that omits the field, archive.org's
// progressive files among them if it ever stopped sending one. Unknown
// therefore falls through to the ordinary ranking, which is what it got before
// there was a protocol to read.
//
// Only then does higher tbr win — and between two entries of the same protocol
// class a tbr comparison means something — then a known size over an unknown
// one, then the smaller size, then the lower format id so the choice does not
// depend on map or slice order.
func betterQuality(best, c *candidate) *candidate {
	if best == nil {
		return c
	}
	cProto, bestProto := classifyProtocol(c.f.Protocol), classifyProtocol(best.f.Protocol)
	switch {
	case cProto == protoHLS && bestProto == protoDirect:
		return best
	case cProto == protoDirect && bestProto == protoHLS:
		return c
	case c.f.TBR != best.f.TBR:
		if c.f.TBR > best.f.TBR {
			return c
		}
		return best
	case c.known != best.known:
		if c.known {
			return c
		}
		return best
	case c.known && c.bytes != best.bytes:
		if c.bytes < best.bytes {
			return c
		}
		return best
	case c.f.FormatID < best.f.FormatID:
		return c
	}
	return best
}

// classifyProtocol maps yt-dlp's protocol string onto the classes ranking cares
// about. Unlike a codec name, this string is yt-dlp's own — it comes from a
// fixed set its extractors choose from, not from the site — so an exact match
// is right here where codec names need case folding. "mhtml" is missing on
// purpose: it is a storyboard, and newCandidate has already dropped it on
// vcodec "none" before anything asks what protocol it uses.
func classifyProtocol(protocol string) protocolClass {
	switch protocol {
	case "https", "http", "http_dash_segments":
		return protoDirect
	case "m3u8", "m3u8_native":
		return protoHLS
	}
	return protoUnknown
}

// classifyCodec maps a vcodec string onto the three classes D3 cares about.
// yt-dlp spells them with profile suffixes ("avc1.640028", "vp09.00.50.08"), so
// this matches on the prefix.
func classifyCodec(vcodec string) codecClass {
	switch {
	case hasAnyPrefix(vcodec, "avc1", "avc3", "h264", "h.264"):
		return codecLegacy
	case hasAnyPrefix(vcodec, "vp9", "vp09", "av01", "av1"):
		return codecModern
	default:
		return codecOther
	}
}

// hasAnyPrefix reports whether s starts with any of the prefixes, case-folded.
func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && equalFoldASCII(s[:len(p)], p) {
			return true
		}
	}
	return false
}

// equalFoldASCII compares two strings case-insensitively over ASCII only. The
// codec names it is used on are ASCII, so full Unicode folding buys nothing.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// videoRow renders one representative as a picker line.
func videoRow(c candidate, hasFFmpeg bool) Row {
	return Row{
		Key:       "video-" + strconv.Itoa(c.height),
		Label:     videoLabel(c),
		Args:      videoArgs(c, hasFFmpeg),
		FormatID:  c.f.FormatID,
		Height:    c.height,
		FPS:       c.f.FPS,
		Ext:       c.f.Ext,
		Bytes:     c.bytes,
		SizeKnown: c.known,
	}
}

// videoLabel renders "1080p60  mp4  ~142 MB". The fps suffix appears only above
// 30: at 30 and below it is noise, but collapsing 1080p60 and 1080p30 into one
// unlabelled row would hide a choice a person cares about.
func videoLabel(c candidate) string {
	label := strconv.Itoa(c.height) + "p"
	// The upper bound is not decoration: FPS is whatever the site said, and
	// converting a float64 outside int's range is implementation-defined.
	if c.f.FPS > 30 && c.f.FPS < maxRenderableFPS {
		label += strconv.Itoa(int(math.Round(c.f.FPS)))
	}

	ext := c.f.Ext
	if ext == "" {
		ext = "?"
	}

	size := "~?"
	if c.known {
		size = "~" + formatBytes(c.bytes)
	}
	return label + "  " + ext + "  " + size
}

// videoArgs builds the argv fragment for a video row, concrete id first (D1).
//
// The "+ba" term is there only when the chosen format is video-only and so
// actually needs a merge; asking yt-dlp to merge audio into a format that
// already carries some would give the file two audio tracks. The trailing
// height-bounded term is the last resort for a stale id — a format list can go
// out of date between the probe and the download.
func videoArgs(c candidate, hasFFmpeg bool) []string {
	// The bound is the taller of the snapped and the real height. Snapping 1082
	// down to 1080 is right for the label and for grouping, but as a selector
	// bound it would exclude the very rendition the row is describing, so the
	// fallback would quietly drop a rung on the one occasion it runs.
	bound := max(c.height, c.f.Height)
	fallback := "b[height<=" + strconv.Itoa(bound) + "]"

	id := c.f.FormatID
	if id == "" {
		return []string{"-f", fallback}
	}

	selector := id + "/" + fallback
	if hasFFmpeg && c.merged {
		selector = id + "+ba/" + selector
	}
	return []string{"-f", selector}
}

// audioRow is the one extract-audio line, appended only when ffmpeg is present
// because mp3 is a transcode (D2). It names no format id: the best audio of the
// moment is exactly what it asks for.
func audioRow() Row {
	return Row{
		Key:       "audio-mp3",
		Label:     "audio only  mp3",
		Args:      []string{"-x", "--audio-format", "mp3"},
		AudioOnly: true,
	}
}

// mergeAudioSize estimates the audio track a merged row will carry: the size of
// the best audio-only format, since "+ba" is what the row will ask for. When
// that format states no size, it falls back to a bitrate estimate, and when the
// duration is unknown too it gives up rather than guess low.
func mergeAudioSize(info *VideoInfo) (int64, bool) {
	var best *RawFormat
	for i := range info.Formats {
		f := &info.Formats[i]
		if !isAudioOnlyTrack(*f) {
			continue
		}
		if best == nil || f.ABR > best.ABR || (f.ABR == best.ABR && f.TBR > best.TBR) {
			best = f
		}
	}

	if best != nil {
		if size, ok := rawSize(*best, info.Duration); ok {
			return size, true
		}
	}
	if info.Duration != nil && *info.Duration > 0 {
		return safeBytes(defaultAudioBitrateKbps * 1000 / 8 * *info.Duration)
	}
	return 0, false
}

// rawSize resolves one format's size: the stated size, else the approximate
// one, else bitrate times duration. Duration is nilable because zero and
// unknown have to stay apart here — a nil duration means "no idea", which the
// label spells "~?", not a size of zero.
func rawSize(f RawFormat, duration *float64) (int64, bool) {
	switch {
	case f.Filesize > 0:
		return f.Filesize, true
	case f.FilesizeApprox > 0:
		return f.FilesizeApprox, true
	case f.TBR > 0 && duration != nil && *duration > 0:
		// TBR is in kbit/s, so this is bits per second over 8.
		return safeBytes(f.TBR * 1000 / 8 * *duration)
	}
	return 0, false
}

// safeBytes turns an estimated byte count into an int64, or reports that there
// is no usable number.
//
// Both factors behind the estimate come straight out of the site's JSON, and
// nothing local bounds them. A float64 outside int64's range converts to an
// implementation-defined value — INT64_MIN on amd64 — so an absurd tbr does not
// produce an absurd size, it produces a *negative* one that "known" then
// vouches for, that formatBytes clamps to a plausible-looking "0 B", and that
// D3 ranks first because a negative number clears any percentage. Out of range
// is not a size; it is an unknown, and the label says so.
func safeBytes(v float64) (int64, bool) {
	if math.IsNaN(v) || v <= 0 || v >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(v), true
}

// formatBytes renders a byte count the way a download size is read: decimal
// units, because that is what "MB" means and what the file manager will say.
// One decimal below 10 ("9.4 MB"), none above ("142 MB").
func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10) + " B"
	}

	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	for i, unit := range units {
		v /= 1000
		digits := 0
		// Decide on the decimal by what will be printed, not by what is held:
		// 9.96 renders as "10.0" if asked for one decimal, which is the decimal
		// this format drops. Round first, then choose.
		if math.Round(v*10)/10 < 10 {
			digits = 1
		}
		// Hand the value up a unit while it is still too large to render, and
		// also when rounding would carry it to 1000 — "1000 MB" reads wrong
		// where "1.0 GB" reads right.
		if math.Round(v) >= 1000 && i < len(units)-1 {
			continue
		}
		return strconv.FormatFloat(v, 'f', digits, 64) + " " + unit
	}
	// Unreachable: the last iteration never continues, so it always returns.
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[len(units)-1]
}
