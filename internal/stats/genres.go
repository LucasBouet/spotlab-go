package stats

import "strings"

// genres is the curated genre/subgenre vocabulary, ported verbatim from
// GENRES in genres.ts. A Last.fm tag is only accepted if it matches one of
// these entries exactly after normalize() — the display casing here is
// exactly what ends up shown in the stats. The metal/hardcore families are
// intentionally deep so subgenres like metalcore/deathcore survive instead
// of collapsing to "Rock"/"Metal".
var genres = []string{
	// Metal
	"Metal", "Heavy Metal", "Thrash Metal", "Death Metal", "Melodic Death Metal",
	"Technical Death Metal", "Brutal Death Metal", "Blackened Death Metal",
	"Black Metal", "Atmospheric Black Metal", "Doom Metal", "Power Metal",
	"Progressive Metal", "Nu Metal", "Groove Metal", "Sludge Metal", "Post-Metal",
	"Symphonic Metal", "Folk Metal", "Industrial Metal", "Gothic Metal",
	"Speed Metal", "Metalcore", "Melodic Metalcore", "Deathcore",
	"Symphonic Deathcore", "Mathcore", "Grindcore", "Deathgrind", "Djent", "Slam",
	// Hardcore / punk
	"Hardcore", "Hardcore Punk", "Post-Hardcore", "Beatdown", "Punk", "Punk Rock",
	"Pop Punk", "Skate Punk", "Emo", "Screamo", "Ska",
	// Rock
	"Rock", "Hard Rock", "Classic Rock", "Alternative Rock", "Indie Rock",
	"Progressive Rock", "Psychedelic Rock", "Post-Rock", "Garage Rock", "Grunge",
	"Shoegaze", "Math Rock", "Stoner Rock",
	// Alternative / indie
	"Alternative", "Indie", "Indie Pop",
	// Pop
	"Pop", "Synth-Pop", "Electropop", "Dance-Pop", "K-Pop", "Hyperpop", "Art Pop",
	// Electronic
	"Electronic", "EDM", "House", "Deep House", "Tech House", "Techno", "Trance",
	"Dubstep", "Drum & Bass", "Ambient", "IDM", "Synthwave", "Trap", "Future Bass",
	"Electro", "Downtempo", "Breakbeat", "Hardstyle",
	// Hip-hop
	"Hip-Hop", "Boom Bap", "Cloud Rap", "Drill", "Grime", "Lo-fi Hip-Hop",
	// R&B / soul / funk
	"R&B", "Soul", "Neo-Soul", "Funk", "Motown",
	// Jazz / blues
	"Jazz", "Blues", "Swing",
	// Folk / country
	"Folk", "Indie Folk", "Country", "Americana", "Bluegrass", "Singer-Songwriter",
	// Other
	"Classical", "Reggae", "Dancehall", "Latin", "Reggaeton", "Afrobeats",
	"Gospel", "Soundtrack", "Instrumental", "Acoustic",
}

// broadGenres are umbrella genres. When a track's tags include both a broad
// genre and a more specific one within the top results, the specific one
// wins — that's what keeps "Deathcore" from being flattened to "Metal".
var broadGenres = []string{
	"Metal", "Rock", "Pop", "Hip-Hop", "Electronic", "Alternative", "Indie",
	"Punk", "Folk", "Jazz", "Classical", "Country", "R&B", "Reggae", "Blues",
	"Soul", "Funk", "Hardcore",
}

// aliases are common Last.fm spellings that don't normalize to a canonical
// entry above.
var aliases = map[string]string{
	"rap":              "Hip-Hop",
	"hip hop":          "Hip-Hop",
	"rnb":              "R&B",
	"r and b":          "R&B",
	"dnb":              "Drum & Bass",
	"drum n bass":      "Drum & Bass",
	"drum and bass":    "Drum & Bass",
	"lofi":             "Lo-fi Hip-Hop",
	"lo fi":            "Lo-fi Hip-Hop",
	"synthpop":         "Synth-Pop",
	"melodic hardcore": "Post-Hardcore",
	"nu-metal":         "Nu Metal",
}

func normalizeGenre(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-'
	})
	return strings.Join(fields, " ")
}

var (
	genreLookup map[string]string
	broadSet    map[string]bool
)

func init() {
	genreLookup = make(map[string]string, len(genres)+len(aliases))
	for _, g := range genres {
		genreLookup[normalizeGenre(g)] = g
	}
	for alias, canonical := range aliases {
		genreLookup[normalizeGenre(alias)] = canonical
	}
	broadSet = make(map[string]bool, len(broadGenres))
	for _, g := range broadGenres {
		broadSet[normalizeGenre(g)] = true
	}
}

// matchGenreTag returns the canonical genre label for a single tag, or ""
// if the tag isn't a recognized genre (the common case for junk tags like
// "seen live", "favorites", "00s").
func matchGenreTag(tag string) string {
	if tag == "" {
		return ""
	}
	return genreLookup[normalizeGenre(tag)]
}

// topTagsConsidered caps how many of the (popularity-ordered) tags are
// examined — matches TOP_TAGS_CONSIDERED in genres.ts.
const topTagsConsidered = 10

// pickGenre picks the best genre from a popularity-ordered list of tag
// names: the first recognized *specific* genre within the top tags, else
// the most popular broad genre among them, else "". Direct port of
// pickGenre in genres.ts.
func pickGenre(tagNames []string) string {
	var broadFallback string
	limit := len(tagNames)
	if limit > topTagsConsidered {
		limit = topTagsConsidered
	}
	for i := 0; i < limit; i++ {
		matched := matchGenreTag(tagNames[i])
		if matched == "" {
			continue
		}
		if broadSet[normalizeGenre(matched)] {
			if broadFallback == "" {
				broadFallback = matched
			}
			continue
		}
		return matched
	}
	return broadFallback
}
