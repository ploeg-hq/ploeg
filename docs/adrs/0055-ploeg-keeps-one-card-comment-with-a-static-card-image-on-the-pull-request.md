---
status: proposed
date: 2026-10-01
decision-makers: Ryan Grippeling
supersedes: none
review-by: 2027-01-31
---

# Ploeg keeps one card comment with a static card image on the pull request

## Context and Problem Statement

A Run card lives in Vloer ([ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md), Vloer ADR 0026). Reviewers, and an agency's clients, look at the pull request instead. They never open Vloer. The owner asked Ploeg to show the card on the pull request when it reaches a moment worth showing: the merge, the release to production, a finish climbed and a mend. The card must look like the Vloer Native card, be drawn on the server without a browser, and expose no per-person figures beyond what the pull request already shows. The questions are what Ploeg posts, how it keeps to a single comment, how it detects moments, how the image reaches the forge, and who turns it on.

## Decision Drivers

* Clients see team aggregates only (owner, 2026-10-01). The comment shows the card's facts and the steward's login, which the pull request already shows as merger or approver. It shows no roster, no crack reporters or confirmers, and no gate actors.
* A single comment that Ploeg edits, like the usage report: a lost comment id must never post a second card.
* No browser and no heavy dependency in ploegd. The image is deterministic and every card text is escaped.
* Best effort: a forge that is down or refuses the file never changes a Work Item, a Shift or an Outcome.
* Off unless a team asks for it.

## Considered Options

* **An SVG card rendered in Go, uploaded as a comment attachment and embedded with Markdown, above a text summary table, in one marked comment per Work Item**
* Rasterise the card to PNG in Go
* Render the card in a headless browser
* Post the text summary table alone
* Link to the card in Vloer

## Decision Outcome

Chosen option: "**SVG in Go, attached to one marked comment, with a summary table**". It needs no new dependency, and forges show it inline.

1. **Image.** `pkg/cardimage.Render` draws a 360-pixel-wide SVG in the layout of the Vloer Native front face. It shows the Unfold wordmark, the title (wrapped to at most three lines), the state chip, the day-live chip with its finish, a cost ring (or "Demo · no model calls" with no amount), diff and pull request · CI tiles, a grade slab when graded, crack marks (red while cracked, gold once mended), the crew line, the steward, the play count and the ids. Finish levels add an edge, a sheen, a band, a gold frame and a dotted orbit. The image contains no `<script>`, `<style>`, `<foreignObject>`, link or external reference. Text is XML-escaped, controls and bidi overrides are stripped, and each slot is truncated. Rendering takes the clock as input, so the same card gives the same bytes.
2. **Skin.** The Work Target's `cardStyle.skin` picks only the colours. `vloer-native` and `forge` have palettes, and any other skin uses `vloer-native`'s. The layout never changes, and the theme is ignored.
3. **Comment.** The body opens with `<!-- unfold:run-card -->`, then `### Run card · <moment>`, the embedded image when an upload succeeded, and `cardimage.Summary`: a table of state, cost, diff, pull request and CI, days live and finish, grade, condition, steward, crew and ids. A footer says Unfold posted it at a card moment and that nothing on it ranks a person. Card text in the table is Markdown- and HTML-escaped, and `@`, `#` and `:` are followed by a word joiner, so a title cannot mention a user, link an issue or add a link. The steward is shown without `@`.
4. **Moments.** `cardimage.MomentOf` builds a key from the latest merged play (`merged:<n>`), a release from a recorded deploy (`released:<env>`), the finish reached (`finish:<key>`) and the number of confirmed mends (`mended:<n>`). A crack alone, a new grade or a new day does not change the key. Ploeg posts when the key differs from the one recorded in `card_comments` (migration 0030). The heading names the newest moment. A withdrawn card, or one with no merged play, has no moment.
5. **Triggers.** A merged-play webhook publishes in the background straight away. The 15-minute sweep (`SweepCardComments`, with the mend sweep) assembles up to 25 opted-in cards per tick, those not checked in the last hour: cards that already have a record, and cards whose play merged in the last seven days. So enabling the option does not backfill old pull requests.
6. **Single comment.** Ploeg lists the play's comments and edits the one that starts with the marker. It creates one only when none exists, and posts nothing when the list fails. A per-Work-Item session advisory lock (`pg_try_advisory_lock`) lets one publisher at a time work on a card across replicas, and a busy card is skipped until the next sweep.
7. **Attachment.** Through `provider.CommentAttacher`, Forgejo uploads to `POST /repos/{owner}/{repo}/issues/comments/{id}/assets` and then deletes the comment's earlier asset of the same name (`unfold-card-<id>.svg`). GitLab uploads to `POST /projects/:id/uploads` and embeds the project-relative URL. GitLab keeps uploads per project, so earlier images stay. When the forge refuses the file, the comment keeps the table alone and the log says why.
8. **Opt-in.** `teams.<team>.cards.prComment: true` turns it on per team, and the default is false. With no team opted in, Ploeg makes no forge call and keeps no record.

### Consequences

* Good, because reviewers and clients see the card where they already are, without a Vloer account.
* Good, because the image needs only the standard library, and golden files pin every byte of it.
* Good, because a lost record, a retried webhook or a second replica edits the same comment instead of posting another.
* Bad, because the image is a snapshot. Days live in it count to the moment it was posted, not to today.
* Bad, because forges show SVG with their own fonts. The text uses a system font stack, and text widths are estimated, so a long line can sit slightly off on some platforms.
* Bad, because a card whose comment lives on an older play stays there when a later play merges. The new play gets its own comment, and the old one is not updated again.
* Bad, because GitLab keeps every uploaded image, one per moment, in the project's uploads.

### Confirmation

In `.forgejo/workflows/on_pull_request.yml`, `go test ./...` in `apps/ploeg` covers:

* `pkg/cardimage`: golden SVG and summary files for merged, escaping, demo, drafting, graded, cracked, mended and forge-skin cards (`TestRenderMatchesTheGoldenImages`), well-formed XML with no script, style, foreign object, image or link element, escaping of every card text in image and table, no amount on a demo card, slab and crack and gold-seam marks, no person but the steward, skin changes only colours, moment keys and headlines, the finish ladder, nl-NL money, the comment body golden file and safe image URLs.
* `pkg/provider/forgejo`, `pkg/provider/gitlab`: attachment upload, replacement of the earlier Forgejo asset, refusal reported with the earlier asset kept, all against `httptest` forges.
* `pkg/httpapi`: off by default, and off when card rules lack `prComment` (`TestCardComment_OffByDefault`); one create, one upload and one edit, no forge write when the moment is unchanged, an edit instead of a second comment after the record was lost, and a finish level-up edit that keeps one image (`TestCardComment_PostsOnceAndEditsInPlace`); a refused image leaves the table; a failed list posts nothing; the sweep posts recent merges and skips old and opted-out ones; a held lock skips; the merge webhook posts the card.
* `pkg/config`: `cards.prComment` is per team and false by default.

`go test ./internal/ledger/` gates this record.

## Pros and Cons of the Options

### Rasterise the card to PNG in Go

* Good, because every forge and every e-mail client shows a PNG.
* Bad, because the pure-Go SVG rasterisers (`oksvg` with `rasterx`) do not draw `<text>`. A card is mostly text, so Ploeg would have to lay out glyphs itself with `x/image/font` and bundle a font. That would be a second renderer to keep in step with the first, and Forgejo already accepts `.svg` attachments: webgrip's instance allows them, checked through `/api/v1/settings/attachment` on 2026-10-01, Forgejo 15.0.2.

### Render the card in a headless browser

* Good, because it would reuse Vloer's own skin exactly.
* Bad, because ploegd would need Chromium. That is hundreds of megabytes, a sandbox, and a slow start on every moment.

### Post the text summary table alone

* Good, because it is the simplest and works on every forge.
* Bad, because it is not the card. The owner asked for the card itself on the pull request. The table stays as the fallback and as the text for screen readers and e-mail.

### Link to the card in Vloer

* Bad, because clients and most reviewers have no Vloer access, and Vloer shows per-person views that clients must not see.

## More Information

* [ADR-0046](0046-a-run-card-is-assembled-per-work-item-from-stored-facts.md) assembles the card that is drawn. [ADR-0047](0047-ploeg-learns-where-a-merged-change-is-deployed-from-a-generic-deploy-endpoint.md) gives the release, [ADR-0050](0050-a-run-cards-grade-is-a-versioned-formula-over-stored-facts.md) the grade and [ADR-0052](0052-a-crack-needs-the-fixer-and-a-second-person-and-ploeg-only-proposes-candidates.md) the cracks and mends.
* The finish ladder is Vloer's: matte, then foil at 7 days, holo at 30, prism at 90, gilded at 180 and infinity at 365 ([Run cards](https://github.com/webgrip/unfold/blob/9c1d53f01fbfb65733800aa75e288341734dc23f/docs/concepts/run-cards.md)).
* The usage report comment (`pkg/shiftengine/publish.go`) uses the same find-by-marker, edit-in-place pattern.
* 2026-10-10 — [ADR-0079](0079-run-cards-belong-to-the-consumer-and-ploeg-supplies-delivery-facts.md) moves the Run card to the operator consumer. This decision stays in force while the card endpoints are deprecated and is removed with them in the next minor release.

## Re-evaluation triggers

* A forge in use refuses SVG attachments, or a reviewer reports the image does not show: add a PNG path with a bundled font, or link the image from Ploeg.
* The owner wants the image to show today's days live: serve it from Ploeg with a signed URL instead of attaching a snapshot.
* A second skin with its own layout is built: decide whether the image follows layouts or stays on the Native layout.
* A sweep tick passes one second of card assembly at p95: keep the moment inputs in a table instead of assembling each card.
