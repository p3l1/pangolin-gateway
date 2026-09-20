# Changelog

## [0.4.0](https://github.com/p3l1/pangolin-gateway/compare/v0.3.0...v0.4.0) (2026-09-20)


### ⚠ BREAKING CHANGES

* pangolin_gateway_prune_total, pangolin_gateway_prune_skipped_total and pangolin_gateway_resources_desired gained a "visibility" label, so the unlabelled series they exposed before are gone. A dashboard or alert selecting them by name alone keeps working; one that matched an exact label set needs the new label summed away, for example sum without (visibility) (pangolin_gateway_resources_desired).

### Features

* let a route carve unauthenticated paths out of SSO ([#16](https://github.com/p3l1/pangolin-gateway/issues/16)) ([406803e](https://github.com/p3l1/pangolin-gateway/commit/406803e4af30cc195c1b814f25fbd6a3e272d7f1))
* publish a route as a private resource ([#18](https://github.com/p3l1/pangolin-gateway/issues/18)) ([fc68885](https://github.com/p3l1/pangolin-gateway/commit/fc6888588cc3f7107fb1734b71319eab11498e4c))


### Bug Fixes

* **ci:** keep a breaking change below 1.0.0 ([#19](https://github.com/p3l1/pangolin-gateway/issues/19)) ([94f57fe](https://github.com/p3l1/pangolin-gateway/commit/94f57fe8ffe8280fc7164d1799a33bdeddff4c50))

## [0.3.0](https://github.com/p3l1/pangolin-gateway/compare/v0.2.0...v0.3.0) (2026-09-19)


### Features

* let a route name the resource Pangolin shows ([#14](https://github.com/p3l1/pangolin-gateway/issues/14)) ([683eb6a](https://github.com/p3l1/pangolin-gateway/commit/683eb6a7e272ef6163df028ea4f7fa630075e9a1))

## [0.2.0](https://github.com/p3l1/pangolin-gateway/compare/v0.1.0...v0.2.0) (2026-09-19)


### Features

* publish a healthcheck when the route annotates a path ([#13](https://github.com/p3l1/pangolin-gateway/issues/13)) ([df438ec](https://github.com/p3l1/pangolin-gateway/commit/df438ece7268e72d1ba0cf5067099a83b2121c26))


### Bug Fixes

* **ci:** give gh a repository when dispatching the release ([#11](https://github.com/p3l1/pangolin-gateway/issues/11)) ([7f4fc35](https://github.com/p3l1/pangolin-gateway/commit/7f4fc35124f755ed57253c71cc2ae9e6b3044301))

## 0.1.0 (2026-09-19)


### Features

* add the Helm chart ([afd17c6](https://github.com/p3l1/pangolin-gateway/commit/afd17c660f1c9edf29b5df7de44ba66c5c20086c))
* add the Pangolin Integration API client ([7baf293](https://github.com/p3l1/pangolin-gateway/commit/7baf29379797ec712bc11ee1c95ce37b43b57de2))
* mark status as a rehearsal under --dry-run ([03db346](https://github.com/p3l1/pangolin-gateway/commit/03db346826329467fb95157823edcb6a031a3e83))
* reconcile Gateway API objects into Pangolin ([f9ef9ae](https://github.com/p3l1/pangolin-gateway/commit/f9ef9aea5619f923e917387b366f2880861b4725))
* render HTTPRoutes into a Pangolin blueprint ([fab213d](https://github.com/p3l1/pangolin-gateway/commit/fab213d363df23f1f1be047fd71a812a1be80eaf))


### Bug Fixes

* **chart:** stop fullname from repeating the chart name ([783f68f](https://github.com/p3l1/pangolin-gateway/commit/783f68f83559149469313ecaaf7d17ed955432da))
* **ci:** stop a title edit from cancelling the code checks ([#9](https://github.com/p3l1/pangolin-gateway/issues/9)) ([d9aa72b](https://github.com/p3l1/pangolin-gateway/commit/d9aa72b06a1cf8f5628aba79d7e54f777ebbdac2))
* **ci:** stop e2e racing k3s for the Gateway API CRDs ([#6](https://github.com/p3l1/pangolin-gateway/issues/6)) ([1f5e628](https://github.com/p3l1/pangolin-gateway/commit/1f5e628c47fac2be601517c6f631e7b54120ba78))
* **justfile:** repair the hook path and make e2e runnable locally ([5b65ff2](https://github.com/p3l1/pangolin-gateway/commit/5b65ff213e7a61b95e259bbe783733c9f89a2d46))
