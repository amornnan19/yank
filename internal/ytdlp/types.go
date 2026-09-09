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
}
