# Changelog

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
