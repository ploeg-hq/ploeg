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

var palettes = map[string]palette{
	"vloer-native": {
		surface: "#FFFFFF", text: "#15191C", muted: "#56636A", border: "#D9DFE1",
		accent: "#3D84E8", accentFg: "#2A66D6", selected: "#E6EFFC", track: "#E4E9EB",
		tones: map[tone]string{
			toneNeutral: "#869396", toneReview: "#9A72D9", toneSuccess: "#3C9A63",
			toneDanger: "#E0625A", toneAttention: "#BC8624", toneLive: "#3D84E8",
		},
		gold: "#C9962B", finish: finishColours,
	},
	"forge": {
		surface: "#1A1F23", text: "#F3F5F4", muted: "#B4C1C3", border: "#3A4247",
		accent: "#6FA0F0", accentFg: "#9CBEF5", selected: "#26313B", track: "#2C3439",
		tones: map[tone]string{
			toneNeutral: "#8B9A9A", toneReview: "#A98BE0", toneSuccess: "#5DB27F",
			toneDanger: "#E77A70", toneAttention: "#D2A243", toneLive: "#6FA0F0",
		},
		gold: "#E0B54A", finish: finishColours,
	},
}

func paletteFor(skin string) palette {
	if p, ok := palettes[skin]; ok {
		return p
	}
	return palettes["vloer-native"]
}
