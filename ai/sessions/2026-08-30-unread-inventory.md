# Session — 2026-08-30 · statement unread inventory (D59)

Working note, not a decision. Records what the statement parser does not yet read,
so the next session decides shapes instead of rediscovering them. Measured over the
three real May–July 2026 statements through the real pdftotext extractor.

## The count

548 dated rows = **422 read + 126 skipped**.

Read: Compra 208 · SPEI in 70 · SPEI out 60 · Retiro Cajita 52 · respaldo 22 ·
Depósito Cajita 10. Every read SPEI row carried a tracking key.

## The 126 skipped

**84 — mirror rows of internal movements. Deliberately unread (D55).** Each
Cajita/respaldo movement prints twice, once per account with opposite signs; the
parser reads the account-section side only. 52 Retiro `-` + 22 respaldo `-` +
10 Depósito `+` = 84, matching the 84 read. Pinned by
`TestTheMirrorRowOfAnInternalMovementIsNotRead`.

**42 — genuinely unread shapes. No parsing code written; awaiting decisions.**

| Rows | Shape | Note |
|---|---|---|
| 11 | `Pago a tu tarjeta de crédito Nu` | Lean: external OUTFLOW. Credit card is its own product, not a D62/D63 debit-side account. |
| 6 | `Cajero <operator> Retiro de efectivo` | ATM cash withdrawal, outflow. Existing vocabulary (merchant). |
| 6 | `<merchant> Devolución` (Uber, DiDi, OnlyFans) | Lean: external INFLOW, merchant set, no refund relationship yet (DATA_MODEL §9 open). |
| 5 | `Bonificación por beneficio de Nu` | Nu benefit, inflow. |
| 4 | `Compensación de retraso SPEI` | Nu SPEI-delay compensation, inflow. |
| 4 | `<merchant> Ajuste realizado` (Apple, Uber) | Purchase adjustment, Compra-like. |
| 2 | `Pago de servicio - <merchant>` (Totalplay) | Service payment, outflow. |
| 2 | `Descongelamos saldo de tu Cajita: <name>` | Cajita family; likely internal. |
| 1 | `Depósito en punto de venta` | POS deposit, inflow. |
| 1 | one row, `-$5,000.00`, no description block | Detail did not attach, or genuinely blank. Investigate geometry. |

Author's calls on 2026-08-30: hold the whole set for one decision session; card
payment and Devolución both external. Entries to be drafted then, not here.

## Furniture confirmed inert

The `Gastos` cover total (one-word label at y≈409.2 + signed amount, no date block)
and the unsigned amount-column figures both yield no row. Pinned by
`TestACoverSummaryLineIsNotAMovement` and
`TestAnUnsignedAmountColumnFigureIsNotAMovement`. A date block is exactly three
words `DD MON YYYY`, which is the discriminator.
