# Narvi brand assets

The mark is a doorway traced in starlight whose two jambs and arch form a lowercase **n** — the first
letter of the name is a door — with a star set into the keystone. It is drawn for dark surfaces
(night-stone `#0b1020`); it is not meant to be recolored for light backgrounds — the dark ground
travels with the mark (as in the avatar).

| File | What it is | Use for |
|---|---|---|
| `mark.svg` | Large-size master: hairline strokes, node stars, ambient dots. Transparent background. | Banners, web pages, anything rendered ≥ 64px on a dark surface. |
| `mark-small.svg` | Small-size master: thickened strokes, single keystone star. Transparent background. | Avatars, favicons, anything < 64px. |
| `avatar.svg` / `avatar-512.png` | The small master centered on the night-stone ground, 512×512. | GitHub organization avatar (upload the PNG — GitHub does not accept SVG avatars). |
| `banner.svg` / `banner-1280x640.png` | Mark + lowercase `narvi` wordmark + uppercase tagline, tagline width-locked to the wordmark via SVG `textLength`. | GitHub repository social preview (Settings → Social preview; upload the PNG). |

Two masters exist on purpose: the hairline drawing disappears below ~64px, so small sizes get a
heavier cut with a single star — standard size-specific logo practice, not two competing logos.
