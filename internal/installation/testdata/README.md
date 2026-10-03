# Public release verification fixtures

`release-manifest.json` and `release-manifest.sigstore.json` are the unchanged
public assets of [Waycloak v1.0.2-rc.3](https://github.com/Amoenus/waycloak/releases/tag/v1.0.2-rc.3).
They contain artifact identities and public signing evidence, not credentials.
The tests verify the exact workflow identity and tag without network access.

`trusted-root.json` is the public-good test trust root from
`github.com/sigstore/sigstore-go@v1.3.0/pkg/testing/data/trusted-roots/public-good.json`.
Its upstream Apache-2.0 license is retained in `sigstore.LICENSE`.
This static fixture is used only by tests. Runtime verification obtains its
trusted material through Sigstore's authenticated TUF client.
