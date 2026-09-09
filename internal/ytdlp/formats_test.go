package ytdlp

import (
	"math"
	"strings"
	"testing"
)

// The fixtures below keep the shapes `yt-dlp -J` really returns for a YouTube
// video: video-only DASH streams that need a merge, two legacy progressive
// formats (18 and 22) that do not, audio-only m4a and webm, and an mhtml
// storyboard that reports a height but no streams.

func secs(d float64) *float64 { return &d }

// audio140 is the m4a audio yt-dlp's "ba" would pick for a merge.
func audio140() RawFormat {
	return RawFormat{
		FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2",
		ABR: 129.482, TBR: 129.482, Filesize: 4_000_000,
	}
}

// audio251 is the opus audio yt-dlp offers alongside it, at a lower ABR here so
// the merge estimate has one obvious winner.
func audio251() RawFormat {
	return RawFormat{
		FormatID: "251", Ext: "webm", VCodec: "none", ACodec: "opus",
		ABR: 122.116, TBR: 122.116, Filesize: 3_700_000,
	}
}

// storyboard is the format that makes "height > 0" insufficient on its own.
func storyboard() RawFormat {
	return RawFormat{
		FormatID: "sb0", Ext: "mhtml", VCodec: "none", ACodec: "none",
		Height: 45, Width: 80,
	}
}

// hlsVariant builds the shape an HLS rendition arrives in: no filesize of any
// kind, and a tbr that is peak bandwidth rather than the average, so its size
// can only ever be estimated. YouTube lists one of these beside every DASH
// format, and many other extractors emit them on their own.
func hlsVariant(id, vcodec string, height int, fps, tbr float64) RawFormat {
	return RawFormat{
		FormatID: id, Ext: "mp4", VCodec: vcodec, ACodec: "none",
		Height: height, Width: height * 16 / 9, FPS: fps, TBR: tbr,
	}
}

// videoOnly builds a DASH video stream: no audio, so its row is a merge.
func videoOnly(id, ext, vcodec string, height int, fps, tbr float64, filesize int64) RawFormat {
	return RawFormat{
		FormatID: id, Ext: ext, VCodec: vcodec, ACodec: "none",
		Height: height, Width: height * 16 / 9, FPS: fps, TBR: tbr, Filesize: filesize,
	}
}

// progressive builds a format that already carries both streams, so it needs no
// ffmpeg and no merge.
func progressive(id, ext, vcodec string, height int, fps, tbr float64, filesize int64) RawFormat {
	return RawFormat{
		FormatID: id, Ext: ext, VCodec: vcodec, ACodec: "mp4a.40.2",
		Height: height, Width: height * 16 / 9, FPS: fps, ABR: 96, TBR: tbr, Filesize: filesize,
	}
}

// gotRow is the part of a Row the tables assert on: what the user reads, what
// runs, and which concrete format id the row promises (D1).
type gotRow struct {
	label    string
	args     string
	formatID string
}

func collect(rows []Row) []gotRow {
	out := make([]gotRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, gotRow{r.Label, strings.Join(r.Args, " "), r.FormatID})
	}
	return out
}

const audioLabel = "audio only  mp3"

var audioLine = gotRow{audioLabel, "-x --audio-format mp3", ""}

func TestRank(t *testing.T) {
	tests := []struct {
		name      string
		info      VideoInfo
		hasFFmpeg bool
		want      []gotRow
	}{
		{
			name:      "empty format list still offers audio",
			info:      VideoInfo{Duration: secs(600), Formats: nil},
			hasFFmpeg: true,
			want:      []gotRow{audioLine},
		},
		{
			name:      "empty format list without ffmpeg offers nothing",
			info:      VideoInfo{Duration: secs(600), Formats: nil},
			hasFFmpeg: false,
			want:      nil,
		},
		{
			name: "no video formats: audio and storyboards only",
			info: VideoInfo{
				Duration: secs(600),
				Formats:  []RawFormat{storyboard(), audio140(), audio251()},
			},
			hasFFmpeg: true,
			want:      []gotRow{audioLine},
		},
		{
			name: "duplicate and near-duplicate heights collapse to one row",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(),
					// 1078 and 1082 are 1080 with a cropped source; 137 is the
					// real thing and wins its group on TBR.
					videoOnly("244", "webm", "vp9", 1078, 29.97, 4300, 95_000_000),
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4400, 96_000_000),
					videoOnly("248", "webm", "vp9", 1082, 29.97, 3000, 90_000_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				// 96 MB of video plus the 4 MB audio it merges with.
				{"1080p  mp4  ~100 MB", "-f 137+ba/137/b[height<=1080]", "137"},
				audioLine,
			},
		},
		{
			name: "six height cap keeps the ladder plus the off-ladder highest",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(),
					// A portrait video: 1920 is on no ladder rung, and is still
					// the row nobody wants dropped.
					videoOnly("401", "mp4", "avc1.640028", 1920, 29.97, 8000, 200_000_000),
					videoOnly("400", "mp4", "avc1.640028", 1440, 29.97, 6000, 150_000_000),
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4400, 96_000_000),
					videoOnly("136", "mp4", "avc1.4d401f", 720, 29.97, 2400, 56_000_000),
					videoOnly("135", "mp4", "avc1.4d401e", 480, 29.97, 1100, 26_000_000),
					videoOnly("134", "mp4", "avc1.4d401e", 360, 29.97, 600, 16_000_000),
					videoOnly("133", "mp4", "avc1.4d4015", 240, 29.97, 250, 6_000_000),
					videoOnly("160", "mp4", "avc1.4d400c", 144, 29.97, 100, 2_000_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				{"1920p  mp4  ~204 MB", "-f 401+ba/401/b[height<=1920]", "401"},
				{"1440p  mp4  ~154 MB", "-f 400+ba/400/b[height<=1440]", "400"},
				{"1080p  mp4  ~100 MB", "-f 137+ba/137/b[height<=1080]", "137"},
				{"720p  mp4  ~60 MB", "-f 136+ba/136/b[height<=720]", "136"},
				{"480p  mp4  ~30 MB", "-f 135+ba/135/b[height<=480]", "135"},
				{"360p  mp4  ~20 MB", "-f 134+ba/134/b[height<=360]", "134"},
				// 240 and 144 lost to the ladder; six video rows plus the audio
				// row is exactly the cap.
				audioLine,
			},
		},
		{
			name: "with no ladder rungs at all the highest six survive",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(),
					videoOnly("f1", "mp4", "avc1.640028", 1900, 29.97, 8000, 200_000_000),
					videoOnly("f2", "mp4", "avc1.640028", 1600, 29.97, 6000, 150_000_000),
					videoOnly("f3", "mp4", "avc1.640028", 1300, 29.97, 4400, 96_000_000),
					videoOnly("f4", "mp4", "avc1.640028", 1000, 29.97, 2400, 56_000_000),
					videoOnly("f5", "mp4", "avc1.640028", 900, 29.97, 1100, 26_000_000),
					videoOnly("f6", "mp4", "avc1.640028", 800, 29.97, 600, 16_000_000),
					videoOnly("f7", "mp4", "avc1.640028", 620, 29.97, 250, 6_000_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				{"1900p  mp4  ~204 MB", "-f f1+ba/f1/b[height<=1900]", "f1"},
				{"1600p  mp4  ~154 MB", "-f f2+ba/f2/b[height<=1600]", "f2"},
				{"1300p  mp4  ~100 MB", "-f f3+ba/f3/b[height<=1300]", "f3"},
				{"1000p  mp4  ~60 MB", "-f f4+ba/f4/b[height<=1000]", "f4"},
				{"900p  mp4  ~30 MB", "-f f5+ba/f5/b[height<=900]", "f5"},
				{"800p  mp4  ~20 MB", "-f f6+ba/f6/b[height<=800]", "f6"},
				audioLine,
			},
		},
		{
			name: "fps is in the label only above 30",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(),
					videoOnly("299", "mp4", "avc1.64002a", 1080, 59.94, 4400, 96_000_000),
					videoOnly("298", "mp4", "avc1.4d4020", 720, 50, 2400, 56_000_000),
					videoOnly("136", "mp4", "avc1.4d401f", 480, 30, 1100, 26_000_000),
					videoOnly("134", "mp4", "avc1.4d401e", 360, 29.97, 600, 16_000_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				{"1080p60  mp4  ~100 MB", "-f 299+ba/299/b[height<=1080]", "299"},
				{"720p50  mp4  ~60 MB", "-f 298+ba/298/b[height<=720]", "298"},
				{"480p  mp4  ~30 MB", "-f 136+ba/136/b[height<=480]", "136"},
				{"360p  mp4  ~20 MB", "-f 134+ba/134/b[height<=360]", "134"},
				audioLine,
			},
		},
		{
			name: "without ffmpeg only progressive formats survive and no audio row",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(), audio251(), storyboard(),
					// Everything DASH needs a merge, so none of it is eligible.
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4400, 96_000_000),
					videoOnly("248", "webm", "vp9", 1080, 29.97, 3000, 60_000_000),
					progressive("22", "mp4", "avc1.64001F", 720, 29.97, 800, 60_000_000),
					progressive("18", "mp4", "avc1.42001E", 360, 29.97, 200, 15_000_000),
				},
			},
			hasFFmpeg: false,
			want: []gotRow{
				{"720p  mp4  ~60 MB", "-f 22/b[height<=720]", "22"},
				{"360p  mp4  ~15 MB", "-f 18/b[height<=360]", "18"},
			},
		},
		{
			name: "progressive format with ffmpeg is not asked to merge a second audio track",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(),
					progressive("18", "mp4", "avc1.42001E", 360, 29.97, 200, 15_000_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				// 15 MB exactly: nothing is merged in, so nothing is added.
				{"360p  mp4  ~15 MB", "-f 18/b[height<=360]", "18"},
				audioLine,
			},
		},
		{
			name: "nil duration with no stated sizes reaches the unknown label",
			info: VideoInfo{
				Duration: nil,
				Formats: []RawFormat{
					{FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 129, TBR: 129},
					// TBR is all there is, and TBR alone estimates nothing.
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4400, 0),
					progressive("18", "mp4", "avc1.42001E", 360, 29.97, 200, 0),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				{"1080p  mp4  ~?", "-f 137+ba/137/b[height<=1080]", "137"},
				{"360p  mp4  ~?", "-f 18/b[height<=360]", "18"},
				audioLine,
			},
		},
		{
			name: "a stated size with a nil duration still renders",
			info: VideoInfo{
				Duration: nil,
				Formats: []RawFormat{
					audio140(),
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4400, 96_000_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				// The audio side is known from its own filesize, not a duration.
				{"1080p  mp4  ~100 MB", "-f 137+ba/137/b[height<=1080]", "137"},
				audioLine,
			},
		},
		{
			name: "size falls through to filesize_approx and then to the bitrate estimate",
			info: VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					// No filesize anywhere: the merge estimate uses 128 kbit/s
					// over 600 s = 9.6 MB.
					{FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 129, TBR: 0},
					{
						FormatID: "137", Ext: "mp4", VCodec: "avc1.640028", ACodec: "none",
						Height: 1080, FPS: 29.97, TBR: 4400, FilesizeApprox: 96_000_000,
					},
					{
						FormatID: "136", Ext: "mp4", VCodec: "avc1.4d401f", ACodec: "none",
						Height: 720, FPS: 29.97, TBR: 2400,
					},
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				// 96 MB approx + 9.6 MB estimated audio.
				{"1080p  mp4  ~106 MB", "-f 137+ba/137/b[height<=1080]", "137"},
				// 2400 kbit/s over 600 s = 180 MB, + 9.6 MB audio.
				{"720p  mp4  ~190 MB", "-f 136+ba/136/b[height<=720]", "136"},
				audioLine,
			},
		},
		{
			name: "a realistic youtube list reduces to seven rows",
			info: VideoInfo{
				Title:    "Rick Astley - Never Gonna Give You Up",
				Duration: secs(212),
				Formats: []RawFormat{
					storyboard(),
					{FormatID: "sb1", Ext: "mhtml", VCodec: "none", ACodec: "none", Height: 90},
					{FormatID: "233", Ext: "mp4", VCodec: "none", ACodec: "none"},
					audio251(), audio140(),
					{FormatID: "139", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.5", ABR: 49.9, TBR: 49.9, Filesize: 1_300_000},
					videoOnly("160", "mp4", "avc1.4d400c", 144, 29.97, 111, 2_900_000),
					videoOnly("278", "webm", "vp09.00.15.08", 144, 29.97, 121, 3_200_000),
					videoOnly("133", "mp4", "avc1.4d400d", 240, 29.97, 251, 6_600_000),
					videoOnly("134", "mp4", "avc1.4d401e", 360, 29.97, 632, 16_700_000),
					videoOnly("243", "webm", "vp09.00.21.08", 360, 29.97, 550, 14_600_000),
					videoOnly("135", "mp4", "avc1.4d401f", 480, 29.97, 1155, 30_600_000),
					videoOnly("136", "mp4", "avc1.4d401f", 720, 29.97, 2412, 63_900_000),
					videoOnly("247", "webm", "vp09.00.31.08", 720, 29.97, 2000, 53_000_000),
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4404, 116_700_000),
					videoOnly("248", "webm", "vp09.00.40.08", 1080, 29.97, 3800, 100_700_000),
					progressive("18", "mp4", "avc1.42001E", 360, 29.97, 613, 16_200_000),
					progressive("22", "mp4", "avc1.64001F", 720, 29.97, 1200, 31_800_000),
				},
			},
			hasFFmpeg: true,
			want: []gotRow{
				// 116.7 MB + 4 MB of audio 140.
				{"1080p  mp4  ~121 MB", "-f 137+ba/137/b[height<=1080]", "137"},
				// 136 outranks progressive 22 on TBR, so the row merges.
				{"720p  mp4  ~68 MB", "-f 136+ba/136/b[height<=720]", "136"},
				{"480p  mp4  ~35 MB", "-f 135+ba/135/b[height<=480]", "135"},
				{"360p  mp4  ~21 MB", "-f 134+ba/134/b[height<=360]", "134"},
				{"240p  mp4  ~11 MB", "-f 133+ba/133/b[height<=240]", "133"},
				// vp9 278 is bigger than avc1 160 here, so nothing unseats avc1.
				{"144p  mp4  ~6.9 MB", "-f 160+ba/160/b[height<=144]", "160"},
				audioLine,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collect(Rank(&tt.info, tt.hasFFmpeg))
			if len(got) > maxRows {
				t.Fatalf("Rank returned %d rows, more than the cap of %d", len(got), maxRows)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Rank returned %d rows, want %d:\ngot  %+v\nwant %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("row %d:\ngot  %+v\nwant %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSizelessRepresentative pins what happens when one candidate in a group
// states its size and the other does not. Neither the bare "prefer the stated
// one" rule nor the bare "prefer the higher tbr" rule is right on its own: the
// first hands the row to a visibly worse rendition, the second hands it to an
// estimate off a bitrate that was never the file's.
func TestSizelessRepresentative(t *testing.T) {
	tests := []struct {
		name     string
		duration *float64
		formats  []RawFormat
		want     gotRow
	}{
		{
			name:     "the same rendition listed twice goes to the copy that states its size",
			duration: secs(213),
			formats: []RawFormat{
				// 96 is 137's HLS twin: 6% more tbr, and that tbr is a peak.
				hlsVariant("96", "avc1.640028", 1080, 25, 4688),
				videoOnly("137", "mp4", "avc1.640028", 1080, 25, 4404, 116_700_000),
			},
			want: gotRow{"1080p  mp4  ~121 MB", "-f 137+ba/137/b[height<=1080]", "137"},
		},
		{
			name:     "a genuinely better sizeless rendition still wins",
			duration: secs(200),
			formats: []RawFormat{
				// 4x the bitrate is not peak-versus-average, it is a different
				// stream, and 1.5 Mbps is not what 1080p should mean here.
				videoOnly("mp4-low", "mp4", "avc1.640028", 1080, 25, 1500, 112_500_000),
				hlsVariant("hls-hi", "avc1.640028", 1080, 25, 6000),
			},
			want: gotRow{"1080p  mp4  ~154 MB", "-f hls-hi+ba/hls-hi/b[height<=1080]", "hls-hi"},
		},
		{
			name:     "a stated format with no bitrate at all is not traded away",
			duration: secs(200),
			formats: []RawFormat{
				// Nothing to compare against, so the real filesize stands.
				{
					FormatID: "http-1080", Ext: "mp4", VCodec: "avc1.640028", ACodec: "none",
					Height: 1080, FPS: 25, Filesize: 112_500_000,
				},
				hlsVariant("hls-hi", "avc1.640028", 1080, 25, 6000),
			},
			want: gotRow{"1080p  mp4  ~116 MB", "-f http-1080+ba/http-1080/b[height<=1080]", "http-1080"},
		},
		{
			name:     "an all-HLS group still produces a row, estimated and labelled as such",
			duration: secs(200),
			formats: []RawFormat{
				hlsVariant("hls-a", "avc1.640028", 1080, 25, 6000),
				hlsVariant("hls-b", "avc1.640028", 1080, 25, 4000),
			},
			want: gotRow{"1080p  mp4  ~154 MB", "-f hls-a+ba/hls-a/b[height<=1080]", "hls-a"},
		},
		{
			name:     "identical candidates fall back to the lower format id",
			duration: secs(213),
			formats: []RawFormat{
				videoOnly("298", "mp4", "avc1.640028", 1080, 25, 4404, 116_700_000),
				videoOnly("137", "mp4", "avc1.640028", 1080, 25, 4404, 116_700_000),
			},
			want: gotRow{"1080p  mp4  ~121 MB", "-f 137+ba/137/b[height<=1080]", "137"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := VideoInfo{
				Duration: tt.duration,
				Formats:  append([]RawFormat{audio140()}, tt.formats...),
			}
			rows := Rank(&info, true)
			if len(rows) != 2 {
				t.Fatalf("got %d rows, want a video row and an audio row: %+v", len(rows), collect(rows))
			}
			if got := collect(rows)[0]; got != tt.want {
				t.Errorf("\ngot  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// TestCodecThresholdNeedsComparableSizes covers D3 against sizes of different
// provenance: a real filesize on one side and a peak-bitrate estimate on the
// other are not two measurements of the same thing, and a 25% gap between them
// is as likely to be the provenance as the codec.
func TestCodecThresholdNeedsComparableSizes(t *testing.T) {
	info := VideoInfo{
		Duration: secs(200),
		Formats: []RawFormat{
			audio140(),
			// Estimates to 150 MB off a peak tbr; the real average is anyone's
			// guess and quite possibly under 100 MB.
			hlsVariant("270", "avc1.640028", 1080, 25, 6000),
			// A real 100 MB. Displayed, that is 104 MB against 154 MB — a 32%
			// gap on paper, which is not evidence about the codec.
			videoOnly("248", "webm", "vp09.00.40.08", 1080, 25, 4000, 100_000_000),
		},
	}

	rows := Rank(&info, true)
	if rows[0].FormatID != "270" {
		t.Errorf("chose format %q, want avc1 270: a stated size and an estimate are not comparable", rows[0].FormatID)
	}
}

// TestAbsentCodecFieldReadings covers the two questions an empty codec field
// gets asked. Unknown reads as present when deciding whether a format needs a
// merge, and as no evidence at all when nominating the track to merge in.
func TestAbsentCodecFieldReadings(t *testing.T) {
	t.Run("an absent acodec on a video format is not a merge", func(t *testing.T) {
		// The extractor omitted both codec fields, which is what archive.org
		// does for its progressive files.
		info := VideoInfo{
			Duration: secs(600),
			Formats: []RawFormat{
				audio140(),
				{FormatID: "v1080", Ext: "mp4", VCodec: "h264", ACodec: "", Height: 1080, TBR: 4400, Filesize: 96_000_000},
			},
		}
		for _, hasFFmpeg := range []bool{true, false} {
			rows := Rank(&info, hasFFmpeg)
			if len(rows) == 0 {
				t.Fatalf("hasFFmpeg=%v: the format was dropped; unknown must read as present", hasFFmpeg)
			}
			want := gotRow{"1080p  mp4  ~96 MB", "-f v1080/b[height<=1080]", "v1080"}
			if got := collect(rows)[0]; got != want {
				t.Errorf("hasFFmpeg=%v:\ngot  %+v\nwant %+v", hasFFmpeg, got, want)
			}
		}
	})

	t.Run("an absent acodec on an audio format is not a merge source", func(t *testing.T) {
		info := VideoInfo{
			Duration: secs(600),
			Formats: []RawFormat{
				// Nothing here says there is audio in it, so its 50 MB must not
				// be added to a merged row as though there were.
				{FormatID: "mystery", Ext: "bin", VCodec: "none", ACodec: "", ABR: 320, Filesize: 50_000_000},
				videoOnly("137", "mp4", "avc1.640028", 1080, 25, 4400, 96_000_000),
			},
		}
		rows := Rank(&info, true)
		// 96 MB plus the 128 kbit/s fallback over 600 s, not 96 + 50.
		const want = 96_000_000 + int64(defaultAudioBitrateKbps*1000/8*600)
		if rows[0].Bytes != want {
			t.Errorf("Bytes = %d, want %d (a format with no stated codec is not audio)", rows[0].Bytes, want)
		}
	})
}

// TestSizeEstimateCannotOverflow covers the two float-to-int conversions that
// run on numbers the site chose. Out of range is an unknown, not a number.
func TestSizeEstimateCannotOverflow(t *testing.T) {
	t.Run("an absurd tbr produces no size rather than a negative one", func(t *testing.T) {
		info := VideoInfo{
			Duration: secs(1e9),
			Formats: []RawFormat{
				audio140(),
				hlsVariant("avc", "avc1.640028", 1080, 25, 1e18),
				hlsVariant("vp9", "vp09.00.40.08", 1080, 25, 1e18),
			},
		}
		rows := Rank(&info, true)
		got := collect(rows)[0]
		want := gotRow{"1080p  mp4  ~?", "-f avc+ba/avc/b[height<=1080]", "avc"}
		if got != want {
			t.Errorf("\ngot  %+v\nwant %+v", got, want)
		}
		if rows[0].SizeKnown || rows[0].Bytes != 0 {
			t.Errorf("SizeKnown=%v Bytes=%d, want an unknown size", rows[0].SizeKnown, rows[0].Bytes)
		}
	})

	// The merged path above is also guarded by its own overflow check, and what
	// an out-of-range conversion produces is implementation-defined — INT64_MIN
	// on amd64, saturation to INT64_MAX on arm64 — so it can land on either
	// side of that check depending on the machine. A progressive format reaches
	// the conversion with nothing else in the way, on every platform.
	t.Run("an absurd tbr on a format that needs no merge is still no size", func(t *testing.T) {
		info := VideoInfo{
			Duration: secs(1e9),
			Formats: []RawFormat{
				{
					FormatID: "18", Ext: "mp4", VCodec: "avc1.42001E", ACodec: "mp4a.40.2",
					Height: 360, FPS: 25, TBR: 1e18,
				},
			},
		}
		rows := Rank(&info, false)
		want := gotRow{"360p  mp4  ~?", "-f 18/b[height<=360]", "18"}
		if got := collect(rows)[0]; got != want {
			t.Errorf("\ngot  %+v\nwant %+v", got, want)
		}
	})

	t.Run("an absurd fps stays out of the label", func(t *testing.T) {
		info := VideoInfo{
			Duration: secs(600),
			Formats: []RawFormat{
				audio140(),
				videoOnly("137", "mp4", "avc1.640028", 1080, 1e300, 4400, 96_000_000),
				videoOnly("136", "mp4", "avc1.4d401f", 720, 59.94, 2400, 56_000_000),
			},
		}
		rows := Rank(&info, true)
		if got, want := rows[0].Label, "1080p  mp4  ~100 MB"; got != want {
			t.Errorf("label = %q, want %q", got, want)
		}
		// The suffix itself still works; it is only the absurd value that goes.
		if got, want := rows[1].Label, "720p60  mp4  ~60 MB"; got != want {
			t.Errorf("label = %q, want %q", got, want)
		}
	})
}

// TestFallbackBoundKeepsTheRenditionReachable covers the last-resort selector
// term for a height that was snapped down: 1082 grouped and labelled as 1080
// must still be inside the bound the fallback asks for.
func TestFallbackBoundKeepsTheRenditionReachable(t *testing.T) {
	info := VideoInfo{
		Duration: secs(600),
		Formats: []RawFormat{
			audio140(),
			videoOnly("137", "mp4", "avc1.640028", 1082, 25, 4400, 96_000_000),
		},
	}
	want := gotRow{"1080p  mp4  ~100 MB", "-f 137+ba/137/b[height<=1082]", "137"}
	if got := collect(Rank(&info, true))[0]; got != want {
		t.Errorf("\ngot  %+v\nwant %+v", got, want)
	}
}

func TestRankNilInfo(t *testing.T) {
	if rows := Rank(nil, true); rows != nil {
		t.Errorf("Rank(nil, true) = %+v, want nil", rows)
	}
}

func TestRankRowsAreSortedDescendingAndKeyed(t *testing.T) {
	info := VideoInfo{
		Duration: secs(212),
		Formats: []RawFormat{
			audio140(),
			videoOnly("134", "mp4", "avc1.4d401e", 360, 29.97, 632, 16_700_000),
			videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4404, 116_700_000),
			videoOnly("136", "mp4", "avc1.4d401f", 720, 29.97, 2412, 63_900_000),
		},
	}

	rows := Rank(&info, true)
	seen := map[string]bool{}
	prev := 1 << 30
	for _, r := range rows {
		if seen[r.Key] {
			t.Errorf("duplicate row key %q", r.Key)
		}
		seen[r.Key] = true
		if r.AudioOnly {
			continue
		}
		if r.Height >= prev {
			t.Errorf("row %q height %d is not below the previous %d", r.Key, r.Height, prev)
		}
		prev = r.Height
	}
	if last := rows[len(rows)-1]; !last.AudioOnly {
		t.Errorf("last row is %q, want the audio row", last.Key)
	}
}

// TestCodecThreshold pins D3's number from both sides. Every candidate here is
// video-only, so the sizes being compared are the displayed ones: the video
// stream plus the 4 MB of audio 140 that the merge will add.
func TestCodecThreshold(t *testing.T) {
	const avc1Filesize = 96_000_000 // 100 MB displayed
	tests := []struct {
		name         string
		vp9Filesize  int64
		wantFormatID string
		wantLabel    string
	}{
		{
			name:         "vp9 is 24% smaller, just under the threshold: avc1 keeps the row",
			vp9Filesize:  72_000_000, // 76 MB displayed = 76% of 100 MB
			wantFormatID: "137",
			wantLabel:    "1080p  mp4  ~100 MB",
		},
		{
			name:         "vp9 is exactly 25% smaller: at least 25% means vp9 takes it",
			vp9Filesize:  71_000_000, // 75 MB displayed
			wantFormatID: "248",
			wantLabel:    "1080p  webm  ~75 MB",
		},
		{
			name:         "vp9 is 26% smaller, just over the threshold: vp9 takes the row",
			vp9Filesize:  70_000_000, // 74 MB displayed
			wantFormatID: "248",
			wantLabel:    "1080p  webm  ~74 MB",
		},
		{
			name:         "vp9 is larger: avc1 keeps the row",
			vp9Filesize:  99_000_000,
			wantFormatID: "137",
			wantLabel:    "1080p  mp4  ~100 MB",
		},
	}

	if modernCodecSizeAdvantage != 0.25 {
		t.Fatalf("modernCodecSizeAdvantage = %v, this table is written against 0.25", modernCodecSizeAdvantage)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := VideoInfo{
				Duration: secs(600),
				Formats: []RawFormat{
					audio140(),
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4404, avc1Filesize),
					// The vp9 stream has the lower TBR, as it does in real
					// lists: size, not bitrate, is what may unseat avc1.
					videoOnly("248", "webm", "vp09.00.40.08", 1080, 29.97, 3800, tt.vp9Filesize),
				},
			}
			rows := Rank(&info, true)
			if len(rows) != 2 {
				t.Fatalf("got %d rows, want a video row and an audio row: %+v", len(rows), collect(rows))
			}
			if rows[0].FormatID != tt.wantFormatID {
				t.Errorf("chose format %q, want %q", rows[0].FormatID, tt.wantFormatID)
			}
			if rows[0].Label != tt.wantLabel {
				t.Errorf("label = %q, want %q", rows[0].Label, tt.wantLabel)
			}
			// D1: the row the user reads must name the format it chose.
			wantArg := tt.wantFormatID + "+ba/" + tt.wantFormatID + "/b[height<=1080]"
			if got := strings.Join(rows[0].Args, " "); got != "-f "+wantArg {
				t.Errorf("args = %q, want %q", got, "-f "+wantArg)
			}
		})
	}
}

// TestCodecThresholdNeedsBothSizes covers the case the threshold cannot decide:
// an unknown size is not evidence of being smaller, so avc1 keeps the row.
func TestCodecThresholdNeedsBothSizes(t *testing.T) {
	info := VideoInfo{
		Duration: nil,
		Formats: []RawFormat{
			audio140(),
			videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4404, 96_000_000),
			// No filesize and no duration to estimate from.
			videoOnly("248", "webm", "vp09.00.40.08", 1080, 29.97, 3800, 0),
		},
	}
	rows := Rank(&info, true)
	if rows[0].FormatID != "137" {
		t.Errorf("chose format %q, want avc1 137 when the vp9 size is unknown", rows[0].FormatID)
	}
}

// TestMergedRowSizeIncludesAudio is the arithmetic D1 rests on: the number in
// the label is the file that lands, not the video stream on its own.
func TestMergedRowSizeIncludesAudio(t *testing.T) {
	const videoBytes = 116_700_000

	tests := []struct {
		name      string
		formats   []RawFormat
		duration  *float64
		wantBytes int64
		wantKnown bool
	}{
		{
			name:      "audio size comes from the best audio format",
			formats:   []RawFormat{audio140(), audio251()},
			duration:  secs(212),
			wantBytes: videoBytes + 4_000_000,
			wantKnown: true,
		},
		{
			name: "audio size falls back to a bitrate estimate",
			formats: []RawFormat{
				{FormatID: "140", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2", ABR: 129},
			},
			duration: secs(212),
			// 128 kbit/s over 212 s.
			wantBytes: videoBytes + int64(defaultAudioBitrateKbps*1000/8*212),
			wantKnown: true,
		},
		{
			name:      "no audio format and no duration: the row refuses to understate",
			formats:   nil,
			duration:  nil,
			wantKnown: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := VideoInfo{
				Duration: tt.duration,
				Formats: append(append([]RawFormat{}, tt.formats...),
					videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4404, videoBytes)),
			}
			rows := Rank(&info, true)
			if rows[0].SizeKnown != tt.wantKnown {
				t.Fatalf("SizeKnown = %v, want %v (label %q)", rows[0].SizeKnown, tt.wantKnown, rows[0].Label)
			}
			if !tt.wantKnown {
				return
			}
			if rows[0].Bytes != tt.wantBytes {
				t.Errorf("Bytes = %d, want %d (video alone is %d)", rows[0].Bytes, tt.wantBytes, videoBytes)
			}
			if rows[0].Bytes <= videoBytes {
				t.Errorf("Bytes = %d does not account for the merged audio track", rows[0].Bytes)
			}
		})
	}
}

// TestSafeBytes states the rule the two end-to-end overflow cases rely on,
// without depending on what a given architecture does with an out-of-range
// conversion.
func TestSafeBytes(t *testing.T) {
	tests := []struct {
		in        float64
		wantBytes int64
		wantOK    bool
	}{
		{1_000_000, 1_000_000, true},
		{9.4, 9, true},
		{0, 0, false},
		{-1, 0, false},
		{math.NaN(), 0, false},
		{math.Inf(1), 0, false},
		{math.Inf(-1), 0, false},
		// 1e18 kbit/s over 1e9 seconds, the shape a hostile -J can produce.
		{1.25e29, 0, false},
		{float64(math.MaxInt64), 0, false},
	}
	for _, tt := range tests {
		gotBytes, gotOK := safeBytes(tt.in)
		if gotBytes != tt.wantBytes || gotOK != tt.wantOK {
			t.Errorf("safeBytes(%g) = %d, %v; want %d, %v", tt.in, gotBytes, gotOK, tt.wantBytes, tt.wantOK)
		}
	}
}

func TestNormaliseHeight(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{1080, 1080},
		{1078, 1080},
		{1082, 1080},
		{1088, 1080},
		{720, 720},
		{718, 720},
		{406, 406}, // 360 and 480 are both further off than the tolerance
		{486, 480},
		{362, 360},
		{2160, 2160},
		{2158, 2160},
		// A portrait video's height is nowhere near a rung and keeps itself.
		{1920, 1920},
		{640, 640},
	}
	for _, tt := range tests {
		if got := normaliseHeight(tt.in); got != tt.want {
			t.Errorf("normaliseHeight(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{-1, "0 B"},
		{0, "0 B"},
		{1, "1 B"},
		{999, "999 B"},
		{1000, "1.0 KB"},
		{1500, "1.5 KB"},
		{9900, "9.9 KB"},
		{9949, "9.9 KB"},
		// The decimal has to be chosen from the rounded value: at one decimal
		// these would print "10.0 KB", which is the decimal this format drops.
		{9950, "10 KB"},
		{9960, "10 KB"},
		{9999, "10 KB"},
		{10_000, "10 KB"},
		{999_400, "999 KB"},
		// Rounding must carry into the next unit rather than print "1000 KB".
		{999_949, "1.0 MB"},
		{1_000_000, "1.0 MB"},
		{7_200_000, "7.2 MB"},
		{142_000_000, "142 MB"},
		{999_949_000, "1.0 GB"},
		{1_500_000_000, "1.5 GB"},
		{1_000_000_000_000, "1.0 TB"},
		// The largest unit has to absorb whatever is left.
		{9_223_372_036_854_775_807, "9223 PB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.in); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClassifyCodec(t *testing.T) {
	tests := []struct {
		vcodec string
		want   codecClass
	}{
		{"avc1.640028", codecLegacy},
		{"AVC1.640028", codecLegacy},
		{"h264", codecLegacy},
		{"avc3.640028", codecLegacy},
		{"vp9", codecModern},
		{"vp09.00.40.08", codecModern},
		{"av01.0.05M.08", codecModern},
		{"av1", codecModern},
		{"", codecOther},
		{"theora", codecOther},
		{"none", codecOther},
	}
	for _, tt := range tests {
		if got := classifyCodec(tt.vcodec); got != tt.want {
			t.Errorf("classifyCodec(%q) = %v, want %v", tt.vcodec, got, tt.want)
		}
	}
}

// TestArgsCarryNoQuotes guards D4: these strings go straight into an argv, so a
// quote character in one of them would be a literal quote in the selector.
func TestArgsCarryNoQuotes(t *testing.T) {
	info := VideoInfo{
		Duration: secs(212),
		Formats: []RawFormat{
			audio140(),
			videoOnly("137", "mp4", "avc1.640028", 1080, 29.97, 4404, 116_700_000),
			progressive("18", "mp4", "avc1.42001E", 360, 29.97, 613, 16_200_000),
		},
	}
	for _, hasFFmpeg := range []bool{true, false} {
		for _, r := range Rank(&info, hasFFmpeg) {
			for _, a := range r.Args {
				if strings.ContainsAny(a, `"'`) {
					t.Errorf("row %q arg %q contains a quote character", r.Key, a)
				}
			}
		}
	}
}
