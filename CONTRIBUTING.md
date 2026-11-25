# Contributing

This project follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Setup

Go 1.27+. Local GitHub App and server: [DEVELOPMENT.md](DEVELOPMENT.md).

```bash
make build
make test
make lint    # golangci-lint
```

Three checks are required before merge, on every PR:

- `test` (`CI` workflow): gofmt, `go vet`, `go build` and `go test -race`.
- `lint` (`Charts` workflow): `helm lint`, `ct lint` and chart render checks.
- `golangci-lint` (`CI` workflow): golangci-lint with `.golangci.yml`.

Run `make lint` before pushing; the `golangci-lint` check runs the same linters.

## Pull requests

- One change per PR, small and short-lived.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/): `type(scope): description`.
- Add or update tests with behaviour changes.
- A change to the preview contract ([`docs/CONTRACT.md`](docs/CONTRACT.md)) regenerates the schemas (`make schema`) and the goldens (`make contract-golden`). Adding, removing or changing a field means a new `apiVersion` (`v1beta2`, or `v1`, under `badil.dev/`): the contract rejects unknown fields, so a v1beta1 renderer refuses the file.
- A new Go package goes into a layer of [Code layers](docs/ARCHITECTURE.md#code-layers): add it to the table there, to a depguard rule in `.golangci.yml` and to the map in `internal/archtest`.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from Conventional Commits.

- `feat`, `fix`, `perf`, `revert` and `docs` commits open or update the release PR; `docs` commits are released so that docs changes reach badil.dev. `chore`, `style`, `refactor`, `test`, `build` and `ci` commits don't, unless they are breaking (`!`).
- On `main` it keeps a `chore(main): release X.Y.Z` PR up to date. Merging it tags `vX.Y.Z`, creates the GitHub Release, and publishes `ghcr.io/badilhq/badil:X.Y.Z` (also `X.Y` and `latest`, for linux/amd64 and linux/arm64) and the `badil` chart.
- Each release also gets the `badil` binaries (linux and darwin, amd64 and arm64) and their checksums, built and uploaded by the `binaries` job of the Release workflow with GoReleaser (`.goreleaser.yaml`). The release notes stay release-please's. If the job fails, re-run it from that Release run.
- Checks on the release PR don't start on their own. A maintainer re-runs them from the PR, checks that the diff only touches `CHANGELOG.md`, `.release-please-manifest.json` and `charts/badil/Chart.yaml`, then merges.

Chart versioning:

- Never hand-edit `version`/`appVersion` in `charts/badil/Chart.yaml`; the release sets both.
- A PR that changes `charts/preview-apps` or `charts/app` must bump that chart's `version` (enforced by the required `lint` check of the `Charts` workflow).
- Merged bumps are published to `oci://ghcr.io/badilhq/charts` on the next push to `main`.
- If the image or chart publish of a release fails, re-run the failed jobs of that Release run: later pushes don't retry it.

Docs site:

- After a release's charts are published, the `docs` job of the Release workflow writes the release tag to `docs/badil-version` in [`badilhq/badil.dev`](https://github.com/badilhq/badil.dev) and pushes it to `main` as `chore(docs): show badil vX.Y.Z`. That push deploys badil.dev with the docs of the release. If it fails, re-run it from that Release run.

Gitops repo template:

- After a release's charts are published, the `template` job of the Release workflow builds `dist/template/` from the tag (`scripts/build-template.sh`, `make template` locally) and pushes it to [`badilhq/gitops-template`](https://github.com/badilhq/gitops-template) as `chore: sync with badil vX.Y.Z`, only when its content changed. If it fails, re-run it from that Release run.
- A PR that changes a commented line of `examples/argocd/badil.yaml` changes `template/badil.yaml` the same way, except `front`'s `# needs: [backend-1, backend-2]`, which the template omits, and the commented `preview.expose` block, which names `my-service` instead of the example's services.
- One-time setup, by a maintainer, before the first release that runs the job:
  1. Create `badilhq/gitops-template`, public, with default branch `main` and an initial commit on it, and enable "Template repository" in its settings.
  2. Create a GitHub App owned by `badilhq` (`badil-release`), with no webhook and the repository permission `Contents: Read and write` only, and install it on `badilhq/gitops-template` and `badilhq/badil.dev` only.
  3. Store its Client ID as the `RELEASE_APP_CLIENT_ID` Actions secret of `badilhq/badil`, and a generated private key as `RELEASE_APP_PRIVATE_KEY`.

  Until then the `template` and `docs` jobs fail on each release; re-run them once set up.

## License

Contributions are licensed under [Apache-2.0](LICENSE).
