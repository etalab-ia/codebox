# Changelog

## [0.3.0](https://github.com/etalab-ia/codebox/compare/codebox-server-v0.2.0...codebox-server-v0.3.0) (2026-11-16)


### Features

* **secrets-gateway:** add stateless ProConnect-gated secret resolution service ([f45c624](https://github.com/etalab-ia/codebox/commit/f45c624e22047a0dfb592cd3121a84e505c32b91))
* **secrets-gateway:** add stateless ProConnect-gated secret resolution service ([323f62d](https://github.com/etalab-ia/codebox/commit/323f62d606789ac9beefd053a669b118279cdce7))
* **secrets:** add manifest v7 and unified CLI project-secret resolution ([aa56499](https://github.com/etalab-ia/codebox/commit/aa56499e64d4c3f1d370f03b0c9d43ad36556329))
* **secrets:** add zero-console CLI commands and gateway resolver ([98c171a](https://github.com/etalab-ia/codebox/commit/98c171a87e6d5c850b7961112020b71b60093355))


### Bug Fixes

* **secrets-gateway:** bind ACL writes to authorization revision and canonicalize tokens ([b2d0ff0](https://github.com/etalab-ia/codebox/commit/b2d0ff07a202598677571f3d89fa866cbd73c1b3))
* **secrets-gateway:** bind UserInfo to the ID-token subject, page versions ([9063f1a](https://github.com/etalab-ia/codebox/commit/9063f1ae67c24008f0c4e335030b17a50ff7f483))
* **secrets-gateway:** close ACL revision race ([e786709](https://github.com/etalab-ia/codebox/commit/e7867097638fee1216152e29faa1818323691adc))
* **secrets-gateway:** correct the ProConnect issuer and accept signed UserInfo ([72e7b62](https://github.com/etalab-ia/codebox/commit/72e7b622ee0c6f4b3adffe95ed3b543a81bf81f5))
* **secrets-gateway:** give each terminal ticket a unique jti ([0193527](https://github.com/etalab-ia/codebox/commit/0193527f15318fc0b8e27ef5dbf7e2f2e946f804))
* **secrets-gateway:** keep projects writable after retraction, single-use tickets ([3f8eedd](https://github.com/etalab-ia/codebox/commit/3f8eedd27d9c7c6548419f69ae681430d54eded1))
* **secrets-gateway:** redirect the browser, atomic writes, audience and flow fixes ([ecb4b83](https://github.com/etalab-ia/codebox/commit/ecb4b83bcbd8833fb01d771ac900212b5ac708a0))
* **secrets-gateway:** reject lossy JSON before decoding request fields ([21aea5f](https://github.com/etalab-ia/codebox/commit/21aea5f7cbc7193fa8fc103e27e94d812662e0ce))
* **secrets:** bind loopback login and gateway redirects ([7dd96a2](https://github.com/etalab-ia/codebox/commit/7dd96a2d4c8532f81c430b68b97b9a450d116361))


### Miscellaneous Chores

* **main:** release secrets-gateway 0.2.0 ([ea9414d](https://github.com/etalab-ia/codebox/commit/ea9414d37f778dfa51ac72396b87c3474d9d0c3f))
* **main:** release secrets-gateway 0.2.0 ([0227dad](https://github.com/etalab-ia/codebox/commit/0227dade932df2d75de44471aa38469c3c357ebf))

## [0.2.0](https://github.com/etalab-ia/codebox/compare/secrets-gateway-v0.1.0...secrets-gateway-v0.2.0) (2026-11-15)


### Features

* **secrets-gateway:** add stateless ProConnect-gated secret resolution service ([f45c624](https://github.com/etalab-ia/codebox/commit/f45c624e22047a0dfb592cd3121a84e505c32b91))
* **secrets-gateway:** add stateless ProConnect-gated secret resolution service ([323f62d](https://github.com/etalab-ia/codebox/commit/323f62d606789ac9beefd053a669b118279cdce7))
* **secrets:** add manifest v7 and unified CLI project-secret resolution ([aa56499](https://github.com/etalab-ia/codebox/commit/aa56499e64d4c3f1d370f03b0c9d43ad36556329))
* **secrets:** add zero-console CLI commands and gateway resolver ([98c171a](https://github.com/etalab-ia/codebox/commit/98c171a87e6d5c850b7961112020b71b60093355))


### Bug Fixes

* **secrets-gateway:** bind ACL writes to authorization revision and canonicalize tokens ([b2d0ff0](https://github.com/etalab-ia/codebox/commit/b2d0ff07a202598677571f3d89fa866cbd73c1b3))
* **secrets-gateway:** bind UserInfo to the ID-token subject, page versions ([9063f1a](https://github.com/etalab-ia/codebox/commit/9063f1ae67c24008f0c4e335030b17a50ff7f483))
* **secrets-gateway:** close ACL revision race ([e786709](https://github.com/etalab-ia/codebox/commit/e7867097638fee1216152e29faa1818323691adc))
* **secrets-gateway:** correct the ProConnect issuer and accept signed UserInfo ([72e7b62](https://github.com/etalab-ia/codebox/commit/72e7b622ee0c6f4b3adffe95ed3b543a81bf81f5))
* **secrets-gateway:** give each terminal ticket a unique jti ([0193527](https://github.com/etalab-ia/codebox/commit/0193527f15318fc0b8e27ef5dbf7e2f2e946f804))
* **secrets-gateway:** keep projects writable after retraction, single-use tickets ([3f8eedd](https://github.com/etalab-ia/codebox/commit/3f8eedd27d9c7c6548419f69ae681430d54eded1))
* **secrets-gateway:** redirect the browser, atomic writes, audience and flow fixes ([ecb4b83](https://github.com/etalab-ia/codebox/commit/ecb4b83bcbd8833fb01d771ac900212b5ac708a0))
* **secrets-gateway:** reject lossy JSON before decoding request fields ([21aea5f](https://github.com/etalab-ia/codebox/commit/21aea5f7cbc7193fa8fc103e27e94d812662e0ce))
* **secrets:** bind loopback login and gateway redirects ([7dd96a2](https://github.com/etalab-ia/codebox/commit/7dd96a2d4c8532f81c430b68b97b9a450d116361))
