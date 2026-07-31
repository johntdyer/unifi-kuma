# Changelog

## [4.2.0](https://github.com/johntdyer/unifi-kuma/compare/v4.1.1...v4.2.0) (2026-07-31)


### Features

* remove stale Ungrouped monitor once a device gains a real group ([#31](https://github.com/johntdyer/unifi-kuma/issues/31)) ([baedb6d](https://github.com/johntdyer/unifi-kuma/commit/baedb6da1c35dc5875cfcc72e148994a01f6b1b2))

## [4.1.1](https://github.com/johntdyer/unifi-kuma/compare/v4.1.0...v4.1.1) (2026-07-31)


### Bug Fixes

* prevent duplicate monitor creation within a sync cycle ([#29](https://github.com/johntdyer/unifi-kuma/issues/29)) ([c3372b6](https://github.com/johntdyer/unifi-kuma/commit/c3372b60b2222175f4ca15174cb1d918dce557a6))

## [4.1.0](https://github.com/johntdyer/unifi-kuma/compare/v4.0.2...v4.1.0) (2026-07-31)


### Features

* require a prefix for Kuma-destination UniFi groups ([#27](https://github.com/johntdyer/unifi-kuma/issues/27)) ([d5fa30b](https://github.com/johntdyer/unifi-kuma/commit/d5fa30bf1f7359f250aed8d9acd2189b72446909))


### Bug Fixes

* set a non-null timeout on ping monitors ([#26](https://github.com/johntdyer/unifi-kuma/issues/26)) ([ecc6302](https://github.com/johntdyer/unifi-kuma/commit/ecc6302b73eede80ea9ffb454a208f369d9cdb84))

## [4.0.2](https://github.com/johntdyer/unifi-kuma/compare/v4.0.1...v4.0.2) (2026-07-31)


### Bug Fixes

* set a positive interval on group monitors ([#24](https://github.com/johntdyer/unifi-kuma/issues/24)) ([c8c9c6f](https://github.com/johntdyer/unifi-kuma/commit/c8c9c6f9b60f3cfa38bcdbaffbd2d468040bc8f5))

## [4.0.1](https://github.com/johntdyer/unifi-kuma/compare/v4.0.0...v4.0.1) (2026-07-31)


### Bug Fixes

* fall back to last_ip for clients not currently connected ([#22](https://github.com/johntdyer/unifi-kuma/issues/22)) ([86cb43f](https://github.com/johntdyer/unifi-kuma/commit/86cb43f5482fc0d7d1fd9094fc35ce483af1e337))

## [4.0.0](https://github.com/johntdyer/unifi-kuma/compare/v3.0.0...v4.0.0) (2026-07-31)


### ⚠ BREAKING CHANGES

* sync from UniFi Groups instead of tags ([#20](https://github.com/johntdyer/unifi-kuma/issues/20))

### Features

* sync from UniFi Groups instead of tags ([#20](https://github.com/johntdyer/unifi-kuma/issues/20)) ([a3cf974](https://github.com/johntdyer/unifi-kuma/commit/a3cf974531a51af9d5637c406b7d6b813625934c))

## [3.0.0](https://github.com/johntdyer/unifi-kuma/compare/v2.0.0...v3.0.0) (2026-07-30)


### ⚠ BREAKING CHANGES

* remove UniFi API key auth, username+password only ([#18](https://github.com/johntdyer/unifi-kuma/issues/18))

### Bug Fixes

* remove UniFi API key auth, username+password only ([#18](https://github.com/johntdyer/unifi-kuma/issues/18)) ([c3dfc84](https://github.com/johntdyer/unifi-kuma/commit/c3dfc84df94b9c5a56672528259c1fd7fb1ee8ba))

## [2.0.0](https://github.com/johntdyer/unifi-kuma/compare/v1.2.2...v2.0.0) (2026-07-30)


### ⚠ BREAKING CHANGES

* replace fictional Kuma REST client with a real Socket.IO client ([#16](https://github.com/johntdyer/unifi-kuma/issues/16))

### Bug Fixes

* replace fictional Kuma REST client with a real Socket.IO client ([#16](https://github.com/johntdyer/unifi-kuma/issues/16)) ([7f9475e](https://github.com/johntdyer/unifi-kuma/commit/7f9475e508f6d3be2cde08a47cc2d473103ab502))

## [1.2.2](https://github.com/johntdyer/unifi-kuma/compare/v1.2.1...v1.2.2) (2026-07-30)


### Bug Fixes

* include full request URL in error messages ([#14](https://github.com/johntdyer/unifi-kuma/issues/14)) ([d3f8a15](https://github.com/johntdyer/unifi-kuma/commit/d3f8a1565d554b5827c4231b8d12cf24f59105d3))

## [1.2.1](https://github.com/johntdyer/unifi-kuma/compare/v1.2.0...v1.2.1) (2026-07-30)


### Bug Fixes

* Update readme ([#12](https://github.com/johntdyer/unifi-kuma/issues/12)) ([dc9b56d](https://github.com/johntdyer/unifi-kuma/commit/dc9b56dd71d63153a4bd90b43919761e90dd7e46))

## [1.2.0](https://github.com/johntdyer/unifi-kuma/compare/v1.1.1...v1.2.0) (2026-07-30)


### Features

* support Uptime Kuma instances with auth disabled ([#8](https://github.com/johntdyer/unifi-kuma/issues/8)) ([6bb8aca](https://github.com/johntdyer/unifi-kuma/commit/6bb8acab5729681e13a0fbe99553c8b0474be52a))

## [1.1.1](https://github.com/johntdyer/unifi-kuma/compare/v1.1.0...v1.1.1) (2026-07-30)


### Bug Fixes

* Update readme to show that u/p is not required when using api ke… ([#6](https://github.com/johntdyer/unifi-kuma/issues/6)) ([854aea6](https://github.com/johntdyer/unifi-kuma/commit/854aea662f5c91d13ce072ab94fda543886db023))

## [1.1.0](https://github.com/johntdyer/unifi-kuma/compare/v1.0.0...v1.1.0) (2026-07-29)


### Features

* add API key authentication for UniFi and Uptime Kuma ([#4](https://github.com/johntdyer/unifi-kuma/issues/4)) ([b04b4a1](https://github.com/johntdyer/unifi-kuma/commit/b04b4a1fb2ced3c4d93e5fa0fa13c630e09726d1))

## 1.0.0 (2026-07-24)


### Features

* sync UniFi Network tags to Uptime Kuma monitors ([6f083b1](https://github.com/johntdyer/unifi-kuma/commit/6f083b12e43e7e0c1aad49b0381b016623b62182))
