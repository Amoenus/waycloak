# Contributing

Waycloak welcomes design and implementation contributions while its API is pre-1.0.

Before starting substantial work:

1. Read `AGENTS.md` and the product PRD.
2. Check the implementation roadmap and existing decisions.
3. Open or reference an issue for behavior that changes the public API or security model.
4. Add an ADR when introducing a difficult-to-reverse architectural decision.

Changes should include tests proportional to risk. Networking and admission changes require end-to-end coverage, including a negative or failure-path test. Generated CRDs, chart copies, and KCL schemas must be updated in the same change as their source. Release contributors use the checksummed KCL CLI installed by `hack/install-kcl.sh`.

Commits should be narrowly scoped and use an imperative subject. Pull requests should explain the user-visible outcome, security impact, verification performed, and any unresolved tradeoffs.
