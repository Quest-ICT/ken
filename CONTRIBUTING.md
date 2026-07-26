# Contributing to Ken

Thanks for your interest in Ken.

## License & sign-off (DCO)

Ken is licensed under the **GNU Affero General Public License v3.0**
(`AGPL-3.0-only`, see [LICENSE](LICENSE)). By contributing, you agree that your
contribution is licensed under those same terms.

Contributions are accepted under the **Developer Certificate of Origin (DCO)** — a
lightweight, standard affirmation that you wrote the patch (or otherwise have the
right to submit it) and agree to it being distributed under the project's license.
Sign off every commit:

```sh
git commit -s -m "your message"
```

That appends a `Signed-off-by: Your Name <you@example.com>` trailer. The full DCO
text is at <https://developercertificate.org/>.

Sign off with **the name you are known by** — it does not have to match a legal
document, and a `@users.noreply.github.com` address is fine. What the DCO is
protecting is *accountability*: a real, contactable person stands behind the
certification. Anonymous handles with no identity behind them are not accepted.

## A note on future commercial licensing

The DCO records provenance; it does **not** transfer copyright or grant the
maintainer the right to relicense your contribution. If Ken later offers a
commercial / dual license, contributions would either need a separate Contributor
License Agreement (CLA) or would remain AGPL-only. Should that path be taken, this
document will be updated and a CLA introduced *before* such contributions are
accepted. For now, everything is `AGPL-3.0-only`.

## Development

- Go 1.26.5+ (the floor in `go.mod`; builds use `GOTOOLCHAIN=local`, so an older 1.26.x fails fast).
- Run `go build ./... && go test ./...` before opening a pull request.
- Docs land with the change: a behavioral change or feature includes its
  documentation and a `CHANGELOG.md` entry in the same PR.
- Keep the UI self-contained (no external requests) and strict-CSP-clean: external
  same-origin JS with delegated `data-*` handlers, never inline scripts or event
  attributes.
