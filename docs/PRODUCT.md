# BillyCore — Product

## Problem

People in Mexico have their financial information scattered across email and other
sources, with no normalized dataset that software or AI can actually use.

- Mexican law obliges banks to provide open finance. In practice it does not exist.
- Companies that do sell access to financial data via API charge a high cost.
- The common workaround is reading your own bank statement by hand, which is not a
  usable dataset for anything.

## What BillyCore is

Infrastructure that connects to those sources and reconstructs structured financial
information from them.

## What BillyCore is not

- Not an AI agent
- Not an MCP server
- Not a financial adviser
- Not a budgeting UI
- Not SAT integration

Those belong to BillyAgent and BillySat.

## Who it is for

First user: myself, and developers building on top of Billy.

BillyCore is a building block, not an end-user product. It is judged by whether
something else can be built on top of it — starting with BillyAgent.

## Sources

Initial:

- Email / Gmail
- Bank statements

BillyCore must be able to absorb new sources of financial information without
rework. Email is the first source, not the assumption the system is built around.

## Product stances

**Usable without AI.** BillyCore must produce useful, structured output on its own.
AI is a consumer of BillyCore, never a dependency of it.

**Self-hosted.** Open source, run by the user on their own setup. The financial data
stays with the person it belongs to.

**Consumed through a public interface.** BillyAgent — and anything else — talks to
BillyCore through its public interface. Nothing reaches into BillyCore's database.

## MVP definition of success

One month. Working open-source MVP.

> I can see my last month of transactions, well classified, in a good table with
> good financial information.

## Delivery shape

BillyCore is delivered as a **self-hosted service** — a daemon exposing endpoints,
not a library and not a batch export.

Consequences accepted:

- The database boundary is physical. BillyAgent cannot reach the data except through
  the endpoints, regardless of who writes it.
- Consumers are language-agnostic. BillyAgent can be Go, Python, TypeScript, or
  anything else.
- BillyCore owns auth, interface versioning, and process lifecycle.
- The user has one more thing to run. Packaging should keep this to a single
  artifact started locally.

## Open questions

- **Protocol.** HTTP or gRPC. To be decided in ARCHITECTURE.md; it does not change
  the product stance above.
