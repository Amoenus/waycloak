## Outcome

What user-visible or operational outcome does this change deliver?

## Security impact

How does it affect routing, admission, capabilities, credentials, provider access, or supply chain? State “none” with a reason when appropriate.

## Verification

List commands and behavioral tests. Networking changes must include a negative/failure-path result.

## Dependency qualification

For every added or materially upgraded dependency, record maintenance,
security-reporting path, license, exact pin, SBOM/provenance/vulnerability
evidence, compatibility/rollback results, and measured binary/RSS impact. State
“none” when the dependency graph and shipped tool/image inventory are unchanged.

## API and documentation

- [ ] Generated artifacts are current.
- [ ] User-facing behavior and conditions are documented.
- [ ] An ADR was added or updated for difficult-to-reverse decisions.
- [ ] No credentials, private endpoints, or identifying public-IP history are included.
- [ ] `make dependency-audit` passes and every deliberate version lag has an owner and review date.

## Release note

Describe migration or compatibility impact, or state “none.”
