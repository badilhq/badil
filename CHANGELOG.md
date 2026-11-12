# Changelog

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
