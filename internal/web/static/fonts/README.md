# Fonts

The three families of the Sunbeam look (SPEC D57), self-hosted so that no request goes to another site (the Content-Security-Policy allows fonts from this site only, D36). All are under the SIL Open Font License 1.1; the license of each family is the `OFL.txt` next to its files, and a test fails when a `.woff2` has none.

Each family is a variable font, Latin and Latin Extended only (the Vietnamese, Cyrillic and Greek subsets are not shipped). `@font-face` is in `../app.css`, with `font-display: swap` and the `unicode-range` of each subset; the text falls back to `system-ui, sans-serif` and the code to `ui-monospace, monospace` until a file has loaded.

| Directory | Used for | Weights | Version | Size |
|---|---|---|---|---|
| `bricolage-grotesque/` | the logo and the headings | 600 to 800 (and the optical size axis) | Google Fonts v9 | `latin.woff2` 76,888 B, `latin-ext.woff2` 30,736 B |
| `nunito/` | the text | 400 to 700 | Google Fonts v32 | `latin.woff2` 39,128 B, `latin-ext.woff2` 35,588 B |
| `jetbrains-mono/` | code | 400 to 600 | Google Fonts v24 | `latin.woff2` 31,432 B, `latin-ext.woff2` 11,624 B |

In all, 225,396 bytes.

## Where they come from

Downloaded on 2026-10-28 from the Google Fonts CSS API, which names the `fonts.gstatic.com` file of each subset (asked with a current Chrome User-Agent, so that it answers with woff2):

```
https://fonts.googleapis.com/css2?family=Bricolage+Grotesque:opsz,wght@12..96,600..800&family=Nunito:wght@400..700&family=JetBrains+Mono:wght@400..600&display=swap
```

| File | Source | Last modified at the source |
|---|---|---|
| `bricolage-grotesque/latin.woff2` | https://fonts.gstatic.com/s/bricolagegrotesque/v9/3y9K6as8bTXq_nANBjzKo3IeZx8z6up5BeSl9D4dj_x9PpZBMlGIInE.woff2 | 2025-09-10 |
| `bricolage-grotesque/latin-ext.woff2` | https://fonts.gstatic.com/s/bricolagegrotesque/v9/3y9K6as8bTXq_nANBjzKo3IeZx8z6up5BeSl9D4dj_x9PpZBMlGGInHEVA.woff2 | 2025-09-10 |
| `nunito/latin.woff2` | https://fonts.gstatic.com/s/nunito/v32/XRXV3I6Li01BKofINeaB.woff2 | 2025-09-15 |
| `nunito/latin-ext.woff2` | https://fonts.gstatic.com/s/nunito/v32/XRXV3I6Li01BKofIO-aBXso.woff2 | 2025-09-15 |
| `jetbrains-mono/latin.woff2` | https://fonts.gstatic.com/s/jetbrainsmono/v24/tDbv2o-flEEny0FZhsfKu5WU4zr3E_BX0PnT8RD8yKwBNntkaToggR7BYRbKPxDcwg.woff2 | 2025-09-10 |
| `jetbrains-mono/latin-ext.woff2` | https://fonts.gstatic.com/s/jetbrainsmono/v24/tDbv2o-flEEny0FZhsfKu5WU4zr3E_BX0PnT8RD8yKwBNntkaToggR7BYRbKPx7cwhsk.woff2 | 2025-09-10 |

The license files are the `OFL.txt` of each family in https://github.com/google/fonts (`ofl/bricolagegrotesque/`, `ofl/nunito/`, `ofl/jetbrainsmono/`), fetched on the same day:

- Bricolage Grotesque: Copyright 2022 The Bricolage Grotesque Project Authors (https://github.com/ateliertriay/bricolage)
- Nunito: Copyright 2014 The Nunito Project Authors (https://github.com/googlefonts/nunito)
- JetBrains Mono: Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono)

To update a family, fetch the new CSS, download the two files named for `latin` and `latin-ext`, replace the files here and the table above, and check that the page still looks right in `clowder-proof`.
