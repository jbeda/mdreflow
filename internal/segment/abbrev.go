package segment

// defaultAbbreviations is the built-in exception list for the sentence
// segmenter: tokens ending in '.' that are known abbreviations rather than
// sentence-terminal punctuation. Matching is case-insensitive (see
// newAbbrevSet), so "Mr." and "mr." are equivalent for lookup purposes even
// though the list is written in its conventional case.
var defaultAbbreviations = []string{
	"e.g.",
	"i.e.",
	"etc.",
	"Mr.",
	"Mrs.",
	"Ms.",
	"Dr.",
	"St.",
	"vs.",
	"approx.",
	"dept.",
	"est.",
	"Fig.",
	"No.",
	"Jr.",
	"Sr.",
	"Prof.",
	"Inc.",
	"Ltd.",
	"Co.",
	"Corp.",
	"U.S.",
	"a.m.",
	"p.m.",
	"vol.",
	"pp.",
	"al.", // "et al."
	"cf.",
}

// DefaultAbbreviations returns a copy of the built-in abbreviation list.
func DefaultAbbreviations() []string {
	out := make([]string, len(defaultAbbreviations))
	copy(out, defaultAbbreviations)
	return out
}
