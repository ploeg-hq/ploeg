package cardimage

type palette struct {
	surface, text, muted, border, accent, accentFg, selected, track string
	tones                                                           map[tone]string
	gold                                                            string
	finish                                                          map[string]string
}

var finishColours = map[string]string{
	"foil":     "#9FB4C7",
	"holo":     "#7FD1E8",
	"prism":    "#B48CF2",
	"gilded":   "#D4A73A",
	"infinity": "#E86FB0",
}

// cardPalette is the one palette Ploeg draws its card image in. A card's skin
// and theme belong to the consumer that draws the card; the image Ploeg posts
// on a pull request does not follow them.
var cardPalette = palette{
	surface: "#FFFFFF", text: "#15191C", muted: "#56636A", border: "#D9DFE1",
	accent: "#3D84E8", accentFg: "#2A66D6", selected: "#E6EFFC", track: "#E4E9EB",
	tones: map[tone]string{
		toneNeutral: "#869396", toneReview: "#9A72D9", toneSuccess: "#3C9A63",
		toneDanger: "#E0625A", toneAttention: "#BC8624", toneLive: "#3D84E8",
	},
	gold: "#C9962B", finish: finishColours,
}
