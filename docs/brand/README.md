# Ploeg — merkidentiteit

Status: v1 · 2026-08-27 · alle bronbestanden in deze map

Een doorbladerbare versie van dit document staat in [merkgids.html](merkgids.html) — met de assets inline, een naad-vergelijking, de acht fontkandidaten naast elkaar en een omkeerbare achtergrond. Open hem lokaal; hij heeft alleen Google Fonts nodig en valt zonder netwerk terug op de systeemstack.

*Ploeg* is Nederlands voor een werkploeg én voor het werktuig dat een voor door
onbewerkte grond trekt. Het merk kiest het werktuig, omdat dat het enige beeld is dat
beide betekenissen tegelijk draagt: een ploeg is een instrument dat door een ploeg
mensen bediend wordt.

---

## 1. De mark

Een steel die bovenaan wegbuigt. Half hoofdletter **P**, half scharblad. De steel draagt,
het blad snijdt — en dat onderscheid is wat de tweekleur codeert.

De vorm is opzettelijk **open**: hij sluit niet terug naar de steel zoals een echte P.
Daarmee blijft hij een werktuig en wordt hij nooit helemaal een letter.

### Constructie

Alles is afgeleid van één maat: de streekbreedte **w = 13** op een canvas van 64 × 64.

| | Waarde | Afgeleid van |
|---|---|---|
| Streekbreedte `w` | 13 | de maat |
| Steel, hartlijn x | 17.5 | randen op 11 en 24 |
| Bovenkant, hartlijn y | 14.5 | randen op 8 en 21 |
| Onderkant, hartlijn y | 49.5 | rand op 56 |
| Bochtstraal (hartlijn) | 17 | ≈ 1.3 × w |
| Einde blad | 46.5, 31.5 | rand op 53 |
| Omhullende | 11, 8 → 53, 56 | 42 × 48, midden op (32, 32) |

```
silhouet  M17.5 49.5V14.5H29.5A17 17 0 0 1 46.5 31.5
steel     M11 21H24V49.5A6.5 6.5 0 0 1 11 49.5Z
knip      clipPath op het silhouet, evenodd: M-8 -8H72V72H-8Z M10 22H25V57H10Z
```

Het silhouet is één `stroke` met `stroke-width="13"`, `stroke-linecap="round"`,
`stroke-linejoin="round"` en **expliciet `fill="none"`**. De steel is een gevuld
vlak dat er in inkt overheen ligt.

De **knip** hoort bij die bouw. De Klei die onder de steel doorloopt schemert
bij het renderen door elke rand van de steel heen — anti-aliasing mengt
randpixels met wat eronder ligt, en dat geeft een oranje zweem langs de steel
en zijn onderkap. Daarom knipt een `clipPath` het verborgen deel van het
silhouet weg, op een strook van één eenheid onder de naad na, die de naad zelf
blijft afdekken. De favicon- en tegelvarianten dragen dezelfde knip op hun
eigen maten (favicon: x 7–23, y 19–61 · tegel: x 13–26, y 24–53). Wie de mark
opnieuw opbouwt zonder knip, bouwt de zweem opnieuw in.

Vier besluiten die niet toevallig zijn:

- **Eén silhouet, kleur erover.** De voor de hand liggende bouw — twee losse
  streken die op hetzelfde punt beginnen — is fout. Twee ronde kappen op
  hetzelfde beginpunt geven een *gebogen* kleurnaad: links zwiept het accent in
  een boog naar beneden tot (11, 14.5), rechts eindigt het vlak op y=21. Dat
  leest als een pil die op de steel ligt, niet als één werktuig. Door het hele
  silhouet in accent te trekken en de steel er als vlak overheen te leggen, is
  de naad een **rechte lijn op y=21**, van x=11 tot x=24.
- **Halve hartlijnen, hele randen.** Bij een oneven streekbreedte vallen de
  *randen* op hele pixels als de hartlijn op een halve valt. Dat is wat scherp
  rendert, niet de hartlijn.
- **Verhouding 42 × 48 (0.875).** Optisch vierkant zou te breed staan; dit is de
  verhouding van een kapitaal.
- **Binnenstraal 10.5** (bochtstraal 17 min halve streek). Ruim genoeg dat de
  tegenvorm in de bocht niet dichtslibt op 16 px. Daarom is de bocht zo wijd.

`fill="none"` staat expliciet op elk `stroke`-pad en niet alleen op het
`<svg>`-element. Wie een pad los kopieert — in een component, een sprite, een
icon-set — krijgt anders een zwart gevuld vlak.

### Eén doorlopend pad

Voor stempels, borduur, print en single-color badges is het silhouet op zichzelf
de complete mark, zonder naad:

```
M17.5 49.5V14.5H29.5A17 17 0 0 1 46.5 31.5
```

### Favicon

Op 16 px is de mark uit `mark.svg` te ruim bemeten en de streek te zwaar.
`favicon.svg` is apart geconstrueerd: voller kader (omhullende 8, 4 → 57, 60),
en de streek is naar verhouding *lichter* gezet (14 op 56 = 25 % tegen 27 % in
de master). Zware streken lopen dicht op klein formaat; dat compenseer je door
ze relatief dunner te maken, niet dikker. Naad op y=18.

---

## 2. Kleur

Zes waarden. De namen komen uit het domein.

| Naam | Hex | Rol |
|---|---|---|
| **Voor** | `#141A1D` | inkt — de dragende vorm, tekst op lichte grond |
| **Klei** | `#E4572E` | accent — het snijdende deel |
| **Klei Diep** | `#C0431F` | accent voor kleine tekst op lichte grond |
| **Kalk** | `#F4F6F2` | papier |
| **Nacht** | `#111619` | donkere grond |
| **Stoppel** | `#5E6B66` | gedempte tekst op licht |
| **Stoppel Licht** | `#8C9A94` | gedempte tekst op donker |

### Gemeten contrast (WCAG 2.1)

| | op Kalk | op Nacht |
|---|---|---|
| Voor | 16.16:1 · AAA | — (gebruik Kalk) |
| Kalk | — | 16.75:1 · AAA |
| Klei | 3.39:1 · **alleen grafisch/groot** | 4.95:1 · AA tekst |
| Klei Diep | 4.75:1 · AA tekst | 3.53:1 · alleen grafisch |
| Stoppel | 5.12:1 · AA tekst | — |
| Stoppel Licht | — | 6.22:1 · AA tekst |

**Klei is geen tekstkleur op lichte grond.** 3.39:1 haalt de grafische drempel (3:1) maar
niet die voor tekst (4.5:1). In de mark is dat prima — dat is een grafisch element. Voor
een link of een label op Kalk gebruik je Klei Diep. Op Nacht draait het om: daar is Klei
wél tekstwaardig en Klei Diep niet.

`tokens.css` regelt die omslag automatisch via `--ploeg-accent`.

---

## 3. Typografie

### Archivo

**Archivo**, variabel, ingesteld op `wght 800` / `wdth 110`, letterspatiëring −1.4 %.
Ontworpen door Héctor Gatti / Omnibus-Type, [SIL OFL 1.1](https://openfontlicense.org/),
beschikbaar via Google Fonts.

De keuze is gemeten, niet gevoeld. De mark heeft een streek van **27.1 %** van zijn hoogte.
Het woordmerk moet daar net onder zitten — de mark is een massief object en hoort iets
zwaarder te lezen dan de tekst ernaast:

| Kandidaat | Stam als % van kaphoogte |
|---|---|
| Archivo 700 / wdth 100 | 20.2 % — te licht |
| **Archivo 800 / wdth 110** | **25.1 % — gekozen** |
| Archivo 900 / wdth 110 | 30.5 % — zwaarder dan de mark |
| Chivo 800 | 25.9 % |
| Familjen Grotesk 700 | 28.7 % |
| Instrument Sans 700 | 20.8 % |
| Public Sans 800 | 34.3 % |
| Bricolage Grotesque 700 | 21.5 % |

Chivo komt er het dichtst bij, maar heeft geen breedte-as. Die as is hier wél nodig:
`wdth 110` geeft het woordmerk de stevige, brede stand die bij een bot werktuig hoort,
zonder naar een extra fontbestand te grijpen. Archivo gaat bovendien terug op
laat-negentiende-eeuwse Amerikaanse grotesques — het register van machinenaamplaatjes,
wat hier toevallig precies klopt.

Afgevallen en waarom: Instrument Sans en Bricolage zijn te licht op 700 en hebben geen
zwaardere variabele stand die de match haalt; Public Sans schiet er ruim overheen;
Bricolage heeft daarnaast eigenzinnige details die met de blunte mark vechten.

### Gebruik

| Rol | Instelling |
|---|---|
| Woordmerk (vast) | Archivo 800, wdth 110, tracking −1.4 % |
| Koppen | Archivo 700, tracking −2 % |
| Interface / labels | Archivo 500–600 |
| Broodtekst | Archivo 400, of de systeemstack |
| Code | een monospace naar keuze; het merk schrijft er geen voor |

Fallback-stack: `"Archivo", "Helvetica Neue", Arial, sans-serif`.

> Het woordmerk in `wordmark.svg` is **omgezet naar contouren**. Er is geen font nodig om
> het te tonen, en het verschuift niet als Archivo ontbreekt. Zet nieuwe tekst nooit na in
> een live font en noem dat het woordmerk — gebruik het bestand.

---

## 4. Lockups

| Bestand | Wanneer |
|---|---|
| `lockup-horizontal.svg` | standaard: README, site, presentaties |
| `lockup-stacked.svg` | vierkante of smalle ruimtes |
| `mark.svg` | naast een bestaande naam, of als de naam al in de context staat |
| `mark-tile.svg` | avatar, app-icoon, org-profiel op de forge |
| `favicon.svg` | browser-tab, 16–32 px |

### Maatverhoudingen

- Kaphoogte woordmerk = **44** in mark-eenheden, tegen een markhoogte van 48. De
  stok van de `l` komt daarmee net onder de bovenkant van de mark uit; de basislijn valt
  gelijk met de onderkant van de mark.
- Tussenruimte mark ↔ woordmerk = **13**, precies één streekbreedte.
- Staffel gestapeld: dezelfde 13 tussen mark en woordmerk.

### Vrije ruimte

Rondom elke lockup minimaal **één streekbreedte** vrij houden — 13 eenheden op een
markhoogte van 48, oftewel 27 % van de hoogte van de mark. Niets in die marge.

### Minimummaten

| | Minimum |
|---|---|
| Mark | 16 px (gebruik dan `favicon.svg`) |
| Horizontale lockup | 96 px breed |
| Gestapelde lockup | 64 px breed |

---

## 5. Wat niet mag

- De tweekleur omdraaien. De **steel is inkt, het blad is Klei** — dat is de betekenis,
  geen decoratie.
- De mark sluiten tot een echte P. Hij is open; dat is het hele punt.
- Streekbreedte, bochtstraal of verhouding aanpassen. Schaal het bestand.
- De mark in een verloop, slagschaduw of omlijning zetten.
- Het woordmerk opnieuw zetten in een ander font, of Archivo op een andere as-instelling.
- De mark op Nacht in `#141A1D` (1.04:1 — onzichtbaar). Gebruik `mark-white.svg` of
  `mark-currentcolor.svg`.
- De mark roteren. De voor loopt horizontaal.

---

## 6. Bestanden

```
mark.svg                      tweekleur, Voor + Klei
mark-currentcolor.svg         thema-volgend: currentColor + var(--ploeg-klei)
mark-mono.svg                 één pad, currentColor
mark-black.svg / -white.svg   één pad, vaste kleur
mark-klei.svg                 één pad, accent
mark-tile.svg                 aflopende tegel, Klei op Voor
favicon.svg / favicon-dark.svg  geoptimaliseerd voor 16–32 px
wordmark.svg / -white.svg     "Ploeg", omgezet naar contouren
lockup-horizontal.svg         + -white, + -mono
lockup-stacked.svg            + -white
tokens.css                    kleur- en fonttokens
png/                          transparante PNG-exports, 512 en 1024 px hoog
```

Alle SVG's hebben `viewBox` en geen vaste eenheden buiten `width`/`height` — schalen doe
je met CSS of door die twee attributen te verwijderen.

De PNG's in `png/` zijn met resvg uit deze SVG's gerenderd — elke variant behalve
`mark-currentcolor.svg` (currentColor heeft buiten CSS geen kleur; gebruik de
black/white-PNG's). Verander je een SVG, render de PNG's dan opnieuw in plaats van
ze te bewerken. Voor plekken die geen SVG slikken: sociale platforms, presentaties,
documenten, e-mail.

---

## 7. Naam en beeldmerk

De code is [Apache-2.0](../../LICENSE), en de bestanden in deze map ook — §6 van
die licentie verleent geen rechten op handelsnamen of merken, en die uitzondering
is opzettelijk. **"Ploeg" en de mark zijn merken**, met hun voorwaarden in
[TRADEMARK.md](TRADEMARK.md).

Kort: je mag de onveranderde mark reproduceren om naar Ploeg te verwijzen —
artikelen, talks, badges, integratielijsten — zonder iets te vragen. Wat
toestemming vraagt: een **fork** onder de naam of de mark uitbrengen, en elk
gebruik dat **endorsement of affiliatie** suggereert.

Er staat bewust **geen aparte licentie onder `docs/brand/`**. Een CC-licentie op
een beeldmerk is het verkeerde instrument: hij is onherroepelijk, hij verleent
het recht om de mark te *wijzigen*, en Creative Commons waarschuwt zelf dat het
je merkrechten kan kosten. De afweging staat in
[ADR-0022](../adrs/0022-the-name-and-mark-are-trademarks-not-cc-licensed-artwork.md);
`scripts/brand-marks.sh` bewaakt hem in CI.

**Archivo** is niet van ons: Héctor Gatti / Omnibus-Type, [SIL OFL 1.1](https://openfontlicense.org/).
Die licentie staat los van het bovenstaande. Wordt het font meegeleverd in een
docs-site, dan hoort de OFL-tekst mee in de repo.
