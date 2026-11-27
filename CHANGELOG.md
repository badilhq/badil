# Changelog

## [0.33.0](https://github.com/badilhq/badil/compare/v0.32.0...v0.33.0) (2026-11-27)


### ⚠ BREAKING CHANGES

* **chart:** charts/app is no longer published; published versions stay on ghcr.io/badilhq/charts. Service charts are the user's.
* **chart:** the namespaceGuard chart values are removed; apply examples/namespace-guard instead.
* **chart:** the cleanup CronJob (cleanup.enabled, cleanup.schedule) and the badil cleanup command are removed; the sweep runs in badil serve every cleanup.interval.
* **cli:** badil verify-app is removed; badil serve checks the App credentials at startup.

### Features

* **chart:** move the namespace guard from the chart to an example ([#171](https://github.com/badilhq/badil/issues/171)) ([1d016e5](https://github.com/badilhq/badil/commit/1d016e525d10d85a13ac787e956b2f1d1b724672))
* **chart:** run the cleanup sweep in the server only ([#170](https://github.com/badilhq/badil/issues/170)) ([e88697f](https://github.com/badilhq/badil/commit/e88697f4d74082f4a82c3cf35c688022a4375149))
* **chart:** stop shipping the app service chart ([#172](https://github.com/badilhq/badil/issues/172)) ([5ced2d8](https://github.com/badilhq/badil/commit/5ced2d800935afd5be4c2cd6377ad479361a629d))
* **cli:** remove the verify-app command ([#169](https://github.com/badilhq/badil/issues/169)) ([9e9777a](https://github.com/badilhq/badil/commit/9e9777a055611a3288bd4f87efd7216667fd4f83))


### Documentation

* state badil's scope and add a kyverno recipe for preview secrets ([#168](https://github.com/badilhq/badil/issues/168)) ([6d23580](https://github.com/badilhq/badil/commit/6d23580a90956f96839755f99755a79d683c370a))

## [0.32.0](https://github.com/badilhq/badil/compare/v0.31.0...v0.32.0) (2026-11-27)


### ⚠ BREAKING CHANGES

* **cli:** badil bootstrap-app is removed; create the GitHub App by hand as INSTALL §5 says.

### Features

* **cli:** remove the bootstrap-app command ([#166](https://github.com/badilhq/badil/issues/166)) ([6b7d469](https://github.com/badilhq/badil/commit/6b7d4694d19d9629f50bf069eb007df0f02617f1))

## [0.31.0](https://github.com/badilhq/badil/compare/v0.30.2...v0.31.0) (2026-11-26)


### ⚠ BREAKING CHANGES

* **gitops:** badil no longer falls back to the GitHub App installation token for the gitops repo: GITOPS_REPO_TOKEN (chart Secret key gitops-repo-token) is required, and the bootstrap manifest requests Contents read instead of write.
* **lifecycle:** a preview label added by a user without write access to the repo (Triage role), by a bot, or by an unknown account no longer deploys the pull request; automation deploys through POST /v1/deploy.
* **api:** POST /v1/deploy no longer accepts head_ref, pr_author and pr_url; badil reads them from pull request pr_number of the service's repo.

### Features

* **api:** read the manual deploy's head branch, author and url from the pull request ([#159](https://github.com/badilhq/badil/issues/159)) ([8d7a8ed](https://github.com/badilhq/badil/commit/8d7a8ed315192cb23d44765c723f4f673d8173b0))
* **gitops:** require a gitops repo token and sign commits with an optional gpg key ([#161](https://github.com/badilhq/badil/issues/161)) ([1d76cd6](https://github.com/badilhq/badil/commit/1d76cd6e3901b111d1ab191c855747717245ced1))
* **lifecycle:** deploy a labelled pull request only when its labeller has write access ([#160](https://github.com/badilhq/badil/issues/160)) ([276887c](https://github.com/badilhq/badil/commit/276887cadd054f9a9ef523ff5441371d402bcc57))

## [0.30.2](https://github.com/badilhq/badil/compare/v0.30.1...v0.30.2) (2026-11-26)


### Bug Fixes

* **lifecycle:** keep ttl-expired previews down until a push or a relabel ([#162](https://github.com/badilhq/badil/issues/162)) ([01f1f9e](https://github.com/badilhq/badil/commit/01f1f9e205cffe005f160978488cbdf0c4f8622b))

## [0.30.1](https://github.com/badilhq/badil/compare/v0.30.0...v0.30.1) (2026-11-25)


### Bug Fixes

* **forge:** name the forge's not found and unauthorized errors after the forge ([#157](https://github.com/badilhq/badil/issues/157)) ([ebe14e5](https://github.com/badilhq/badil/commit/ebe14e54d5fe420100d2fe7be99f9c740479e227))

## [0.30.0](https://github.com/badilhq/badil/compare/v0.29.0...v0.30.0) (2026-11-25)


### ⚠ BREAKING CHANGES

* **chart:** expose only the github webhook through the ingress by default ([#153](https://github.com/badilhq/badil/issues/153))

### Features

* **chart:** expose only the github webhook through the ingress by default ([#153](https://github.com/badilhq/badil/issues/153)) ([21ab484](https://github.com/badilhq/badil/commit/21ab4848c1a508dbb64a029af05293847d27a823))


### Bug Fixes

* **deps:** build with go 1.27.4 and update vulnerable dependencies ([#155](https://github.com/badilhq/badil/issues/155)) ([1e45830](https://github.com/badilhq/badil/commit/1e458302b96e4da9c89f76c3e3b05904c4f599e6))

## [0.29.0](https://github.com/badilhq/badil/compare/v0.28.0...v0.29.0) (2026-11-24)


### ⚠ BREAKING CHANGES

* label preview objects with badil.dev/preview ([#136](https://github.com/badilhq/badil/issues/136))

### Features

* label preview objects with badil.dev/preview ([#136](https://github.com/badilhq/badil/issues/136)) ([3f3bb60](https://github.com/badilhq/badil/commit/3f3bb60ada4d31e708ecf5c124636f250ac1ce3c))

## [0.28.0](https://github.com/badilhq/badil/compare/v0.27.1...v0.28.0) (2026-11-23)


### Features

* **chart:** add an opt-in admission policy that keeps the preview renderer off namespaces badil did not create ([#132](https://github.com/badilhq/badil/issues/132)) ([48622bf](https://github.com/badilhq/badil/commit/48622bfed2743dc1f36c65570e15db08ec0dfa0e))


### Bug Fixes

* refuse a preview whose namespace badil did not create ([#130](https://github.com/badilhq/badil/issues/130)) ([7f35f8f](https://github.com/badilhq/badil/commit/7f35f8fd67bf7d7abf2f97faefb3bacd28715355))

## [0.27.1](https://github.com/badilhq/badil/compare/v0.27.0...v0.27.1) (2026-11-22)


### Documentation

* replace slogan phrasing with plain statements ([#128](https://github.com/badilhq/badil/issues/128)) ([adb64b4](https://github.com/badilhq/badil/commit/adb64b4f05074cecffdd246c1fb638a70af83e87))

## [0.27.0](https://github.com/badilhq/badil/compare/v0.26.0...v0.27.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* GITOPS_REPO is replaced by GITOPS_REPO_URL, and the chart value config.gitopsRepo by config.gitopsRepoURL.

### Features

* add a mock GitHub server for local testing ([#124](https://github.com/badilhq/badil/issues/124)) ([d7def30](https://github.com/badilhq/badil/commit/d7def305844ac585af3e20c76e89d6264f1287ac))
* configure the gitops repo by URL and token ([#120](https://github.com/badilhq/badil/issues/120)) ([6969dac](https://github.com/badilhq/badil/commit/6969dac22754ac3e560c91aaea14c3f130eb2f1a))
* make the GitHub host configurable ([#122](https://github.com/badilhq/badil/issues/122)) ([eda2481](https://github.com/badilhq/badil/commit/eda24817187dc80dc0b64e2f53190295fb092f27))

## [0.26.0](https://github.com/badilhq/badil/compare/v0.25.3...v0.26.0) (2026-11-20)


### ⚠ BREAKING CHANGES

* POST /webhooks/argocd and ARGOCD_WEBHOOK_TOKEN are replaced by POST /webhooks/status and BADIL_STATUS_TOKEN (chart secret key status-token instead of argocd-webhook-token); Argo CD notifications use badil-status-token, the badil-status template and the on-status-* triggers; badil_argocd_events_total is replaced by badil_status_events_total.

### Features

* catch up on missed pull request events after start and each sweep ([#111](https://github.com/badilhq/badil/issues/111)) ([97bfec0](https://github.com/badilhq/badil/commit/97bfec0da79edcdbba124510ae4bb244a4479ace))
* list previews and wait for a preview to be ready ([#115](https://github.com/badilhq/badil/issues/115)) ([0f59f73](https://github.com/badilhq/badil/commit/0f59f73d79ffc2a9981273ad54d5b5b4bc0d8fe7))
* read the gitops repo from a git clone ([#107](https://github.com/badilhq/badil/issues/107)) ([eae1793](https://github.com/badilhq/badil/commit/eae1793dc317b3452ada89236e57c619699d72b3))
* receive preview status from the renderer and keep it in Kubernetes ([#113](https://github.com/badilhq/badil/issues/113)) ([3ae70be](https://github.com/badilhq/badil/commit/3ae70be680af4ea3132099704efbbe08cd25fc09))
* write the gitops repo with git, one commit per operation ([#109](https://github.com/badilhq/badil/issues/109)) ([fdd8065](https://github.com/badilhq/badil/commit/fdd8065fb7ef52a526df793cc8fec6eb4fc00075))

## [0.25.3](https://github.com/badilhq/badil/compare/v0.25.2...v0.25.3) (2026-11-19)


### Documentation

* remove the quickstart page ([#103](https://github.com/badilhq/badil/issues/103)) ([25ce5e9](https://github.com/badilhq/badil/commit/25ce5e99adc779c277071df6aa73bfd99d4449b3))

## [0.25.2](https://github.com/badilhq/badil/compare/v0.25.1...v0.25.2) (2026-11-19)


### Documentation

* add a quickstart page that points to badilhq/quickstart ([#101](https://github.com/badilhq/badil/issues/101)) ([6af75a1](https://github.com/badilhq/badil/commit/6af75a1b948ce26b0ab7911d63d46121c1f30b38))

## [0.25.1](https://github.com/badilhq/badil/compare/v0.25.0...v0.25.1) (2026-11-19)


### Documentation

* name the preview contract in its links ([#98](https://github.com/badilhq/badil/issues/98)) ([5634e44](https://github.com/badilhq/badil/commit/5634e44ffccec233f9a4823e486ee8e709ff9cf5))

## [0.25.0](https://github.com/badilhq/badil/compare/v0.24.0...v0.25.0) (2026-11-18)


### Miscellaneous Chores

* release 0.25.0 ([20ec579](https://github.com/badilhq/badil/commit/20ec57965df479305ec0560dd9458515574a1b20))

## [0.24.0](https://github.com/badilhq/badil/compare/v0.23.0...v0.24.0) (2026-11-18)


### Features

* make BADIL_DEPLOY_TOKEN optional and reject paths that never match ([#92](https://github.com/badilhq/badil/issues/92)) ([20656a2](https://github.com/badilhq/badil/commit/20656a29c69a918e1696b98697bb37dd018bc2d6))

## [0.23.0](https://github.com/badilhq/badil/compare/v0.22.0...v0.23.0) (2026-11-17)


### Features

* build and sync a gitops repo template on release ([#88](https://github.com/badilhq/badil/issues/88)) ([99dffc6](https://github.com/badilhq/badil/commit/99dffc676f41d52b1b1b1e60901cc3acd3d286ac))

## [0.22.0](https://github.com/badilhq/badil/compare/v0.21.0...v0.22.0) (2026-11-17)


### ⚠ BREAKING CHANGES

* **charts:** the renderer chart is published as oci://ghcr.io/badilhq/charts/preview-apps from 0.13.0; preview-env gets no new versions.

### Features

* **charts:** rename the preview-env chart to preview-apps ([#82](https://github.com/badilhq/badil/issues/82)) ([57d0b24](https://github.com/badilhq/badil/commit/57d0b2461766cdb1d7e7b1d60853a798d1d4311f))
* **lifecycle:** pin only the monorepo services a pr changes ([#85](https://github.com/badilhq/badil/issues/85)) ([7a58718](https://github.com/badilhq/badil/commit/7a58718d12717ca1522d99cdc0aa3efea070eca2))


### Bug Fixes

* **examples:** mark unpinned services' github deployments inactive ([#86](https://github.com/badilhq/badil/issues/86)) ([9c74a2f](https://github.com/badilhq/badil/commit/9c74a2f860656f991ab6ad0c1998603c76f62604))
* **examples:** wait for post-sync health before posting success or spin-up ([#87](https://github.com/badilhq/badil/issues/87)) ([659465d](https://github.com/badilhq/badil/commit/659465d815b301964a7b4c4789b58d73868cb5af))

## [0.21.0](https://github.com/badilhq/badil/compare/v0.20.0...v0.21.0) (2026-11-16)


### ⚠ BREAKING CHANGES

* the preview file requires the root gitopsRepoURL and dependencies.
* preview-env.yaml is removed: chart settings move to badil.yaml (services.<name>.chart, root namespace and imageTag), and the preview file carries chart, imageTag and namespace. preview-env 0.11.0 reads only the preview file.

### Features

* deploy upstream helm and oci charts as preview dependencies ([#77](https://github.com/badilhq/badil/issues/77)) ([429321f](https://github.com/badilhq/badil/commit/429321f6b15fbe84084502c7173f5e15d7957e40))
* link each preview's argo cd applications from its pr comment ([#80](https://github.com/badilhq/badil/issues/80)) ([add0f2c](https://github.com/badilhq/badil/commit/add0f2c576aa3e3cdd8e5cf1efdee935baa2b929))
* move chart settings and image tags into badil.yaml ([#75](https://github.com/badilhq/badil/issues/75)) ([32df98c](https://github.com/badilhq/badil/commit/32df98cfff38a8a5b1b2afe10056bf4d76482303))
* **server:** sync every preview with badil.yaml on push and POST /v1/sync ([#79](https://github.com/badilhq/badil/issues/79)) ([d53b91d](https://github.com/badilhq/badil/commit/d53b91d4d2ad35f1c245f3396c7aa5d665c87f8b))
* trim previews to pinned services and what they need ([#78](https://github.com/badilhq/badil/issues/78)) ([406f71d](https://github.com/badilhq/badil/commit/406f71d2b930b890d15c68874f68360dad628677))

## [0.20.0](https://github.com/badilhq/badil/compare/v0.19.0...v0.20.0) (2026-11-15)


### ⚠ BREAKING CHANGES

* preview files use apiVersion badil.dev/v1beta1 instead of badil.dev/v1, and preview-env 0.10.0 accepts only badil.dev/v1beta1.

### Features

* rename the preview contract apiVersion to badil.dev/v1beta1 ([#73](https://github.com/badilhq/badil/issues/73)) ([7cb002c](https://github.com/badilhq/badil/commit/7cb002c0ceb5c73ded39c2e7e918837258f9cf3e))

## [0.19.0](https://github.com/badilhq/badil/compare/v0.18.0...v0.19.0) (2026-11-15)


### Features

* keep previews in sync with badil.yaml on deploys and sweeps ([#71](https://github.com/badilhq/badil/issues/71)) ([a77ae76](https://github.com/badilhq/badil/commit/a77ae76982e64cad4f58319ad903aac54c769295))

## [0.18.0](https://github.com/badilhq/badil/compare/v0.17.0...v0.18.0) (2026-11-15)


### ⚠ BREAKING CHANGES

* preview files from older releases fail rendering until rewritten, and the gitops repo needs badil.yaml and preview-env.yaml (upgrade steps in INSTALL).

### Features

* version the preview contract and split badil.yaml from preview-env.yaml ([#69](https://github.com/badilhq/badil/issues/69)) ([3c0466b](https://github.com/badilhq/badil/commit/3c0466b258a34c96290eca321a1b3843fddd0724))

## [0.17.0](https://github.com/badilhq/badil/compare/v0.16.0...v0.17.0) (2026-11-14)


### Features

* **examples:** post github deployment failure and error for failed previews ([#66](https://github.com/badilhq/badil/issues/66)) ([6c66fee](https://github.com/badilhq/badil/commit/6c66feeaa3042ab820daa225059f6ca59836101d))
* **preview-env:** add preview namespace labels and annotations and a kyverno limits example ([#68](https://github.com/badilhq/badil/issues/68)) ([fa4e64f](https://github.com/badilhq/badil/commit/fa4e64f0090331337fb56009e30e935200254b70))
* **preview-env:** make the argo cd namespace configurable ([#64](https://github.com/badilhq/badil/issues/64)) ([5dadc90](https://github.com/badilhq/badil/commit/5dadc90cb0af782a32c3111610279eeac2c5278f))
* refuse deploys that would create a preview past preview.maxPreviews ([#67](https://github.com/badilhq/badil/issues/67)) ([3cb8a89](https://github.com/badilhq/badil/commit/3cb8a897a9e5b0f288c7e7f19a760c3c16d70b71))

## [0.16.0](https://github.com/badilhq/badil/compare/v0.15.1...v0.16.0) (2026-11-12)


### Features

* add a version command, skip no-op deploys and count previews live ([#62](https://github.com/badilhq/badil/issues/62)) ([38259ed](https://github.com/badilhq/badil/commit/38259edd98aa8765fb82f60cc673910dea5cf75f))

## [0.15.1](https://github.com/badilhq/badil/compare/v0.15.0...v0.15.1) (2026-11-12)


### Documentation

* point changelog commit links at the current history ([#59](https://github.com/badilhq/badil/issues/59)) ([e40eb60](https://github.com/badilhq/badil/commit/e40eb6084e57e9103135169afc20de6c52d5186d))

## [0.15.0](https://github.com/badilhq/badil/compare/v0.14.0...v0.15.0) (2026-11-12)


### ⚠ BREAKING CHANGES

* rename project to badil ([#55](https://github.com/badilhq/badil/issues/55))

### Code Refactoring

* rename project to badil ([#55](https://github.com/badilhq/badil/issues/55)) ([1b6fa79](https://github.com/badilhq/badil/commit/1b6fa79e71ca3bd6348eb7bdf02bec4fd80527b6))

## [0.14.0](https://github.com/badilhq/badil/compare/v0.13.0...v0.14.0) (2026-10-30)


### Features

* post preview deployments on github from argo cd ([#53](https://github.com/badilhq/badil/issues/53)) ([1ed2890](https://github.com/badilhq/badil/commit/1ed2890759b2aeb8beb7f35fb69cad9cdc2366dc))

## [0.13.0](https://github.com/badilhq/badil/compare/v0.12.0...v0.13.0) (2026-10-29)


### Features

* add a grafana dashboard ([#51](https://github.com/badilhq/badil/issues/51)) ([a1e6ea6](https://github.com/badilhq/badil/commit/a1e6ea6abaa33777a080a6161bd51034471e2925))

## [0.12.0](https://github.com/badilhq/badil/compare/v0.11.1...v0.12.0) (2026-10-29)


### Features

* expose prometheus metrics and run the sweep in the server ([#47](https://github.com/badilhq/badil/issues/47)) ([6aeceea](https://github.com/badilhq/badil/commit/6aeceeaeb00d2ab5c5f40529870671db026438d1))
* measure preview spin-up time ([#49](https://github.com/badilhq/badil/issues/49)) ([b9695ad](https://github.com/badilhq/badil/commit/b9695ad3ab10c080da13d714fd29442fed78dc9e))

## [0.11.1](https://github.com/badilhq/badil/compare/v0.11.0...v0.11.1) (2026-10-29)


### Bug Fixes

* **charts:** fail preview-env on non-string serviceCharts values ([#44](https://github.com/badilhq/badil/issues/44)) ([67edcc1](https://github.com/badilhq/badil/commit/67edcc1eb0a2fb121999952575cc8371353e6e08))

## [0.11.0](https://github.com/badilhq/badil/compare/v0.10.0...v0.11.0) (2026-10-29)


### Features

* let each service set its chart branch, path and parameters ([#42](https://github.com/badilhq/badil/issues/42)) ([dbaabf6](https://github.com/badilhq/badil/commit/dbaabf6153ed3e0eb9e110edf4d7384d2f33157f))

## [0.10.0](https://github.com/badilhq/badil/compare/v0.9.0...v0.10.0) (2026-10-28)


### Features

* cap preview slugs and hosts to their dns limits ([#40](https://github.com/badilhq/badil/issues/40)) ([bbc2e54](https://github.com/badilhq/badil/commit/bbc2e5488b3a21191582e4a003ae7f1a67695d75))

## [0.9.0](https://github.com/badilhq/badil/compare/v0.8.1...v0.9.0) (2026-10-28)


### Features

* **cli:** create the github app with a given webhook url ([#38](https://github.com/badilhq/badil/issues/38)) ([356390a](https://github.com/badilhq/badil/commit/356390a8f99094caac3df8ee3b46823120bbdbc0))

## [0.8.1](https://github.com/badilhq/badil/compare/v0.8.0...v0.8.1) (2026-10-28)


### Bug Fixes

* **lifecycle:** skip the empty branch slug in payload teardown ([#36](https://github.com/badilhq/badil/issues/36)) ([47ebd87](https://github.com/badilhq/badil/commit/47ebd87a0bfdb0ca612013945f4bc80298f98236))

## [0.8.0](https://github.com/badilhq/badil/compare/v0.7.0...v0.8.0) (2026-10-28)


### Features

* share a preview across pull requests with named labels ([#34](https://github.com/badilhq/badil/issues/34)) ([8e8d975](https://github.com/badilhq/badil/commit/8e8d975fe14090737bd0f405644b8e2e1f6bf912))

## [0.7.0](https://github.com/badilhq/badil/compare/v0.6.0...v0.7.0) (2026-10-28)


### ⚠ BREAKING CHANGES

* create previews from a pull request label ([#31](https://github.com/badilhq/badil/issues/31))

### Features

* create previews from a pull request label ([#31](https://github.com/badilhq/badil/issues/31)) ([b84f40d](https://github.com/badilhq/badil/commit/b84f40dfeda9e27fba52076f6ecea19e92f158c0))

## [0.6.0](https://github.com/badilhq/badil/compare/v0.5.0...v0.6.0) (2026-10-27)


### Features

* add a deploy action for service ci ([#29](https://github.com/badilhq/badil/issues/29)) ([eba41ad](https://github.com/badilhq/badil/commit/eba41add6722cc21f68f57e5ea328686fa5a0798))

## [0.5.0](https://github.com/badilhq/badil/compare/v0.4.1...v0.5.0) (2026-10-27)


### ⚠ BREAKING CHANGES

* make the app chart data-agnostic ([#27](https://github.com/badilhq/badil/issues/27))

### Features

* make the app chart data-agnostic ([#27](https://github.com/badilhq/badil/issues/27)) ([a519349](https://github.com/badilhq/badil/commit/a5193497bad992c4c1ec353ff9941a3ffebd05f9))

## [0.4.1](https://github.com/badilhq/badil/compare/v0.4.0...v0.4.1) (2026-10-27)


### Bug Fixes

* run the cleanup job once per schedule ([#25](https://github.com/badilhq/badil/issues/25)) ([a6e3759](https://github.com/badilhq/badil/commit/a6e37594a921a4b3f86f3165210607417f15ff31))

## [0.4.0](https://github.com/badilhq/badil/compare/v0.3.0...v0.4.0) (2026-10-27)


### Features

* sweep stale and expired previews ([#23](https://github.com/badilhq/badil/issues/23)) ([7fb1f16](https://github.com/badilhq/badil/commit/7fb1f1690fbb51df880eb320e4cdcaf880213cb8))

## [0.3.0](https://github.com/badilhq/badil/compare/v0.2.1...v0.3.0) (2026-10-27)


### Features

* tear down previews when a pull request closes ([#21](https://github.com/badilhq/badil/issues/21)) ([9b56053](https://github.com/badilhq/badil/commit/9b5605301084b61e58436c4998c7168592985376))

## [0.2.1](https://github.com/badilhq/badil/compare/v0.2.0...v0.2.1) (2026-10-27)


### Bug Fixes

* quote preview metadata safely and replace hand-rolled helpers ([#19](https://github.com/badilhq/badil/issues/19)) ([3b220a9](https://github.com/badilhq/badil/commit/3b220a94090031c96d090f3e6b0e2deeb0fdc769))

## [0.2.0](https://github.com/badilhq/badil/compare/v0.1.1...v0.2.0) (2026-10-27)


### Features

* show per-preview urls for exposed services ([#16](https://github.com/badilhq/badil/issues/16)) ([d1e2cd8](https://github.com/badilhq/badil/commit/d1e2cd86b7d3c529c57271987c94e95bd79ee623))

## [0.1.1](https://github.com/badilhq/badil/compare/v0.1.0...v0.1.1) (2026-10-27)


### Bug Fixes

* remove internal leftovers from docs and dev defaults ([#14](https://github.com/badilhq/badil/issues/14)) ([87fe108](https://github.com/badilhq/badil/commit/87fe1087dcb458d2d7300f7ddf7a94eecfedb189))

## 0.1.0 (2026-10-27)


### Features

* **charts:** add preview-env, app and badil helm charts ([e90978d](https://github.com/badilhq/badil/commit/e90978de29d9e625363185bdec6d553eba07191d))
* **cli:** add serve, verify-app and bootstrap-app commands ([ee205ee](https://github.com/badilhq/badil/commit/ee205ee40b5bf39d74bc975ebc4bf571f0bcf6f5))
* **config:** load configuration from the environment ([f63c2f9](https://github.com/badilhq/badil/commit/f63c2f93ad11f7e11625d3d9ed160016990e95cf))
* **github:** authenticate as a github app ([eeef246](https://github.com/badilhq/badil/commit/eeef246d72867dd04440465cdb654a4404a1e5be))
* **gitops:** write preview values with optimistic locking ([792087c](https://github.com/badilhq/badil/commit/792087c24f94e8cc5b5a2206aa67ae29bd8ca5cf))
* **preview:** compute preview slugs, values and pr summaries ([10399ad](https://github.com/badilhq/badil/commit/10399ad355ca16bd880eb64c9167a61767a027a2))
* **server:** serve the deploy endpoint and github webhooks ([d0591ef](https://github.com/badilhq/badil/commit/d0591ef345bcac48a61aae8bc524ce284cd3586d))
