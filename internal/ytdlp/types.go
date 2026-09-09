package ytdlp

// VideoInfo is the subset of `yt-dlp -J` output that yank uses.
//
// Fields yt-dlp may return as null decode to their zero value, which the
// callers already treat as "unknown" — except Duration, where zero and unknown
// have to stay distinguishable because size estimates divide by it.
type VideoInfo struct {
	Title        string      `json:"title"`
	Uploader     string      `json:"uploader"`
	Duration     *float64    `json:"duration"`
	WebpageURL   string      `json:"webpage_url"`
	ExtractorKey string      `json:"extractor_key"`
	IsLive       bool        `json:"is_live"`
	Formats      []RawFormat `json:"formats"`
}

// RawFormat is one entry of VideoInfo.Formats, before ranking reduces the
// hundred-odd of them to the handful a person picks from.
type RawFormat struct {
	FormatID       string  `json:"format_id"`
	Ext            string  `json:"ext"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	Height         int     `json:"height"`
	Width          int     `json:"width"`
	FPS            float64 `json:"fps"`
	ABR            float64 `json:"abr"`
	TBR            float64 `json:"tbr"`
	Filesize       int64   `json:"filesize"`
	FilesizeApprox int64   `json:"filesize_approx"`
	// Protocol is yt-dlp's own name for how the stream is fetched — "https",
	// "http_dash_segments", "m3u8_native" and the like. Ranking reads it to
	// tell an m3u8 variant, whose tbr is the manifest's advertised peak, from a
	// direct stream, whose tbr is the bitrate it was encoded at.
	Protocol string `json:"protocol"`
}
