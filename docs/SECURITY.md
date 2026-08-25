# BillyCore — Security

BillyCore holds a complete, structured, machine-readable record of one person's
financial life. That is the whole product, and it is also the whole problem: the
thing that makes BillyCore useful is exactly the thing that makes it worth stealing.

This document records the security decisions that follow from that, and names the ones
deliberately not taken. It is subordinate to what is already decided elsewhere —
[PRODUCT.md](PRODUCT.md) established self-hosted, single-user, open source;
[ARCHITECTURE.md](ARCHITECTURE.md) chose one binary, SQLite, loopback HTTP, and a single
bearer token; [DOMAIN.md](DOMAIN.md) §9 drew the boundary that makes validation the
security control it is. Where those documents left a question open, this one either
closes it on security grounds and says so, or leaves it open in §12.

---

## 1. What is being protected

Not all of BillyCore's data is equally dangerous to lose.

| Class | What it is | Loss means |
|---|---|---|
| **C1 — Source credentials** | Gmail OAuth refresh tokens, future bank or Source secrets | Attacker gains ongoing access to the *source*, not just the copy. Survives deleting `billy.db`. |
| **C2 — API token** | The single bearer token from ARCHITECTURE.md §6 | Full read and write access to every endpoint for as long as the binary runs. |
| **C3 — Raw Evidence** | `evidence.raw_content` — original emails, statement bytes | Full account numbers, names, addresses, balances, and everything else the sender chose to include. Richer than the domain model built from it. |
| **C4 — Derived financial data** | Transactions, Claims, events | A precise behavioral profile: where the user is, what they buy, who pays them, when. |

The ordering matters. C1 is the only class where a breach keeps producing new damage
after it is discovered, which is why credential storage gets a decision in §5 and
`billy.db` does not get one in §6.

C3 deserves one more note. Evidence is deliberately preserved verbatim
(DOMAIN.md §2), which means BillyCore stores *more* sensitive data than it models. A
bank email may carry a full card number in a footer that no parser ever reads. The
immutability invariant is a domain requirement and is not negotiable here; the
consequence is that raw Evidence must be treated as the most sensitive stored class,
not as a harmless blob.

---

## 2. Threat model

### In scope

| # | Threat | Mitigated by |
|---|---|---|
| T1 | Another process or user on the same machine reaching the API | Loopback bind, bearer token (§4) |
| T2 | Another local user reading `billy.db` or the credential file | Filesystem permissions (§5, §6) |
| T3 | Hostile Evidence — a crafted email or PDF exploiting a parser | Parser rules (§7) |
| T4 | A Source or network position injecting fabricated financial data | Provenance; §8 |
| T5 | A buggy or compromised proposer (BillyAgent) corrupting the domain | Invariant validation at the boundary (§9) |
| T6 | Secrets leaking through logs, errors, or crash output | Redaction rules (§10) |
| T7 | Backups quietly exporting everything in cleartext | §6, stated as a user-facing consequence |
| T8 | A malicious dependency reading the database or exfiltrating it | Dependency policy (§11) |

### Explicitly out of scope

- **A compromised host.** If an attacker has code execution as the user running
  BillyCore, they have the data. Nothing in a single-user self-hosted daemon changes
  that, and pretending otherwise produces security theater.
- **Root or physical access** to an unlocked machine.
- **Multi-tenant isolation.** There is no second user to isolate from
  (DATA_MODEL.md §9).
- **The Source's own security.** If the user's Gmail account is compromised, BillyCore
  is downstream of that.
- **Targeted attackers with resources.** The realistic adversary here is opportunistic:
  a stolen laptop, an over-broad backup, a leaked token in a screenshot, a malicious
  dependency.

The out-of-scope list is not an excuse list. It is there so that §3–§11 can be honest
about what the controls actually buy.

---

## 3. Trust boundaries

```
        ┌─────────── untrusted ───────────┐
        │  Email / bank statement content │
        └────────────────┬────────────────┘
                         │  fetched, never executed
   ┌─────────────────────▼─────────────────────┐
   │  billycore process                        │
   │                                           │
   │   adapter/source  ── fetch, no parse      │
   │   adapter/parser  ── hostile input zone   │
   │   domain          ── invariants           │
   │   store/sqlite    ── billy.db             │
   └───────▲───────────────────────┬───────────┘
           │ bearer token          │ file perms
    ┌──────┴──────┐         ┌──────▼──────┐
    │  BillyAgent │         │  billy.db   │
    │  semi-trust │         │  creds file │
    └─────────────┘         └─────────────┘
```

Three boundaries carry weight:

1. **Evidence content is untrusted input.** It arrives from outside, it is attacker-
   influenceable (anyone can email the user), and it is fed to parsers. §7.
2. **Proposers are semi-trusted.** A holder of the bearer token can read everything and
   propose anything, but cannot produce an invalid domain state. §9.
3. **The database boundary is physical** (PRODUCT.md). Nothing reaches into `billy.db`
   except BillyCore. This is a security property, not only an architectural one — it
   means there is exactly one code path that can write financial state.

---

## 4. The API token

**Decision.** One bearer token, read from config at startup, required on every `/v1`
endpoint. `/healthz` is unauthenticated (API.md §2, §11).

Rules:

| Rule | Reason |
|---|---|
| Compared in constant time | A naive `==` on a secret is a timing oracle. Cheap to avoid. |
| Never logged, never echoed in an error | §10 |
| `401` does not distinguish *missing* from *wrong* | Already in API.md §2; keeps the endpoint from confirming token shape |
| Refuse to start with an empty or absent token | A default-open financial API is not an acceptable failure mode |
| Refuse to start bound to a non-loopback address without an explicitly set token | See below |

**Loopback is a control, not a default.** ARCHITECTURE.md §6 binds to loopback. That is
the primary mitigation for T1 — a single static token is weak authentication, and it is
only adequate because the listener is not reachable off-host. If the user overrides the
bind address, the token stops being a second line of defense and becomes the only one.
BillyCore should say so loudly at startup rather than silently accepting it.

**Not doing:** scopes, per-consumer tokens, rotation endpoints, expiry, mTLS, TLS
termination. One user, one consumer, loopback. Every one of these becomes justified the
moment the "multi-user, hosted, or shared deployment" line in ARCHITECTURE.md §9 stops
being out of scope — and none of them are worth building before then.

---

## 5. Source credentials

This closes **ARCHITECTURE.md open question 2** — where Gmail OAuth tokens live.

**Decision.** Source credentials are stored in a separate file next to `billy.db`,
created `0600`, never inside the database.

```
billy.db            0600   evidence, claims, transactions
billy.db-wal        0600
billy.db-shm        0600
credentials.json    0600   OAuth refresh tokens, Source secrets
```

**Why not in the database.** C1 and C3/C4 have different lifetimes and different
blast radii. `billy.db` is the file PRODUCT.md tells the user to copy for backups —
"the user backs up their financial history by copying a file" (ARCHITECTURE.md §7). If
refresh tokens live in that file, every backup, every `scp` to another machine, and
every copy handed to a support conversation is a live credential leak that keeps
working after the copy is discarded. Separating them means the recommended backup
gesture stays safe by default.

**Why not the OS keychain.** It is the better answer on security grounds and it loses
on the delivery commitment. Keychain access means a cgo dependency on macOS, a
different API on Linux, an interactive unlock prompt for a background daemon, and a
credential store that a headless server may not have at all — against
ARCHITECTURE.md §3, which chose pure-Go specifically to keep the binary static and
cross-compilable. The keychain remains the right upgrade if BillyCore is ever packaged
as a desktop app; that is a different delivery shape.

**Consequences accepted:**

- Refresh tokens sit in cleartext on disk, protected by file permissions and whatever
  full-disk encryption the user runs. On a stolen powered-off encrypted laptop this is
  adequate; on a shared machine it is not, and §12 keeps that open.
- BillyCore must verify permissions on the credential file at startup and refuse to
  read a world-readable one. A file that *is* the credential must not be silently
  usable when its permissions say it has already leaked.
- OAuth scopes are requested read-only. BillyCore reads email; it never needs to send,
  delete, or modify, and the token it holds should not be able to.

---

## 6. Data at rest

**Decision.** No application-level encryption of `billy.db` in v1. `0600` permissions
and the user's full-disk encryption.

The honest reasoning: an encrypted SQLite file needs a key, and the key has to be
available to an unattended daemon that starts at boot. Storing that key next to the
database is obfuscation, not encryption. Prompting for a passphrase at startup is real,
and it converts BillyCore from "a binary you run" into "a binary you must babysit
through every restart" — which breaks the daemon shape. SQLCipher additionally means
cgo, which contradicts ARCHITECTURE.md §3.

So the choice is between honest file permissions and a key-management story that does
not exist yet. This document picks the first and refuses to claim the second.

What that buys and what it does not:

| Scenario | Protected? |
|---|---|
| Stolen laptop, powered off, FDE on | Yes — by FDE, not by BillyCore |
| Stolen laptop, unlocked | No |
| Another user account on the same machine | Yes — file permissions |
| Backup copied to cloud storage | **No.** The backup is cleartext financial history. |
| Malicious dependency running in-process | No |

The backup row is the one worth surfacing to the user in documentation rather than
burying here. "Copy one file to back up" is a genuine feature of the SQLite decision,
and its security consequence is that the copy is as sensitive as the original and
belongs somewhere encrypted.

**Deletion.** Deleting a row does not erase the bytes; SQLite leaves them in free pages,
and the WAL keeps history. Any future "delete my Evidence" feature has to reckon with
`VACUUM` and with the fact that DATA_MODEL.md open question 2 has not yet decided
whether Evidence can be deleted at all. Not solved here; flagged so it is not
accidentally claimed later.

---

## 7. Evidence is hostile input

This is the largest real attack surface, and it is the one most easily forgotten,
because the input *feels* trusted — it is the user's own email.

It is not. Anyone who knows the user's address can put bytes into the parser.

**The staged pipeline is a security property.** ARCHITECTURE.md §5 persists Evidence
before extraction for reliability reasons. It also means that a parser crash cannot
lose the artifact, and that a malformed input is reproducible after the fact instead of
vanishing with the process that choked on it.

Rules for `internal/adapter/parser` and everything upstream of it:

| Rule | Threat |
|---|---|
| **Never execute or render Evidence.** No HTML rendering, no headless browser, no JS. Parse as text and structure. | T3, code execution |
| **Never resolve remote references.** No fetching `<img>` sources, no following links, no external entity resolution. | Exfiltration by tracking pixel; SSRF |
| **XML/XXE off by default.** Any XML or SVG path disables external entities and DTDs. | T3 |
| **Bounded everything.** Cap artifact size at ingestion, cap decompressed size, cap parse time per artifact, cap recursion depth. | Zip bombs, catastrophic backtracking, pathological nesting |
| **No regex on unbounded input without a timeout.** Bank templates invite greedy patterns. | ReDoS |
| **A parser panic is contained.** Recover per-artifact, mark the row failed, keep serving. | Availability; one bad email must not stop the pipeline |
| **Attachments are bytes, not files.** Never write an attachment to a path derived from its declared filename. | Path traversal |

A failed parse is an ordinary outcome, not an incident: `attempts` increments,
`last_error` records why (redacted per §10), and the Evidence stays exactly as
received. That is the design working.

---

## 8. Fabricated Evidence

An attacker who can email the user can manufacture something that looks like a bank
notification. BillyCore will ingest it, a parser may recognize it, and a Transaction may
appear that never happened.

This is not fully preventable and should not be described as if it were. What the
design does provide:

- **Every Claim carries provenance to Evidence** (DOMAIN.md §4, API.md violation code
  `provenance.required`). Nothing is asserted without a traceable artifact behind it.
- **Evidence is immutable and retained**, so a fabricated Transaction can always be
  traced back to the message that produced it.
- **`source_id` and `source_type` are recorded per artifact**
  (DATA_MODEL.md §4.1), so "which Source told us this" is always answerable.

What is deliberately *not* built in v1: sender authentication as a domain signal. Using
SPF/DKIM/DMARC results to weight a Claim's confidence is a genuinely good idea and it is
a **domain** decision — it changes what confidence means (DOMAIN.md §7) — so it does not
get invented in the security document. §12 carries it.

Until then the mitigation is honest and weak: the trail exists, so a fabrication is
discoverable after the fact. BillyCore may be wrong, but it is never unsupported.

---

## 9. Validation is the security boundary

DOMAIN.md §9 says BillyCore validates what BillyAgent proposes. That boundary is usually
discussed as architectural hygiene. It is also the containment story for a class of
compromise that is genuinely likely — BillyAgent is the component that will run LLM
output, and LLM output is influenced by Evidence content, which §7 already established
as attacker-influenceable.

Prompt injection in a bank email is not hypothetical. The realistic path is:

```
crafted email  →  Evidence  →  BillyAgent reads it  →  LLM emits an attacker-shaped proposal
                                                     →  POST /v1/claims
                                                     →  BillyCore validates  ←── the wall
```

What the wall holds:

- A proposal cannot bypass domain invariants. Same constructor, same rules, whether the
  Claim came from an internal parser or over HTTP (ARCHITECTURE.md §4).
- A proposal cannot invent provenance — `provenance.evidence_not_found` is checked
  against stored Evidence, not taken on trust.
- A proposal cannot force a `MATCH` where Core computes `AMBIGUOUS` (ARCHITECTURE.md §6).
- A proposal cannot submit Money as a float or a negative number.

What the wall does **not** hold, stated plainly:

- A proposer holding the token can **read everything** — every Transaction, every piece
  of raw Evidence. Validation constrains writes; it does nothing for reads.
- A proposer can make *plausible* wrong Claims all day. Every one of them is valid,
  provenanced, and false. Invariants are not truth.

This is why §4 refuses to hand out long-lived shared tokens casually, and it is the
strongest argument for per-consumer tokens the day a second consumer exists.

---

## 10. Logging, errors, and redaction

The fastest way to leak C1–C3 is not an attacker. It is a log file, a crash dump, or a
GitHub issue with a stack trace pasted into it.

| Rule | |
|---|---|
| **Never log `raw_content`**, or any substring of it | Not at debug level, not "temporarily" |
| **Never log the bearer token or credentials**, including in request dumps | Redact `Authorization` at the middleware, not at each call site |
| **`last_error` is diagnostic, and it is redacted.** Store parser class and failure reason, never the input that caused it | DATA_MODEL.md §4.1 already says domain code never reads it; §7 makes it a place where hostile input would otherwise land verbatim |
| **API errors never echo request bodies.** `violations[].field` is a JSON path; `detail` describes the rule | API.md §3 defines the shape; this makes it a security rule too |
| **Money amounts are not logged at info level** | A log of every transaction is a second copy of C4 with weaker permissions |
| **Panics log a stack trace, not the artifact being processed** | |

Practically: one redaction helper, applied at the log boundary, and a review reflex that
treats a new `log.Printf` near parser or auth code as something to look at twice.

---

## 11. Dependencies

BillyCore runs in one process with full access to every class in §1. Anything linked in
inherits that access, so dependency count is a security number, not just a build-time
one.

- **Keep the tree small and boring.** Standard library first. ARCHITECTURE.md §4 already
  rules out an ORM; the same instinct applies to HTTP routers, config libraries, and
  helper packages.
- **Pin and commit `go.sum`.** Reproducible builds.
- **Review what a new dependency pulls in**, not just the dependency.
- **Vulnerability scanning in CI** — `govulncheck` is free and Go-native.
- **Parsing libraries get the most scrutiny**, because they are the code §7 points
  hostile bytes at.

The pure-Go SQLite choice (ARCHITECTURE.md §3) helps here as a side effect: no cgo means
no C memory-safety surface in the storage path.

---

## 12. Open questions

1. **Should DKIM/SPF results feed Claim confidence?** §8. It is a domain question —
   it changes what confidence means (DOMAIN.md §7) — and it may be the single highest-
   value control against fabricated Evidence.
2. **Does raw Evidence need a retention policy?** BillyCore currently keeps every
   artifact forever. Retention reduces C3 exposure and collides directly with the
   immutability invariant and DATA_MODEL.md open question 2.
3. **Should `raw_content` be encrypted at rest separately from the rest of the
   database?** It is the most sensitive column and the only one nothing queries into,
   so it is the one column where per-value encryption is actually feasible. Still needs
   the key-management answer §6 does not have.
4. **What is the credential file's story on a shared machine?** §5 accepts cleartext-
   plus-permissions. If BillyCore ever runs somewhere the user does not solely control,
   that assumption fails before anything else does.
5. **Does `GET /v1/evidence/{id}` returning raw content need to be separately
   gated?** It is the endpoint that hands out C3 in full, and it currently carries the
   same authorization as `GET /v1/transactions`. Related to API.md open question 7.
6. **Is there any audit trail for reads?** Domain events record what changed
   (DOMAIN.md §8). Nothing records who read what, and with one user that is arguably
   correct — but it means a stolen token leaves no trace.
7. **Token rotation without a restart.** Today the token is read at startup, so
   revoking it means restarting the daemon. Acceptable now; worth naming.

---

> Security decisions here follow the same rule as everywhere else in BillyCore: a
> control is written down when a real threat justifies it, and a control that is not
> built is named rather than implied.
