# Coordinate fixtures

Three redacted windows of `pdftotext -bbox-layout` output, one from each of the
May, June and July 2026 Nu statements (D51, D54). They exist so the statement
parser can be written and tested without a real statement and without Poppler.

## What is real and what is not

**Real:** every coordinate, the page size, the `page → flow → block → line →
word` nesting, the number of words on a line, the order the blocks arrive in,
and the structural Spanish vocabulary Nu prints (`Compra`, `Depósito SPEI,`,
`Clave de rastreo`, `FECHA`, the legal footer).

**Synthetic:** every name, account number, CLABE, tracking key, reference
number, card number, concept, merchant and amount.

Redaction replaces the text inside `<word>` and nothing else. The generator
checks that the file is byte-identical to the original once word text is
removed, so a fixture always describes the document it was cut from.

## Why the values look the way they do

Long numbers are built from a repeated two-digit motif — `050505050505` — so
they keep the length and shape a parser must handle while remaining impossible
to confuse with a real identifier. `TestCoordinateFixturesAreSmallAndRedacted`
enforces this: any digit run of seven or more characters with more than two
distinct digits fails the build. Names and merchants become pool words with a
numeric suffix (`ALVAREZ76`, `Panaderia65`), which cannot collide with real
vocabulary. The mapping is stable inside a statement, so the same counterparty
reads the same way in a row and in its detail block.

## What each fixture is for

| File | Shows |
|---|---|
| `nu_2026_05_bbox.xhtml` | Ordinary rows. The date, description and amount are three sibling blocks that share a `y`, and the blocks do not arrive in row order. Holds a six-line SPEI detail with two lines on one `y`. |
| `nu_2026_06_bbox.xhtml` | A wrapped detail block, the case plain `-layout` misplaced five times. |
| `nu_2026_07_bbox.xhtml` | Two pages. The second opens with a detail block that belongs to a row dated on the first, before any date of its own. |

## Regenerating

The statements themselves are not in git. They live in `testdata/statements/`,
which `.gitignore` excludes together with every `*.pdf` and `*.raw.xhtml`. With
them present:

```sh
pdftotext -bbox-layout testdata/statements/Mayo_2026.pdf /tmp/raw/Mayo.xhtml   # and Junio, Julio
python3 testdata/statements/redact.py /tmp/raw
python3 testdata/statements/excerpt.py internal/adapter/extractor/pdftotext/testdata
```

`redact.py` refuses to write if any redacted token survives or if a coordinate
moved.
