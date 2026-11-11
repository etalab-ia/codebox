# Changelog

## [0.17.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.16.0...codebox-v0.17.0) (2026-11-11)


### ⚠ BREAKING CHANGES

* rename just-code to codebox across module, CLI, paths and releases

### Features

* add recovery path for upgraded stopped Microsandbox instances ([7f9a41e](https://github.com/etalab-ia/codebox/commit/7f9a41e2b68689248d404b2ccafc020cbaee866e))
* add recovery path for upgraded stopped Microsandbox instances ([2ed179c](https://github.com/etalab-ia/codebox/commit/2ed179c780f01e0ff60f6d0b6a65263d5e0d62c1))
* **harness:** support Pi as a second agent harness ([#176](https://github.com/etalab-ia/codebox/issues/176)) ([3c8e4eb](https://github.com/etalab-ia/codebox/commit/3c8e4eb23c94b2147e79e08e6a0ce18d51894cc8))
* **harness:** support Pi as a second agent harness ([#176](https://github.com/etalab-ia/codebox/issues/176)) ([9db5b28](https://github.com/etalab-ia/codebox/commit/9db5b285f74654a5b5ecbbd00773beae661a7a96))
* **init:** drop the models Pi cannot run reliably ([b24173f](https://github.com/etalab-ia/codebox/commit/b24173fd33d7a1ad268a8292bc03353cac707962))
* rename just-code to codebox across module, CLI, paths and releases ([1390083](https://github.com/etalab-ia/codebox/commit/13900837c55f089b0db7bfe87777fb4f63883db6))
* **stats:** post usage events to a Grist table without a credential ([a753acd](https://github.com/etalab-ia/codebox/commit/a753acde641705205fbaca8eaea78210bc65b56b))
* **stats:** post usage events to a Grist table without a credential ([5d6cff5](https://github.com/etalab-ia/codebox/commit/5d6cff58ebcc8b93173602ce1ac499f4b324bd72))


### Bug Fixes

* align phase-3 temp-file probe with the frozen publisher and allowlist the new header digest ([9b3cf7f](https://github.com/etalab-ia/codebox/commit/9b3cf7f7dc804742603349272f8516e6fe2e01fe))
* gofmt microsandbox_test.go fakeMSBClient struct alignment ([6b8cc4d](https://github.com/etalab-ia/codebox/commit/6b8cc4dba89e77e9ea5594d0d648386d24ea3fa3))
* **harness:** classify the agent harness as creation-fixed ([7af02a7](https://github.com/etalab-ia/codebox/commit/7af02a742697bd1033eefa3048940dd019db981a)), closes [#176](https://github.com/etalab-ia/codebox/issues/176)
* **harness:** correct the Pi model wiring, the browser profile gate and the harness guard ([20a2f84](https://github.com/etalab-ia/codebox/commit/20a2f84f6317ecb53749c0313b98e94f15f3832a))
* **init:** correct the obsolete init intro and launch-time flow ([5c271f5](https://github.com/etalab-ia/codebox/commit/5c271f568ca9ef4d7c0af5187dc4eecc43ec392b))
* **init:** correct the obsolete init intro and launch-time flow ([a4034cd](https://github.com/etalab-ia/codebox/commit/a4034cd44cad2b498e86009d07d5a4f6071439e9))
* **microsandbox:** read secrets from where the runtime stores them, and check provider endpoints ([222fb8d](https://github.com/etalab-ia/codebox/commit/222fb8d7f4e6ea18278d0b2dd82bd38f83286030))
* **microsandbox:** recover the bindings persisted on the stopped sandbox ([f733409](https://github.com/etalab-ia/codebox/commit/f733409305db5c1b4f5094e69780681523bbba63))
* **microsandbox:** resolve persisted project-secret bindings during recovery ([32b7df0](https://github.com/etalab-ia/codebox/commit/32b7df0dcf6ea1615a32e76c8045f584a4b721a4))
* **microsandbox:** satisfy every persisted registration at recovery boot ([3c571b3](https://github.com/etalab-ia/codebox/commit/3c571b371c1663d822791a8482677f627c6cf259))
* **microsandbox:** validate persisted project-secret metadata before publishing ([c0a4ed1](https://github.com/etalab-ia/codebox/commit/c0a4ed1109ff01c48908a2577b8df35ec2b3a6c1))
* **opencode:** default fresh projects to DeepSeek V4 Flash ([9c7759c](https://github.com/etalab-ia/codebox/commit/9c7759c561517cfd288fd142fdd8602ff691e456))
* **opencode:** default fresh projects to DeepSeek V4 Flash ([9547317](https://github.com/etalab-ia/codebox/commit/95473170c663ce51a4ad525f2eb6688a670e8b56))
* **pi:** install into a user-owned prefix and gate readiness on the configuration ([e7d7985](https://github.com/etalab-ia/codebox/commit/e7d7985918b0a0875a27315216d2c9b21188d012))
* **pi:** pin declared packages by content and write the model Pi launches ([4935284](https://github.com/etalab-ia/codebox/commit/49352842097fb27d5b658f7b121e63a9e8d745f5))
* **pi:** refuse a start whose managed configuration cannot be composed ([5a3817e](https://github.com/etalab-ia/codebox/commit/5a3817efc1e39060b185c83db19e704c882b96a0))
* **reconcile:** restart a Pi guest prepared from an older configuration ([e49a292](https://github.com/etalab-ia/codebox/commit/e49a2927fb5dca36751bbca4bbb89c78ce69014e))
* **recover:** publish host secrets when booting a stopped sandbox ([0ed4d33](https://github.com/etalab-ia/codebox/commit/0ed4d337413e6dea561d267e9b49b81609884a6e))
* **skills:** retry transient-empty OpenCode skill discovery ([466deb4](https://github.com/etalab-ia/codebox/commit/466deb4043ee20f5fa0358d0bf32268d9977b334))
* **skills:** retry transient-empty OpenCode skill discovery ([dc6fdd7](https://github.com/etalab-ia/codebox/commit/dc6fdd71cbfbc66062cfd025084bc86672ded3d3))
* **stats:** identify the duplicate guard by Grist's rule type, not a memo ([b86583d](https://github.com/etalab-ia/codebox/commit/b86583da0d8e54ced7da812737d133a9de9fa8ed))
* **stats:** keep events when Grist rejects the request shape ([2e73ac4](https://github.com/etalab-ia/codebox/commit/2e73ac4e495ba58a1fe81d37a24835d179d33fcd))
* **stats:** read consent per request, match Grist's real rejection ([f33c1db](https://github.com/etalab-ia/codebox/commit/f33c1db4c8a9ea82418950f1f013d5a80b12227d))
* **stats:** refuse a redirect that leaves a secure endpoint ([3c41abe](https://github.com/etalab-ia/codebox/commit/3c41abe5a6793b197f55f7f927a1adcf9da8b57e))
* **stats:** refuse an insecure endpoint instead of falling back ([38a7d3a](https://github.com/etalab-ia/codebox/commit/38a7d3abf6aac1b84cd8b3e4682fd59b6653fece))
* **stats:** require the duplicate memo to treat a 403 as already stored ([710694e](https://github.com/etalab-ia/codebox/commit/710694e81a0e6843a0ec065879a2e9f0e015ff7c))
* **test:** assert Recover via the runtime instance name ([af95bd9](https://github.com/etalab-ia/codebox/commit/af95bd9a5edea877af481717f94ab8b6c1b8093d))
* **trust:** approve the files Pi settings declare as extensions ([2a9a66f](https://github.com/etalab-ia/codebox/commit/2a9a66fb68b23bdfdcce69baa629ca0e22ce32ed))
* **trust:** gate Pi project extensions and settings before launch ([efe5a5c](https://github.com/etalab-ia/codebox/commit/efe5a5c05087ce71744b6883a6b7d807b36968cd))
* **ui:** align choice-screen wording in setup and init ([11592f0](https://github.com/etalab-ia/codebox/commit/11592f08ccf83cfae4d62e90201ccd312998ab12))
* **ui:** align choice-screen wording in setup and init ([4d0d19f](https://github.com/etalab-ia/codebox/commit/4d0d19fa5ee964f496e357c453ca10a4a6a3161a))
* **ui:** French wording for confirmations, seconds and setup intro ([d64fe3f](https://github.com/etalab-ia/codebox/commit/d64fe3f2159e4b45bd0d585a174f2b3a71879e85))
* **ui:** French wording for confirmations, seconds and setup intro ([cce771c](https://github.com/etalab-ia/codebox/commit/cce771c507332a31e5badfa4871964fd744d91b8))
* **ui:** keep the models-screen marker on the user-selected default ([c94e33e](https://github.com/etalab-ia/codebox/commit/c94e33e3403cf7c88343e2db8a7c094003c85e74))
* update guest-secret header pin and phase-3 kit marker assertion for the rename ([80e00df](https://github.com/etalab-ia/codebox/commit/80e00df7bfb10e25ddd4f12c47b16e913db83dab))

## [0.16.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.15.0...codebox-v0.16.0) (2026-11-06)


### Features

* **init:** apply wizard answers directly without a review screen ([#333](https://github.com/etalab-ia/codebox/issues/333)) ([b391416](https://github.com/etalab-ia/codebox/commit/b391416e57a357c673de4938e2cb88f0d1cc850b))
* **init:** five-step wizard, no review screen ([#333](https://github.com/etalab-ia/codebox/issues/333)) ([2a806b3](https://github.com/etalab-ia/codebox/commit/2a806b337f65eae27a5d967746fa0c2f96fa9be7))
* **setup:** apply configuration directly without a review screen ([#333](https://github.com/etalab-ia/codebox/issues/333)) ([983a92f](https://github.com/etalab-ia/codebox/commit/983a92ffe036ee0dff1b589abd3f243dc6a6221c))


### Bug Fixes

* **init:** cancel the line wizard when input closes ([#333](https://github.com/etalab-ia/codebox/issues/333)) ([927cfb5](https://github.com/etalab-ia/codebox/commit/927cfb5e307da1901e6ab80c1e5c963468c6a03a))
* **init:** finish French texts and simplify beginner recaps ([eb61497](https://github.com/etalab-ia/codebox/commit/eb61497553193ea143927cfa3ad6bf1905f0cf63))
* **init:** finish French texts and simplify beginner recaps ([1c0adff](https://github.com/etalab-ia/codebox/commit/1c0adff3db79a131e1e7db83c23b54598e2c18f2))
* **workspace:** backdate restored index after recovery refresh ([75a0951](https://github.com/etalab-ia/codebox/commit/75a0951c0e10e7fde7d6fb6cc93b44ed58b98ee6))
* **workspace:** backdate restored index after recovery refresh ([d9b8970](https://github.com/etalab-ia/codebox/commit/d9b89709643be6aa50fed089586c4465962ff1d6))

## [0.15.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.14.1...codebox-v0.15.0) (2026-11-05)


### Features

* **opencode:** migrate to OpenCode v2.2.5 ([4b364ef](https://github.com/etalab-ia/codebox/commit/4b364ef9e9b97976af58791e1ffd8f6bf8a25e3b))
* **opencode:** migrer vers OpenCode v2.2.5 ([e11ca6d](https://github.com/etalab-ia/codebox/commit/e11ca6d6618ea4b1eab38662e1cbf93109201908))


### Bug Fixes

* **exit:** distinguish fetched refs from guest pushes ([cae76de](https://github.com/etalab-ia/codebox/commit/cae76de31fb887d4f387abcc9a53e277f7357685))
* **exit:** ignore sealed bootstrap branch without origin ([45b7bf3](https://github.com/etalab-ia/codebox/commit/45b7bf3efe5775fac0035b71084d8bb9e3973d42))
* **exit:** ignore sealed bootstrap branch without origin ([58d9eeb](https://github.com/etalab-ia/codebox/commit/58d9eeb266d80fc4896dc916fcffdcbb1c745647))
* **exit:** report the branch actually pushed and its PR state ([9b34849](https://github.com/etalab-ia/codebox/commit/9b348499c39b1b1a742e41dc601f6065c6afdcb2))
* **exit:** report the branch actually pushed and its PR state ([d2cd2d4](https://github.com/etalab-ia/codebox/commit/d2cd2d4985584db91d14c50069bd1aadf0f7a390))
* **exit:** report the branch actually pushed and its PR state ([4472d9d](https://github.com/etalab-ia/codebox/commit/4472d9d4bda9d6ee11b3a4e02801d0b05d486bd5))
* **exit:** select the actual pushed work branch ([fac4353](https://github.com/etalab-ia/codebox/commit/fac435324f0dd63d31a9883a85328d757c93f539))
* **exit:** select the actual pushed work branch ([8914c5f](https://github.com/etalab-ia/codebox/commit/8914c5fde6087f02e9a51709c17a064d4aaf3e8a))
* **github:** allow uninitialized guest policy probes ([67b9310](https://github.com/etalab-ia/codebox/commit/67b9310aa9b6eb25d47b6c49a8d593ccd3c83220))
* **github:** block unapproved snapshot publication ([a4cff51](https://github.com/etalab-ia/codebox/commit/a4cff51d92a98fd13f69eb08ffb48b05d6a30f1d))
* **github:** block unapproved snapshot publication ([8f0f238](https://github.com/etalab-ia/codebox/commit/8f0f238e19e6f4294d539e213d1e0bb7fb7b6708))
* **github:** enforce policy without resolving credentials ([5951dfe](https://github.com/etalab-ia/codebox/commit/5951dfecfeaa01465013c49e021405679a2cdc0f))
* **github:** guard refreshed and healthy guest publication ([515a35c](https://github.com/etalab-ia/codebox/commit/515a35c3538a61a26577ad09158c7557f3bb2037))
* **init:** align beginner intro and key hints with QA ([bedd675](https://github.com/etalab-ia/codebox/commit/bedd675943e70e7a3cbc3d18ad6216de14cd7d2e))
* **init:** attribute form abandons to the actual step ([24f4419](https://github.com/etalab-ia/codebox/commit/24f44192c8e5762c423abe53027efe839e5ba950))
* **init:** attribute form abandons to the actual step ([f0d6f6f](https://github.com/etalab-ia/codebox/commit/f0d6f6f847ebcf0fc7a74fecf81fa98d0253471b))
* **init:** confirm an empty skills selection before the MCP step ([7c69970](https://github.com/etalab-ia/codebox/commit/7c69970409a6887ca2c19b5315fbee99472e799a))
* **init:** confirm an empty skills selection before the MCP step ([5d145e7](https://github.com/etalab-ia/codebox/commit/5d145e7c5b7d8c699835b79d2ed5bbd8eaab5eb4))
* **init:** confirm an empty skills selection before the MCP step ([f0b1bb2](https://github.com/etalab-ia/codebox/commit/f0b1bb2c13fb5e8bd00d26af9697e763fcddb139))
* **init:** confirm empty skills once across MCP retries ([f952d67](https://github.com/etalab-ia/codebox/commit/f952d676cdc5d9840b21dd498dd672d27e986c20))
* **init:** confirm empty skills once across MCP retries ([74606be](https://github.com/etalab-ia/codebox/commit/74606bee866b642b2fcaba4676c68f724e375035))
* **init:** finish French wording and unify step counters ([348044d](https://github.com/etalab-ia/codebox/commit/348044dd32e61ceb9086bbe5f00ca282644d1103))
* **init:** finish French wording and unify step counters ([9df901d](https://github.com/etalab-ia/codebox/commit/9df901d44a5666ff7552c524c579054c1bf62447))
* **init:** restore beginner step counters ([7de71d8](https://github.com/etalab-ia/codebox/commit/7de71d845e6e1823e6c477f0280fdd82a86080a9))
* **init:** show the French MCP screen in the huh wizard ([4dfcc58](https://github.com/etalab-ia/codebox/commit/4dfcc58127352a2a47dcbcafba299d9cb790384e))
* **init:** show the French MCP screen in the huh wizard ([27aa3f0](https://github.com/etalab-ia/codebox/commit/27aa3f00464c35409885f2d54610c02c4eb13d3c))
* **providers:** keep fixed-endpoint failures retryable ([e68f470](https://github.com/etalab-ia/codebox/commit/e68f470f73a696f12ecd5833730d26b8de5f4f06))
* **providers:** tell an incompatible URL apart from a network failure ([45f2755](https://github.com/etalab-ia/codebox/commit/45f2755866ccd8ca34e8120cdb249e330afab672))
* **providers:** tell an incompatible URL apart from a network failure ([8ecd03d](https://github.com/etalab-ia/codebox/commit/8ecd03d003c9096aa4b0a3aa9c5a354b20fd806d))
* **setup:** align stored Albert credential priority ([73fff36](https://github.com/etalab-ia/codebox/commit/73fff3656f3fc04bc196e3d17912c5249d8c4b98))
* **setup:** align stored Albert credential priority ([021a07e](https://github.com/etalab-ia/codebox/commit/021a07e88ddcd40374c83f445df1f6343e3db077))
* **setup:** let a resumed setup revisit its platform selection ([c975ed0](https://github.com/etalab-ia/codebox/commit/c975ed007efb9d4807abd44fd6e88b03bc1cf28a))
* **setup:** let a resumed setup revisit its platform selection ([e6aa788](https://github.com/etalab-ia/codebox/commit/e6aa788219f0467af32dc55b288fd881cca37eb7))
* **setup:** let a resumed setup revisit its platform selection ([4584754](https://github.com/etalab-ia/codebox/commit/4584754d19565e43516a64da89fb34675ecd077c))
* **setup:** offer the ALBERT_API_KEY found in the environment ([76a43eb](https://github.com/etalab-ia/codebox/commit/76a43ebd322874e5e3904ea40f60a9b03822a95f))
* **setup:** offer the ALBERT_API_KEY found in the environment ([2491ffe](https://github.com/etalab-ia/codebox/commit/2491ffee8670c3f471aac5999b321310cf2ff97d))
* **setup:** offer the ALBERT_API_KEY found in the environment ([d2b58d3](https://github.com/etalab-ia/codebox/commit/d2b58d33c893b3425107cc4f85da71b277c092b6))
* **setup:** preserve unmanaged model defaults ([2aae325](https://github.com/etalab-ia/codebox/commit/2aae3255d14b4d68a1d1c2aa32956febd21c0011))
* **setup:** reconcile models after platform changes ([14f0aa5](https://github.com/etalab-ia/codebox/commit/14f0aa51273d33f58014f2b8342a74930cc0e060))
* **setup:** reconcile models after platform changes ([2e45512](https://github.com/etalab-ia/codebox/commit/2e45512a57fac944e4041e4f423b2105fd276c09))
* **setup:** reconcile resumed models after text input ([9599fa5](https://github.com/etalab-ia/codebox/commit/9599fa598e18a2b4ab05daa3ee08b235576c7c2f))
* **setup:** reconcile resumed models after text input ([4a7f3a9](https://github.com/etalab-ia/codebox/commit/4a7f3a9a04a6e5beed4f12e0ffc8c3f72f5598d2))
* **setup:** use the stored credential for catalogue discovery ([92f7a96](https://github.com/etalab-ia/codebox/commit/92f7a96175a28ac2f5c90ca6439f537d29480328))
* **tart:** use upgraded OpenCode in full mode ([2492831](https://github.com/etalab-ia/codebox/commit/24928318adfbec15ab317079fb88bbe609b7f9e9))
* **test:** seal the Microsandbox seam in lifecycle tests ([2d0f069](https://github.com/etalab-ia/codebox/commit/2d0f06922f808c2c5b0733bcceb39cd7b9b09942))
* **trust:** reapprove changed declared plugin code ([b464d23](https://github.com/etalab-ia/codebox/commit/b464d237404ae58f83356371be22486fc804a861))
* **tui:** detect top cursor before resizing ([08e2950](https://github.com/etalab-ia/codebox/commit/08e2950255a9a869804201546c70d0c4ffd3caaa))
* **tui:** distinguish cursor from option text ([80991f6](https://github.com/etalab-ia/codebox/commit/80991f668dd056344e508f0b56d7d29b9a08690f))
* **tui:** keep multi-select options visible ([906ea83](https://github.com/etalab-ia/codebox/commit/906ea833f7a92f56d1c09caed1c00a58e8c4dbcd))
* **tui:** keep multi-select options visible ([3465e0f](https://github.com/etalab-ia/codebox/commit/3465e0f1ee7324eaf1dc39c9045740653bdaa1ae))
* **tui:** preserve cursor when resizing lists ([ac24537](https://github.com/etalab-ia/codebox/commit/ac24537560b0396b36710ea14ddb366917296e82))
* **tui:** preserve filter and cursor on resize ([1bf1079](https://github.com/etalab-ia/codebox/commit/1bf10793a745bce183455b0c9a74c7adf0e61a82))
* **tui:** reveal hidden cursor after resize ([4da9892](https://github.com/etalab-ia/codebox/commit/4da98923b1a919648e2d5d0f394a004c36944f99))
* **workspace:** avoid reverse-only recovery detection ([d538df8](https://github.com/etalab-ia/codebox/commit/d538df846f2e85d8c02ac7869eb1b60dc41da6e7))
* **workspace:** compare recovered files exactly ([b360ff8](https://github.com/etalab-ia/codebox/commit/b360ff8e67dc168e86d9105dff1f35980bd90804))
* **workspace:** format committed-change refusal ([624528a](https://github.com/etalab-ia/codebox/commit/624528afee2cb4258eaa08dc1c7aa1bfe3482e94))
* **workspace:** make recovery refresh transactional ([215335f](https://github.com/etalab-ia/codebox/commit/215335f038f8d510b312b6adb7d631479d215eb1))
* **workspace:** merge independent host edits during recovery ([df8b49f](https://github.com/etalab-ia/codebox/commit/df8b49fd6357c6a468134fd97beaa1e2752dc044))
* **workspace:** preserve committed work across host refreshes ([ffe6fa7](https://github.com/etalab-ia/codebox/commit/ffe6fa7e4eea8ac8e7527b22589ae665bc3a70f3))
* **workspace:** preserve committed work through forced refresh ([027a9d6](https://github.com/etalab-ia/codebox/commit/027a9d647e36c6c18ceaf8abf7013f29bd07f89a))
* **workspace:** preserve excluded deletions during refresh ([123f9f2](https://github.com/etalab-ia/codebox/commit/123f9f2c1a07b01b137915ba586e74223f093af9))
* **workspace:** preserve the publishable index during refresh ([a57f38b](https://github.com/etalab-ia/codebox/commit/a57f38b9168c8603ac0f36f3a8a753e16402afe3))
* **workspace:** quote refresh program and clean rollback ([8b37520](https://github.com/etalab-ia/codebox/commit/8b37520dad00cab08e36475b094ad5cdc0108a7f))
* **workspace:** recover changes the agent already committed ([ab9c20e](https://github.com/etalab-ia/codebox/commit/ab9c20e5c0bcbfffe66031fecc18b1950be964fd))
* **workspace:** recover changes the agent already committed ([4784c28](https://github.com/etalab-ia/codebox/commit/4784c288dcc58fb10ca5bb0225b9c4d0f5a37429))
* **workspace:** refresh recovery comparison state ([b488ab8](https://github.com/etalab-ia/codebox/commit/b488ab889949b160921e3438e22eab71ba7733fe))
* **workspace:** restore omitted modified paths before recovery ([b336d1c](https://github.com/etalab-ia/codebox/commit/b336d1ce483bef33e66cd0e617e14b357ba06755))
* **workspace:** support older Git in bootstrap baseline ([c310c49](https://github.com/etalab-ia/codebox/commit/c310c493f106d9849288945b3cf00c6b3a6a13c5))
* **workspace:** support older Git in bootstrap baseline ([4237f92](https://github.com/etalab-ia/codebox/commit/4237f92c9951133000c3a1bf3d8826520dacd320))
* **workspace:** verify recovered additions against the host ([6c22fa8](https://github.com/etalab-ia/codebox/commit/6c22fa87d386b7527415081fe29645f4fc727102))

## [0.14.1](https://github.com/etalab-ia/codebox/compare/codebox-v0.14.0...codebox-v0.14.1) (2026-10-30)


### Bug Fixes

* add provider selection to text setup ([3f82d69](https://github.com/etalab-ia/codebox/commit/3f82d694e4eba1e998c7305c726f3e9b79ada5fd))
* **config:** omettre les limites de modèles OpenCode partielles ([62b266e](https://github.com/etalab-ia/codebox/commit/62b266e26ff10da0f6299c3e1e381627a6f60747))
* **config:** omit partial OpenCode model limits ([cd8bff4](https://github.com/etalab-ia/codebox/commit/cd8bff4edf87b6906970e8b2f43871a5ad925bf3))
* **setup:** allow provider selection in text mode ([d50116a](https://github.com/etalab-ia/codebox/commit/d50116a5f2fcdb528b4416b62511c40e5309ea00))

## [0.14.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.13.0...codebox-v0.14.0) (2026-10-28)


### Features

* **init:** offer Ministral as OpenCode small model ([6756312](https://github.com/etalab-ia/codebox/commit/6756312fa1b602fc20187b46dd3ce63f3c9d21ba))
* **init:** permettre Ministral comme petit modèle OpenCode ([256e423](https://github.com/etalab-ia/codebox/commit/256e4233845aae324e924e7be6c73a3ef11c663b))


### Bug Fixes

* **init:** require Albert for Ministral small model ([12e5677](https://github.com/etalab-ia/codebox/commit/12e56770a342f492a5fcb004e4083d90ce85c4b2))
* **init:** validate curated small-model references ([0e36e8a](https://github.com/etalab-ia/codebox/commit/0e36e8ae7f965109f54c8a4d801846c2544ee006))
* **init:** validate small model in line wizard ([ad5a205](https://github.com/etalab-ia/codebox/commit/ad5a205188193e41b6a11be09fc3f047207519ca))
* **models:** validate small-model API references ([3c44a52](https://github.com/etalab-ia/codebox/commit/3c44a52b752fb33163f74e217b87b9c8668d9ffd))

## [0.13.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.12.0...codebox-v0.13.0) (2026-10-28)


### Features

* **exit:** écran de fin de session et récupération des modifications ([e8f8f5e](https://github.com/etalab-ia/codebox/commit/e8f8f5e725df7a7e68e228d704e979bffe04d92c))
* **exit:** écran de fin de session et récupération des modifications ([abeaaeb](https://github.com/etalab-ia/codebox/commit/abeaaeb6bfad85a1f2abf6609314f8483c37808c))
* **init:** complete the six-skill setup flow ([#167](https://github.com/etalab-ia/codebox/issues/167)) ([c3dd76f](https://github.com/etalab-ia/codebox/commit/c3dd76f2e4c28c8f12e2dc31c51cd92564166c0f))
* **init:** déplacer les réglages avancés dans le récapitulatif ([#170](https://github.com/etalab-ia/codebox/issues/170)) ([591c2c3](https://github.com/etalab-ia/codebox/commit/591c2c3a537ffd0066aa635b0998154dda08cfa3))
* **init:** GitHub wizard 3.9, four verified screens ([57dbef2](https://github.com/etalab-ia/codebox/commit/57dbef2bf0a98ec3aca832d581c1e83ae4ccc686))
* **init:** improve skills selection screen ([#167](https://github.com/etalab-ia/codebox/issues/167)) ([e083dde](https://github.com/etalab-ia/codebox/commit/e083ddec9d748f975cdad495bc19102acc4d2100))
* **init:** introduce the project setup wizard ([4b4cf5f](https://github.com/etalab-ia/codebox/commit/4b4cf5f2708d9bb7ade6db5b143482c5fca9ea07))
* **init:** introduire l’écran de configuration du projet ([4152c4b](https://github.com/etalab-ia/codebox/commit/4152c4bdf91256a74a02bbacc7c2482e042c0308))
* **init:** MCP wizard 3.8 and withdrawal of context7 and playwright ([b22af47](https://github.com/etalab-ia/codebox/commit/b22af47c1439194fe055f110cde7663aad2df67f))
* **init:** MCP wizard 3.8 and withdrawal of context7 and playwright ([#168](https://github.com/etalab-ia/codebox/issues/168) [#175](https://github.com/etalab-ia/codebox/issues/175)) ([283be38](https://github.com/etalab-ia/codebox/commit/283be38f3a6c3c1f88a905971c0bfccd8938ae48))
* **init:** refaire le récapitulatif interactif ([1359e79](https://github.com/etalab-ia/codebox/commit/1359e795b24b820863a4659a46b165af3a0d70fa))
* **init:** refaire le récapitulatif interactif ([#171](https://github.com/etalab-ia/codebox/issues/171)) ([d82f42c](https://github.com/etalab-ia/codebox/commit/d82f42c1622a3108158c2c2a67c042fb36095f3c))
* **init:** réserver les réglages techniques au récapitulatif ([#170](https://github.com/etalab-ia/codebox/issues/170)) ([63fced7](https://github.com/etalab-ia/codebox/commit/63fced746fa3643e1219699e2b32dcb134f8136c))
* **init:** rework the GitHub step into four verified screens ([#169](https://github.com/etalab-ia/codebox/issues/169)) ([d8b38b8](https://github.com/etalab-ia/codebox/commit/d8b38b811b4194cea2abefdc68250bd7c4ae15f9))
* **init:** terminer le parcours de sélection des skills ([#167](https://github.com/etalab-ia/codebox/issues/167)) ([260bfc6](https://github.com/etalab-ia/codebox/commit/260bfc635048d063a5e8db49b3677cbb4e615b9c))
* **launch:** résumer le premier démarrage et approuver les entrées sur place ([f127c93](https://github.com/etalab-ia/codebox/commit/f127c93926aaf2b06d5dca64c965def391baf3b5))
* **launch:** simplifier le premier lancement et approuver sur place ([1ac11c5](https://github.com/etalab-ia/codebox/commit/1ac11c5bc3b67d2f1b5acfe539b203aa8012db6b))
* **secrets:** fournir le détecteur de secrets au lieu de le demander à l'hôte ([db53bd8](https://github.com/etalab-ia/codebox/commit/db53bd8cefb6bdce78354764047a2dfa340a7303))
* **secrets:** fournir le détecteur de secrets au lieu de le demander à l'hôte ([4191767](https://github.com/etalab-ia/codebox/commit/4191767a8870b6d4e8c7e7c5241d81580265f8ee))
* **setup:** add editable recap choices and credential status ([63996e4](https://github.com/etalab-ia/codebox/commit/63996e48d81bbb8cab8298cb080d0ef6b5ddd17f))
* **setup:** add multi-model selection ([3796545](https://github.com/etalab-ia/codebox/commit/37965450b1d09fbc1f2588afa8f9fbe3d5829fc2))
* **setup:** clarify recap and show install progress ([44bb6d8](https://github.com/etalab-ia/codebox/commit/44bb6d8104652cb6bd1643164b8cfbc0d2b62aec))
* **setup:** connecter plusieurs plateformes d'inférence ([3c687f9](https://github.com/etalab-ia/codebox/commit/3c687f961646595ad45f9262ccda870aae9f0802))
* **setup:** connecter plusieurs plateformes d'inférence ([e3d95c4](https://github.com/etalab-ia/codebox/commit/e3d95c4974327642e3725ba5261417a692f862bd))
* **setup:** explain and validate the optional GitHub token ([#164](https://github.com/etalab-ia/codebox/issues/164)) ([1594d67](https://github.com/etalab-ia/codebox/commit/1594d675da7879860de13282b5bb2aea09487d05))
* **setup:** expliquer et valider le jeton GitHub facultatif ([#164](https://github.com/etalab-ia/codebox/issues/164)) ([41fc411](https://github.com/etalab-ia/codebox/commit/41fc4112b2d7ccd26d9427c46182b3d4240e15dc))
* **setup:** finaliser le récapitulatif et l’installation ([#165](https://github.com/etalab-ia/codebox/issues/165)) ([125d1ca](https://github.com/etalab-ia/codebox/commit/125d1caff357628f5b76d965ba484ced0ee9613b))
* **setup:** sélectionner plusieurs modèles Albert API ([bc785dd](https://github.com/etalab-ia/codebox/commit/bc785ddc45ceba2f4ca157a34d1025c4fe3c5ac4))
* **setup:** statistiques d'usage avec consentement ([115e6ca](https://github.com/etalab-ia/codebox/commit/115e6cad5eca82a96efa5669bc5d0e1e1817954b))
* **setup:** statistiques d'usage avec consentement ([cbe0ad4](https://github.com/etalab-ia/codebox/commit/cbe0ad48d2acce90e20cc15764eaedccc8cdbfc2))


### Bug Fixes

* **import:** préserver une configuration OpenRouter sans Albert ([72c9682](https://github.com/etalab-ia/codebox/commit/72c968290afe3cede4476a173b979791ad21d8ee))
* **init:** address Codex review on MCP wizard seeding, retention, and probe ([6e84779](https://github.com/etalab-ia/codebox/commit/6e8477903e3b264a0c409d60a80bd119615d4e89))
* **init:** annuler le récapitulatif accessible sur EOF ([cf26f9a](https://github.com/etalab-ia/codebox/commit/cf26f9ab3553355f856a7c44d26e9986ec370a6f))
* **init:** close the review findings on the GitHub step ([97ccfa1](https://github.com/etalab-ia/codebox/commit/97ccfa1ee68e0bd648a819222157f8dde71bf7aa))
* **init:** fiabiliser le récapitulatif avant application ([993beef](https://github.com/etalab-ia/codebox/commit/993beefffb5c86dcac27655117034967f48e5827))
* **init:** garantir l'annulation accessible après une entrée invalide ([f3dccca](https://github.com/etalab-ia/codebox/commit/f3dcccaf3411df514a35c1115c3d3c60028de089))
* **init:** give the TUI token stage a reader ([0e39540](https://github.com/etalab-ia/codebox/commit/0e39540e5cffd86e029f9d7e7bcd8074bc01341d))
* **init:** isolate global credential check ([185155a](https://github.com/etalab-ia/codebox/commit/185155aee80edd67a4862c9a4a41623afc54f1f9))
* **init:** keep retained pins and sibling choices on retry ([#167](https://github.com/etalab-ia/codebox/issues/167)) ([dbac9a5](https://github.com/etalab-ia/codebox/commit/dbac9a5d07dadd7fb3d1cf50bc492c04171274d2))
* **init:** permettre de corriger les réglages hérités invalides ([f696306](https://github.com/etalab-ia/codebox/commit/f69630629a42ff5e54b80b64b9375e1979861fe7))
* **init:** permettre de réparer un runtime hérité incompatible ([dd03269](https://github.com/etalab-ia/codebox/commit/dd03269efc1f639b281ac2b0c9da37193fb4009e))
* **init:** preserve and retry skill selections ([#167](https://github.com/etalab-ia/codebox/issues/167)) ([1f61da5](https://github.com/etalab-ia/codebox/commit/1f61da516a36c4349f01743e5c11dc6df56bf484))
* **init:** probe default Albert credential for setup ([140f624](https://github.com/etalab-ia/codebox/commit/140f624a649679accd7bb895e2616d413b80f80f))
* **init:** render the GitHub step last and keep the step order honest ([6eb5967](https://github.com/etalab-ia/codebox/commit/6eb59673a11b40b8fda257f1e6536c12392d500e))
* **init:** reopen the questions when the GitHub situation changes ([d16aa1a](https://github.com/etalab-ia/codebox/commit/d16aa1a14a98de30a36a3e82853f50a7088de1de))
* **init:** scope workspace intro to runtime ([8c891bc](https://github.com/etalab-ia/codebox/commit/8c891bc0c01f6f9b3ba1ab2523725f91de202a1a))
* **init:** state the GitHub check without an empty account name ([d51995a](https://github.com/etalab-ia/codebox/commit/d51995a328c2c7b8d6b253042d22ef3b5b1ade01))
* **init:** state the runtime limit before asking about GitHub ([93b4673](https://github.com/etalab-ia/codebox/commit/93b4673d88f32965c1a444313d69069eb68feaa5))
* **init:** uniformiser les chemins du récapitulatif ([375e495](https://github.com/etalab-ia/codebox/commit/375e4958fe621b9692c1864597d4c8379981616f))
* **init:** validate options before setup offer ([d7b5886](https://github.com/etalab-ia/codebox/commit/d7b58863961a4d29ccafae6fc7c2b5b2d0ea3317))
* **init:** verify the Pull requests permission before claiming the rights ([fc7131c](https://github.com/etalab-ia/codebox/commit/fc7131c43ae5b9c12508d0e115d99414280cadee))
* **launch:** aligner les mesures et la disponibilité sur le runtime ([d4b1eaa](https://github.com/etalab-ia/codebox/commit/d4b1eaa4dbdd8539a8dbfeeb05df746eed565469))
* **launch:** attendre la disponibilité avant de conserver la durée ([2dc36c2](https://github.com/etalab-ia/codebox/commit/2dc36c22776ec72ed03ebc52dfd8ec55752665b3))
* **launch:** distinguer les installations reprises des démarrages à chaud ([a7360c1](https://github.com/etalab-ia/codebox/commit/a7360c13c882232792c2ee55c0a5407c8d61c6d1))
* **launch:** vérifier la préparation après réconciliation en isolation complète ([afe4b14](https://github.com/etalab-ia/codebox/commit/afe4b14f776219f5c77555a4d87d3ab2f40c3d78))
* narrow Gitleaks allowlist to public matches ([11e35a8](https://github.com/etalab-ia/codebox/commit/11e35a87f168f2e9ec8eb3571ff3cfdafa025a3f))
* **opencode:** corriger la justification du mécanisme de désactivation ([d268dab](https://github.com/etalab-ia/codebox/commit/d268daba4a250b0beb698cbed8fe76fb03b4dd37))
* **opencode:** désactiver la vérification de mise à jour dans tous les runtimes ([ce07ca8](https://github.com/etalab-ia/codebox/commit/ce07ca84780a3f894ff6fdadab3fd9780c2da85b))
* **opencode:** désactiver la vérification de mise à jour dans tous les runtimes ([d35fff0](https://github.com/etalab-ia/codebox/commit/d35fff0258eeca5298d7ad0b1f0c385b91288502))
* **secrets:** ancrer le binaire épinglé et durcir l'extraction ([61d8668](https://github.com/etalab-ia/codebox/commit/61d866888b6adc2af791df89c38052025770fe91))
* **secrets:** rendre la vérification d'exécution portable sur Windows ([47121a9](https://github.com/etalab-ia/codebox/commit/47121a9bdbc067aec22d2f3a7d96186c4e8a0653))
* **setup:** cancel pending token replacement on apply ([6d43fc0](https://github.com/etalab-ia/codebox/commit/6d43fc07b2dcf102ad185c7def06c7e85080a712))
* **setup:** corriger quatre cas limites du multi-plateforme ([2e146d0](https://github.com/etalab-ia/codebox/commit/2e146d03abc59e80e9d9585db83baa874c6b0091))
* **setup:** keep model selection in one screen ([55b553b](https://github.com/etalab-ia/codebox/commit/55b553b9794801b512c54a3b987c4a253e07172b))
* **setup:** normalize legacy model references ([e8e8a65](https://github.com/etalab-ia/codebox/commit/e8e8a651aecad57216bfd9ce3db2f8cc51409908))
* **setup:** preserve legacy model settings ([60ec2c0](https://github.com/etalab-ia/codebox/commit/60ec2c05953a3010213844fd4ee7f635a01591b6))
* **setup:** preserve unknown status in legacy journals ([e4a4b3e](https://github.com/etalab-ia/codebox/commit/e4a4b3ebe0c449976950cd6fa561ee6c978760c6))
* **setup:** préserver les alias de modèles et revalider les plateformes ([cf9caba](https://github.com/etalab-ia/codebox/commit/cf9caba910630723c985103d898c17c801055f50))
* **setup:** qualify runtime download estimate ([c857d0e](https://github.com/etalab-ia/codebox/commit/c857d0ec28a54b447fcdefbb640078cf908243bc))
* **setup:** repartir des plateformes enregistrées et valider l'import complet ([9b43660](https://github.com/etalab-ia/codebox/commit/9b436602198b90924010a895e312b329ef7ef3cf))
* **setup:** resume GitHub replacement and cancel on review EOF ([a36506b](https://github.com/etalab-ia/codebox/commit/a36506b2cdfd4ac1c0d986515237c033d768fa06))
* **setup:** retain existing provider defaults ([e84ea89](https://github.com/etalab-ia/codebox/commit/e84ea89b6e758ca8414fdc43515d857488f7d4cb))
* **setup:** skip empty git identity form ([69e21d8](https://github.com/etalab-ia/codebox/commit/69e21d8ba48dc668c2f173b20ec7835b83a0c8b7))
* **setup:** valider les imports et ne jamais suivre une redirection ([478031c](https://github.com/etalab-ia/codebox/commit/478031c12f3f6a4a090f273b9e69772c92388715))
* **setup:** vérifier les clés présentes, préserver Albert et réserver les variables ([293dee7](https://github.com/etalab-ia/codebox/commit/293dee7d497741250422afee94e02597e5cb71b0))
* **stats:** afficher le consentement effectif et préserver un « o » existant ([f815bc3](https://github.com/etalab-ia/codebox/commit/f815bc33c27db4a5b207d2cd559a6108dd389212))
* **stats:** appliquer le consentement en attente à tous les événements ([d6b3b5e](https://github.com/etalab-ia/codebox/commit/d6b3b5ec32f11d780916a3b61b52ee5d04134351))
* **stats:** corriger les permissions Windows et le double comptage ([16a5a59](https://github.com/etalab-ia/codebox/commit/16a5a59d6bbb840e3a970736ce7e9ba9e1d16b37))
* **stats:** déduire les plateformes de la sélection finale ([2afef44](https://github.com/etalab-ia/codebox/commit/2afef443d359f2ae720bbefd3be8127f85791611))
* **stats:** faire primer un refus en attente sur l'acceptation durable ([59a920c](https://github.com/etalab-ia/codebox/commit/59a920c9632158725fafd4d84f8df2f6f433578c))
* **stats:** honorer la décision de consentement en attente pour un abandon ([868c0e7](https://github.com/etalab-ia/codebox/commit/868c0e7d9942ca0fc66e8ab8607d31a44c97d2f3))
* **stats:** n'enregistrer un abandon qu'après le consentement ([be9b0eb](https://github.com/etalab-ia/codebox/commit/be9b0eb464cbe27f9f872843f80af73836375dcf))
* **stats:** ne pas ignorer l'échec d'écriture du journal de consentement ([3691033](https://github.com/etalab-ia/codebox/commit/3691033924968ca3c8db37260b2b9247bbb7724f))
* **stats:** ne retenir qu'un changement durable postérieur à la revue ([71d4606](https://github.com/etalab-ia/codebox/commit/71d460681ec24faa4d5318fe30f85c52d3db3256))
* **stats:** préserver la file, couvrir le wizard huh et Windows ([d2c1f5c](https://github.com/etalab-ia/codebox/commit/d2c1f5c5c3d0f07744ee350b29e15c8732f72346))
* **stats:** récupérer un verrou mort et tracer les sorties GitHub du TUI ([47f1a8d](https://github.com/etalab-ia/codebox/commit/47f1a8dd06ff5f9db78c619e265a82c862b8bdde))
* **stats:** résoudre le consentement au moment de l'application ([ae24da7](https://github.com/etalab-ia/codebox/commit/ae24da7093161ababfb5033e5e48e5280dabc175))
* **stats:** résoudre le consentement de la même façon partout ([2a7444d](https://github.com/etalab-ia/codebox/commit/2a7444de6e6393088e0315afc5140c3f1ac05180))
* **stats:** respecter un refus explicite à la reprise ([2e3c53e](https://github.com/etalab-ia/codebox/commit/2e3c53eaa934a36c141d5102d493a36a81520064))
* **stats:** restaurer la décision du journal sans supposer son ancienneté ([f62969e](https://github.com/etalab-ia/codebox/commit/f62969ef528d24005cf68ceaab7a846be9e80752))
* **stats:** restaurer le consentement du journal et sérialiser les identifiants ([73f83d1](https://github.com/etalab-ia/codebox/commit/73f83d11dc5bc568826c7f63b0fc36c0b4edd80c))
* **stats:** synchroniser le consentement dans les deux sens ([304c73c](https://github.com/etalab-ia/codebox/commit/304c73c62b764b7c1b53c803f522a0cd555f5cb4))
* **stats:** synchroniser le refus du journal avant de vider la file ([4c6b3d7](https://github.com/etalab-ia/codebox/commit/4c6b3d7f45334003a8a5c9c2dffb4aca241d0ae9))
* **tart:** forward selected model definitions to guest ([559eefb](https://github.com/etalab-ia/codebox/commit/559eefbf30751f841bafece1a24308aaf92abbce))
* **tart:** restart backend when overlay changes ([fb87df6](https://github.com/etalab-ia/codebox/commit/fb87df6bbbe8c6af06cf270a251cd8922d721ecd))
* **test:** parse the native kit go.mod instead of token-scanning ([17e5e3a](https://github.com/etalab-ia/codebox/commit/17e5e3afe8cbe63403acf24cf0ad31ab9fbfac83))
* **test:** pin the native secret kit to the embedded SDK ([bb79304](https://github.com/etalab-ia/codebox/commit/bb79304fe142a3d15447517e765d5b1c82560ab3))
* **workspace:** appliquer la source configurée et conserver la base de récupération ([4c4cbbd](https://github.com/etalab-ia/codebox/commit/4c4cbbd967607460c44cb0b5d1daea2415f25bb9))
* **workspace:** conserver les modifications de l'hôte et détecter les réversions ([f844d2a](https://github.com/etalab-ia/codebox/commit/f844d2a212979b960c0fbabba95429ee202c3c9b))
* **workspace:** signaler les inversions après fusion et poser le mode invité ([e811d76](https://github.com/etalab-ia/codebox/commit/e811d7616da732ff4359d7bd1ffb04d78dbb4b7f))

## [0.12.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.11.0...codebox-v0.12.0) (2026-10-23)


### Features

* **install:** messages français, PATH avant setup, postes non pris en charge ([b83a99c](https://github.com/etalab-ia/codebox/commit/b83a99c908e4cf07c2a36d55a089ca1c7753b7aa))
* **install:** messages français, PATH avant setup, postes non pris en charge ([ab503d4](https://github.com/etalab-ia/codebox/commit/ab503d407e594a9243e1af66852cf6e3b086b813))
* **setup:** écran d'introduction avant la première question ([399cffd](https://github.com/etalab-ia/codebox/commit/399cffdbbc7edb3428f231656a2fb25abf78d720))
* **setup:** écran d'introduction avant la première question ([88d5714](https://github.com/etalab-ia/codebox/commit/88d57144540afc6a455cbd02f802c98c877e0d69))
* **setup:** écran de la clé Albert API, ordre clé → modèles → GitHub ([98309b1](https://github.com/etalab-ia/codebox/commit/98309b1f433110c80326526b433b0b00abf9932f))
* **setup:** écran de la clé Albert API, ordre clé → modèles → GitHub ([1f20630](https://github.com/etalab-ia/codebox/commit/1f20630c8efd4a5c0ce10522091747ed920dd293))


### Bug Fixes

* **agent-vm:** épingler une image Lima résolvable et la vérifier avant reconstruction ([1f545a8](https://github.com/etalab-ia/codebox/commit/1f545a802cdca7ed7998d50c1b0814afe99532e0))
* **agent-vm:** épingler une image Lima résolvable et la vérifier avant reconstruction ([87ca8eb](https://github.com/etalab-ia/codebox/commit/87ca8eb337876cdd4c6ddaeff0d6ce46fe4c74c9))
* **agent-vm:** report an inconclusive image check, not a passed one ([9fb869e](https://github.com/etalab-ia/codebox/commit/9fb869e3447cc265c7f55d49713e05434abb3d1f))
* **agent-vm:** stop recommending the default image when it already failed ([94826ec](https://github.com/etalab-ia/codebox/commit/94826eca5a4f9650d58f962cdd8bb3ab287bc59a))
* **agent-vm:** témoin sans image pour ne pas imputer un refus de limactl à AGENT_VM_IMAGE ([32062aa](https://github.com/etalab-ia/codebox/commit/32062aacdcc8f524cbe4b34b3a3d7726332a1a56))
* **agent-vm:** use limactl info as the image-independent witness ([d4101d3](https://github.com/etalab-ia/codebox/commit/d4101d31e14f07d4427fc13026c2a1460631eeef))
* **credentials:** lire un identifiant dans le magasin qui l'a reçu ([dfbae65](https://github.com/etalab-ia/codebox/commit/dfbae65b97d7ae5dd4278c62d4c4ccb0947e6d34))
* **credentials:** read a credential from the store that received it ([4e5f8de](https://github.com/etalab-ia/codebox/commit/4e5f8def835451bf9cf11bbc21a194e544b5a2bc))
* **credentials:** serialize the store record and stop reporting unpinnable success ([6cb5f9c](https://github.com/etalab-ia/codebox/commit/6cb5f9c0ebfe3d5b66302ac5bc285f10d6baf4ea))
* **diagnostics:** sonde Go, cause de résolution nommée, portée de la sonde de magasin ([364263d](https://github.com/etalab-ia/codebox/commit/364263d08e63af30f3241959a838e58db1984748))
* **diagnostics:** sonde Go, cause de résolution nommée, portée de la sonde de magasin ([5acebe1](https://github.com/etalab-ia/codebox/commit/5acebe1cf16cd0f994cc4669aee0e057f0488704))
* **doc,test:** compteurs exacts, garde d'introduction réellement testée ([d03b736](https://github.com/etalab-ia/codebox/commit/d03b7365568bb73eae0c1452d9969d513dc817f5))
* **doctor:** distinguer inventaire incomplet et absence de journal ([ce93ccc](https://github.com/etalab-ia/codebox/commit/ce93ccc717de092e8c86765cef8bc45af42f5698))
* **doctor:** l'inventaire des identifiants vient du magasin, pas du journal ([bb37138](https://github.com/etalab-ia/codebox/commit/bb371388a4552a70a215a46916f2acaf0075c564))
* **doctor:** l'inventaire des identifiants vient du magasin, pas du journal ([bb2aef4](https://github.com/etalab-ia/codebox/commit/bb2aef425ad21ca5ba6483973e9c44a53c011dfc))
* **install:** fichier lu par un terminal ordinaire, chemins cités ([90f2711](https://github.com/etalab-ia/codebox/commit/90f2711d66edfc7abf20dc5bd11588a116b8ad9c))
* **install:** ne pas empiler la ligne PATH, doc alignée sur le code ([3e565d7](https://github.com/etalab-ia/codebox/commit/3e565d7be1b66a8024b1b745f77966916b3baa3d))
* **install:** profil bash, SHELL vide, reprise et retour arrière ([a6b617c](https://github.com/etalab-ia/codebox/commit/a6b617c9ae3c58df9b6783dbd4a641f4993d6f0d))
* **install:** reconnaître la ligne PATH exacte, pas le mot just-code ([bec51ba](https://github.com/etalab-ia/codebox/commit/bec51ba9fdb0e95e7d66f9a34639a8dd49fc3618))
* **install:** reprise, interruption, profil non modifiable, sauvegarde ([a22b4de](https://github.com/etalab-ia/codebox/commit/a22b4deb92c8e8cdee8efaa18a8160ae6286a6a2))
* **install:** shells non POSIX, commandes de récupération littérales ([f3942f6](https://github.com/etalab-ia/codebox/commit/f3942f6d9ce5b974d28b8592581d073a97c8e617))
* **install:** un seul fichier par plateforme, lecture ratée = refus ([be94056](https://github.com/etalab-ia/codebox/commit/be94056e9330cbde74ae983a03debf6251d3ebe9))
* **secrets:** accepter le type natif concret enregistré dans l'index ([d5017a7](https://github.com/etalab-ia/codebox/commit/d5017a7b756080bd60501e9cf476b72c5d84ed94))
* **secrets:** aligner les limites des réparses d'attribution ([a7e7936](https://github.com/etalab-ia/codebox/commit/a7e7936abfeee93b5f2a2440e76f57747da54ba9))
* **secrets:** classer le refus de backticks avant le retour chariot ([70c1861](https://github.com/etalab-ia/codebox/commit/70c18611896e13647de6cb13aa50d595ac067385))
* **secrets:** ne pas masquer un magasin demandé ni arrêter un bac à sable sain ([a9fa42a](https://github.com/etalab-ia/codebox/commit/a9fa42a23a4139fc5b60b18aeca09cdb2fc43cc9))
* **secrets:** ne rien enregistrer quand une approbation ne peut pas résoudre ([8efd5f5](https://github.com/etalab-ia/codebox/commit/8efd5f582c7705f35f1cba5718f2f4ae569a1399))
* **secrets:** ne rien enregistrer quand une approbation ne peut pas résoudre ([3e27678](https://github.com/etalab-ia/codebox/commit/3e276781bf9a5736b36dc5e1e567bd68e4194921))
* **secrets:** nommer la variable et la règle au refus d'import via go-rotini ([673af49](https://github.com/etalab-ia/codebox/commit/673af49a6e59ad7a5edc4b692a1176809aa2ba55))
* **secrets:** nommer la variable et la règle au refus d'import via go-rotini ([e605669](https://github.com/etalab-ia/codebox/commit/e6056695a167fe772c5c44e93e5575a0737fd6b7))
* **secrets:** nommer la variable et la règle au refus de backticks ([60997ea](https://github.com/etalab-ia/codebox/commit/60997eaa8f7575fcddd9304b1e01908e0f2db673))
* **secrets:** propager un échec de sélection du magasin avant tout redémarrage ([d5c5f8a](https://github.com/etalab-ia/codebox/commit/d5c5f8ad146ac3a5a03ec087b9725ff45db5a111))
* **secrets:** refuser avant d'ouvrir le magasin, et signaler une approbation existante ([cb4d3a4](https://github.com/etalab-ia/codebox/commit/cb4d3a4883d8d79c54b684fc34a9199df4e55556))
* **secrets:** rendre effective la confirmation de bindings approve ([0f6ccdc](https://github.com/etalab-ia/codebox/commit/0f6ccdceadca4e5ad26b512d2432b675f6db6ef5))
* **secrets:** rendre effective la confirmation de bindings approve ([f1e2b5d](https://github.com/etalab-ia/codebox/commit/f1e2b5d0f2b59ff99506dac02c815e8ac04270d8))
* **secrets:** résoudre le magasin depuis le type enregistré à l'import ([e3f68a4](https://github.com/etalab-ia/codebox/commit/e3f68a4c59be69a65f5d41a722069bedd0f06bcf))
* **secrets:** résoudre le magasin depuis le type enregistré à l'import ([2934733](https://github.com/etalab-ia/codebox/commit/2934733e85e763e66df77afdf352dbec2a81e67b))
* **setup:** l'intro attend Entrée sans afficher la saisie collée ([9c95ea8](https://github.com/etalab-ia/codebox/commit/9c95ea8d12dd8c98779a4fb2c140174d5e45f0ad))
* **setup:** l'intro ne consomme pas la saisie collée avec Entrée ([ed66859](https://github.com/etalab-ia/codebox/commit/ed6685953a40b0deff0d92418bade0da9886a041))
* **setup:** l'intro ne peut pas promettre qu'aucune écriture n'a lieu ([ad35b89](https://github.com/etalab-ia/codebox/commit/ad35b893385f1f6c8ee6116c6abff7df860983fc))
* **setup:** l'intro nomme aussi l'identité git comme enregistrée ([375efd4](https://github.com/etalab-ia/codebox/commit/375efd4159283faf7ad97b62953543ca55ec83ea))
* **setup:** le choix explicite du défaut survit à une reprise sans réponse ([17c959d](https://github.com/etalab-ia/codebox/commit/17c959db54f8a2917c1be97f24eec4819b55f9c1))
* **setup:** le modèle choisi survit à une interruption plus loin ([a46d028](https://github.com/etalab-ia/codebox/commit/a46d028d7962ada26e8d1e43ffdeac641fe623cc))
* **setup:** ne pas stocker la clé avant d'avoir demandé ([fcacdd1](https://github.com/etalab-ia/codebox/commit/fcacdd10d273b65a08df563d1f6ef22bda9a9ceb))
* **setup:** une faute de frappe redemande, le repli dit où va la clé ([81abe70](https://github.com/etalab-ia/codebox/commit/81abe70fcd2e27ab987dcad41785c94bed3f393e))
* **setup:** une question non répondue annule au lieu de stocker ([87ae8d0](https://github.com/etalab-ia/codebox/commit/87ae8d017891486c7a799c50cc9db04a9d315904))
* **setup:** une question sans réponse ne remet pas le modèle à zéro ([802c0c7](https://github.com/etalab-ia/codebox/commit/802c0c783bf297e82050e6ffb360165a8b602c4e))
* **setup:** une réponse vide revient au modèle par défaut ([68754e7](https://github.com/etalab-ia/codebox/commit/68754e7cdc86685c0460da7c3d790f29dc7e9693))
* **start:** inverser le classement — ne tolérer qu'une panne de magasin ([800f1d6](https://github.com/etalab-ia/codebox/commit/800f1d6e99550b4cc4b4eb909052a71ed22e3b53))
* **start:** refuser un démarrage no-op sur une liaison hors profil actif ([681bbbb](https://github.com/etalab-ia/codebox/commit/681bbbb78015db3abecb549f2e072763a1134610))
* **start:** refuser un démarrage no-op sur une liaison hors profil actif ([c46d8e1](https://github.com/etalab-ia/codebox/commit/c46d8e17c3540a859d725947e712356bf9522880))
* **start:** un secret de projet approuvé sans valeur importée est structurel ([bb160f2](https://github.com/etalab-ia/codebox/commit/bb160f23dc55d34d514db9d3135ed1e8d7276763))
* **tart:** ne pas diagnostiquer un démarrage quand on s'attache à une VM déjà lancée ([01f9ae2](https://github.com/etalab-ia/codebox/commit/01f9ae2ca12868d64469144e4a61d57b0471cedd))
* **tart:** ne pas relire l'échec d'une exécution précédente, et préciser la portée de la sonde ([d2e2115](https://github.com/etalab-ia/codebox/commit/d2e2115810774983fe095b88e15f912f17064499))
* **tart:** nommer le refus de Virtualization.framework au lieu d'un délai agent ([b982e41](https://github.com/etalab-ia/codebox/commit/b982e417b2f57efc146f8edb9adb20609d38dc50))
* **tart:** nommer le refus de Virtualization.framework au lieu d'un délai agent ([a0d6df2](https://github.com/etalab-ia/codebox/commit/a0d6df2b48bc97739e40fbbf5ce437403c4bdabb))
* **tart:** relever le journal avant le lancement, et cibler le vrai échec de trousseau ([d117f79](https://github.com/etalab-ia/codebox/commit/d117f79eb924a96c3f48af5c021665267a35ac88))
* **tests:** attendre le marqueur de setup dans le test pty du profil non modifiable ([eba353c](https://github.com/etalab-ia/codebox/commit/eba353c16f94c3f311384629ccf2f2bd1ca6ae18))
* **tests:** la boucle pty attend le marqueur de setup, pas la bannière ([e3c366f](https://github.com/etalab-ia/codebox/commit/e3c366f4eb9010a2434617b569cc4a31afc4aa91))
* **tests:** vider le pty avant d'asserter sur la sortie capturée ([ae3b0b7](https://github.com/etalab-ia/codebox/commit/ae3b0b70f81092648ca55c6ad81a4a17c5d624bd))
* **transfer:** conserver les dates sous la seconde avec un en-tête PAX ([af46206](https://github.com/etalab-ia/codebox/commit/af46206ce33234ca0f851b4599ca9eba0205264b))
* **transfer:** préserver les dates de modification dans l'espace de travail scellé ([3e96c0d](https://github.com/etalab-ia/codebox/commit/3e96c0ddcd6b6496ac6a77b3e9248f1b4fb20e63))
* **transfer:** préserver les dates de modification dans l'espace de travail scellé ([bd63d7f](https://github.com/etalab-ia/codebox/commit/bd63d7f2dbff2ed7f2669a65ad5455f37a41f709))

## [0.11.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.10.0...codebox-v0.11.0) (2026-10-19)


### Features

* **check:** report reachability of configured preview ports ([9357fc3](https://github.com/etalab-ia/codebox/commit/9357fc37f82ecc15e1557d08f342619e670cab0d))


### Bug Fixes

* **check:** probe the port mappings applied to the running sandbox ([d61f498](https://github.com/etalab-ia/codebox/commit/d61f498b0d8916d0c8329fb36812cd65c01c7b8a))
* **check:** report stale forwards and zone strips in emptied states ([a365020](https://github.com/etalab-ia/codebox/commit/a365020c6a85d477c19ac168afc820bccfb38b77))
* **import:** annoncer l'approbation de confiance requise au premier lancement ([8467335](https://github.com/etalab-ia/codebox/commit/846733576e94c56adc10896b19d77ceb6c6127b3))
* **import:** announce the trust approval the first run will require ([713c954](https://github.com/etalab-ia/codebox/commit/713c95468f1991a87a6d1c4a2f52895203db9664))
* **import:** harden reads of untrusted project OpenCode configs ([6368a02](https://github.com/etalab-ia/codebox/commit/6368a02bcddaf176292b6748685d593c3c855da5))
* **import:** name the project directory in the trust approval hint ([efdeb1d](https://github.com/etalab-ia/codebox/commit/efdeb1dbbc3d55e8a7ba702db9e76f99ca7f7ef0))
* **init:** annoncer les touches de sélection des MultiSelect du wizard ([6fde5b7](https://github.com/etalab-ia/codebox/commit/6fde5b754c34554e056acf7ec03e25348bdc3128))
* **init:** announce MultiSelect selection keys in the wizard ([96ea5c5](https://github.com/etalab-ia/codebox/commit/96ea5c5f163e6c26a71ba899853f91233a681905))
* **init:** base local-only AGENTS.md ownership on actual zone presence ([f5d0655](https://github.com/etalab-ia/codebox/commit/f5d06556a05b1649ea3c9ebf000d9f89329c2462))
* **init:** check AGENTS.md entry type before reading for a zone ([f8aa563](https://github.com/etalab-ia/codebox/commit/f8aa5630dd840f52207ef65404b46457ef438fb1))
* **init:** recheck AGENTS.md at apply time for zones added after planning ([51665d2](https://github.com/etalab-ia/codebox/commit/51665d259e9f74f376bf84004c4131a19ab02fd7))
* **init:** resolve the GitHub origin before the approval question ([3d5618a](https://github.com/etalab-ia/codebox/commit/3d5618a7db35cd4bef67a063e31e34efae2706d9))
* **init:** résoudre l'origine GitHub avant la question d'approbation ([3467324](https://github.com/etalab-ia/codebox/commit/3467324fe25e0ca060d2ad79124c1c5f54dbea87))
* **init:** scan AGENTS.md for zone markers in bounded memory ([56cae38](https://github.com/etalab-ia/codebox/commit/56cae38b459b278d4afa98f72284049b1072ef75))
* probe AGENTS.md and host ports without unbounded reads or waits ([e85152c](https://github.com/etalab-ia/codebox/commit/e85152c5e9a76274d022fc17f4d739a874ea10f8))
* scope managed guidance and port probes to the microsandbox runtime ([d238885](https://github.com/etalab-ia/codebox/commit/d23888513a1744ba1c4022c5761d78e0123bbe2e))
* **skills:** address review on runtime parsing and AGENTS.md claim ([18c559d](https://github.com/etalab-ia/codebox/commit/18c559dbd054fd6c8e615b18d9d85c55d557e93a))
* **skills:** keep standing managed instructions in every versioned project ([7cb93c5](https://github.com/etalab-ia/codebox/commit/7cb93c575118b4202129d5bc7336247570e6c73f))
* **trust:** refuse symlinks in project path components ([294863b](https://github.com/etalab-ia/codebox/commit/294863b21f9caae7342b1e47f6d67dff03c93764))
* web preview reachable through the host port forward ([1ff037e](https://github.com/etalab-ia/codebox/commit/1ff037eb2fa69d37ceff32b1046676187cb8bf70))

## [0.10.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.9.0...codebox-v0.10.0) (2026-10-16)


### Features

* deliver approved secrets readable inside the guest ([a6bbfa5](https://github.com/etalab-ia/codebox/commit/a6bbfa596c91909df1f16c186727e8c33bb8dc8a))
* livrer les secrets approuvés lisibles dans le guest (phase 4) ([08a0a15](https://github.com/etalab-ia/codebox/commit/08a0a15991245fa8d7ba58605bcc3aaeb18005b8))


### Bug Fixes

* apply guest-readable deliveries on the reconcile path, and restrict before writing ([26b934b](https://github.com/etalab-ia/codebox/commit/26b934b5debc075044c56a331bea199ffa18869b))
* compare each file against the content wanted now, not the last write ([aa0807c](https://github.com/etalab-ia/codebox/commit/aa0807ce2a8478e762d8efb1552352d492c1e434))
* compare managed guest files by digest instead of reading them back ([2daf132](https://github.com/etalab-ia/codebox/commit/2daf132bdf01a9cc1d6ecf10aaa417678b77501e)), closes [#190](https://github.com/etalab-ia/codebox/issues/190)
* compare the digest as a field, not as a whole sha256sum line ([a28614d](https://github.com/etalab-ia/codebox/commit/a28614df4f4829e95bf0e24768516a9cf8f6535e))
* compare the ownership header as bytes, not through a shell variable ([fccde22](https://github.com/etalab-ia/codebox/commit/fccde22e2759d7314354c34a64c28aeab7decd25))
* compare the ownership header by digest over its exact bytes ([fa3e8e0](https://github.com/etalab-ia/codebox/commit/fa3e8e0f8854dde0fd1b8b11158827ed26e36e98))
* comparer les fichiers gérés par condensat au lieu de les relire ([a37243d](https://github.com/etalab-ia/codebox/commit/a37243d1cc19d2e43959c2c3502894d4b32116cb))
* deliver backslash-pair and trailing-backslash values raw instead of refusing them ([2a0f2d5](https://github.com/etalab-ia/codebox/commit/2a0f2d5beb381540ded5e0db463ae4c5f8baaf5a))
* deliver representable non-ASCII and refuse what no encoding carries ([a7b4e28](https://github.com/etalab-ia/codebox/commit/a7b4e28dd488b69bd053344dcdef1841619895f6))
* encode guest-readable values instead of writing them raw ([595a8c8](https://github.com/etalab-ia/codebox/commit/595a8c8045138ee75a942bb1b83addbefd95032b))
* keep the content digest out of argv and stop normalising the header ([16019ab](https://github.com/etalab-ia/codebox/commit/16019ab9231a920afe33ded135b579d69b3666f3))
* livrer les formes d'antislash que les chargeurs dotenv ne lisent pas exactement entre guillemets ([7294eed](https://github.com/etalab-ia/codebox/commit/7294eed7e95d20dd0b4695aef09dae960ce769ab))
* make the phase 4 probe controls able to fail ([e1b1efa](https://github.com/etalab-ia/codebox/commit/e1b1efa05344a190c3938e18ee5c6b26e9dc8754))
* refuse the backslash shapes dotenv loaders do not read exactly ([d2a5a99](https://github.com/etalab-ia/codebox/commit/d2a5a99d68730e6a1636a249d85e5a07afc3dea2))
* state the phase 4 snapshot finding without overclaiming ([9c37d72](https://github.com/etalab-ia/codebox/commit/9c37d72b8812efdf69a0183b947eeb3510c390dd))
* treat quotes and backticks as positional in the raw dotenv form ([d63e31e](https://github.com/etalab-ia/codebox/commit/d63e31eb7cc53c836167db4bc03f2659d198a2ea))

## [0.9.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.8.1...codebox-v0.9.0) (2026-10-15)


### Features

* configure Microsandbox ports per project ([e35a002](https://github.com/etalab-ia/codebox/commit/e35a002f419db1053f1c8e83b6a81bed55e93932))
* deliver approved project secrets through the Microsandbox proxy ([1951c7e](https://github.com/etalab-ia/codebox/commit/1951c7e6d29031e6c408b793f69ee47f902727da))
* exposer les secrets projet approuvés via le proxy Microsandbox (phase 2) ([8d5ecf1](https://github.com/etalab-ia/codebox/commit/8d5ecf1cdc2b98d48527ecd10e1635cc8a81a4f9))
* fichiers dotenv gérés dans le guest pour les secrets approuvés (phase 3) ([f483a80](https://github.com/etalab-ia/codebox/commit/f483a8003d7745dfe3cdea7363d5f366af51eebc))
* import project-scoped dotenv secrets ([77a5695](https://github.com/etalab-ia/codebox/commit/77a5695a68df228444145f7c6520234022029e0a))
* importer les secrets dotenv par projet (phase 1) ([0af624b](https://github.com/etalab-ia/codebox/commit/0af624b58b0d3eeb648bd0a398e74b741aaa5099))
* publish approved project secrets as managed guest dotenv files ([c830b14](https://github.com/etalab-ia/codebox/commit/c830b14fe258fe9e58ce68fb231ad212ea7d30e3))


### Bug Fixes

* bind the qualification transcript to what actually ran ([f9bbcee](https://github.com/etalab-ia/codebox/commit/f9bbceefd653b234284803c80850891b290ae16b))
* make the native runner cleanup exception-safe and document lab exposure ([0764fdc](https://github.com/etalab-ia/codebox/commit/0764fdca63b600d7f761490c071a9ce168276e9a))
* make the project secret inventory authoritative on every apply path ([f3d31f7](https://github.com/etalab-ia/codebox/commit/f3d31f7d11fc8254723d3bce3d251f0626b68c21))
* pin python-dotenv 1.2.2 for the loader qualification ([252e1de](https://github.com/etalab-ia/codebox/commit/252e1dea7cc1e14ea96b0c398241a4d5f164549d))
* retry Windows credential-file contention ([52b4f6f](https://github.com/etalab-ia/codebox/commit/52b4f6f7a5a4839d001a8073db42db3261411580))
* serialize fallback mutations and validate Windows blob sizes ([e18f46a](https://github.com/etalab-ia/codebox/commit/e18f46a0a8b42f9884ea42122618a0f91915f995))

## [0.8.1](https://github.com/etalab-ia/codebox/compare/codebox-v0.8.0...codebox-v0.8.1) (2026-10-07)


### Bug Fixes

* **install:** reattach setup wizard to the terminal under curl | sh ([faa8ecd](https://github.com/etalab-ia/codebox/commit/faa8ecd1c47f857c7602823e69a0da8331fe0d61))
* **setup:** aligner le message de fin sur le runtime par défaut ([0e8a4ca](https://github.com/etalab-ia/codebox/commit/0e8a4ca949aec179df8f92e9a1c8e7664c17f00a))
* **setup:** clarify default guest launch message ([37658ca](https://github.com/etalab-ia/codebox/commit/37658cae04222725f2bb1bfdf0170a1b20b9c594))

## [0.8.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.7.4...codebox-v0.8.0) (2026-10-07)


### Features

* **import:** importer les projets Albert Code (P19) ([1e4dbe0](https://github.com/etalab-ia/codebox/commit/1e4dbe0adf3d6615e5eb230b572c2452e45cf53e))
* préparer les diagnostics P21 avant release ([41129dc](https://github.com/etalab-ia/codebox/commit/41129dcb07af632cfb2ea5b87e4d9abb05dce36f))
* **setup:** add read-only P21 diagnostics report ([f0d963b](https://github.com/etalab-ia/codebox/commit/f0d963b0522b32f4205663a5044cc2bf117ca57a))


### Bug Fixes

* **doctor:** address P21 diagnostics review findings ([e7b9e47](https://github.com/etalab-ia/codebox/commit/e7b9e4715cba815d916f6752dc387b837a20859e))
* **doctor:** validate the selected Microsandbox runtime ([44312f9](https://github.com/etalab-ia/codebox/commit/44312f9c55543f6b3c7d7e1c62b87f0c075616b8))
* **import:** reconcile existing Albert Code imports ([2e5a787](https://github.com/etalab-ia/codebox/commit/2e5a7874967091c68052fe31b8069b08857e80ad))
* **init:** keep skill catalogue lines under 80 columns ([55803d0](https://github.com/etalab-ia/codebox/commit/55803d045d746653d5cbe2a0eb38bdae85e1b96e))
* **init:** rendre la liste des compétences compacte et lisible ([1e95748](https://github.com/etalab-ia/codebox/commit/1e957484d309050f425cd4e44663ea312a62de84))
* **qualification:** address remaining doctor review comments ([5ca6f62](https://github.com/etalab-ia/codebox/commit/5ca6f62fcb734688f2c78bb670bda50fbc24d825))

## [0.7.4](https://github.com/etalab-ia/codebox/compare/codebox-v0.7.3...codebox-v0.7.4) (2026-10-05)


### Bug Fixes

* **ci:** donner au dispatch manuel le contexte de dépôt à gh ([473bd81](https://github.com/etalab-ia/codebox/commit/473bd811128d24674296eecf5292f65f40b2d416))
* **ci:** donner au dispatch manuel le contexte de dépôt à gh ([6807ba5](https://github.com/etalab-ia/codebox/commit/6807ba5196506b556cfe5532cac06e04dec2470e))
* **project:** réessayer la lecture du lock face aux échecs transitoires ([bfd77a8](https://github.com/etalab-ia/codebox/commit/bfd77a8c5afe3e1daee1cfe27212d0645fffcef0))
* **project:** réessayer la lecture du lock face aux échecs transitoires ([b0baac6](https://github.com/etalab-ia/codebox/commit/b0baac6e47329166e54dc4eb3ada7387a2799211))
* **project:** remonter la dernière erreur de lecture du lock à l'expiration ([b5088fa](https://github.com/etalab-ia/codebox/commit/b5088fa18ad7ab73fae6b3b636bd33ec7b14705e))

## [0.7.3](https://github.com/etalab-ia/codebox/compare/codebox-v0.7.2...codebox-v0.7.3) (2026-10-05)


### Bug Fixes

* **mcp:** empêcher l'installateur gh de terminer le script de démarrage ([#123](https://github.com/etalab-ia/codebox/issues/123)) ([c0f2d46](https://github.com/etalab-ia/codebox/commit/c0f2d4624f209e57359102611e3b51d7cbd52628))

## [0.7.2](https://github.com/etalab-ia/codebox/compare/codebox-v0.7.1...codebox-v0.7.2) (2026-10-05)


### Bug Fixes

* **mcp:** corriger le nom de la variable npm dans le script de préparation ([#121](https://github.com/etalab-ia/codebox/issues/121)) ([fdd532a](https://github.com/etalab-ia/codebox/commit/fdd532a892bc5f5ac874bd13e5ffef8258015b1a))

## [0.7.1](https://github.com/etalab-ia/codebox/compare/codebox-v0.7.0...codebox-v0.7.1) (2026-10-05)


### Bug Fixes

* **mcp:** réparer les épingles apt du profil navigateur (P16) ([#118](https://github.com/etalab-ia/codebox/issues/118)) ([7e87dfe](https://github.com/etalab-ia/codebox/commit/7e87dfed3d13b334611d8dd2ffaca75fb0189600))

## [0.7.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.6.0...codebox-v0.7.0) (2026-10-05)


### Features

* **mcp:** ajouter les MCP navigateur et unifier init (P16/P17) ([#116](https://github.com/etalab-ia/codebox/issues/116)) ([bfc1748](https://github.com/etalab-ia/codebox/commit/bfc17488d59dac8be2e818565326a385723e2cff))
* **mcp:** configurer data.gouv et Context7 (P15) ([#115](https://github.com/etalab-ia/codebox/issues/115)) ([7fe8bbe](https://github.com/etalab-ia/codebox/commit/7fe8bbe488f6325be5b9cc457a3b484edea1baa9))
* **skills:** gérer les skills projet épinglées (P14) ([#113](https://github.com/etalab-ia/codebox/issues/113)) ([26e942e](https://github.com/etalab-ia/codebox/commit/26e942efdb8437472aec427184a28eeac4c155b5))

## [0.6.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.5.0...codebox-v0.6.0) (2026-10-02)


### Features

* **github:** activer le workflow invité approuvé (P13) ([#105](https://github.com/etalab-ia/codebox/issues/105)) ([51879f7](https://github.com/etalab-ia/codebox/commit/51879f7221499ff15ca87ae52857c368ee53feb6))


### Bug Fixes

* **tests:** fiabiliser le laboratoire d identifiants P02 ([#110](https://github.com/etalab-ia/codebox/issues/110)) ([a819397](https://github.com/etalab-ia/codebox/commit/a819397bc10997bc16898f7b74e168e88a417a1e))

## [0.5.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.4.2...codebox-v0.5.0) (2026-09-28)


### Features

* **auth:** stocker les identifiants hors des projets (P08, en cours) ([7bf35b5](https://github.com/etalab-ia/codebox/commit/7bf35b59af060897b6d62419c6eda7ee109c96a4))
* **auth:** stocker les identifiants hors des projets (P08) ([4103ff5](https://github.com/etalab-ia/codebox/commit/4103ff505656cc46b54bc45e0419f72fa8c5cea5))
* **auth:** stocker les identifiants hors des projets (P08) ([b3357d1](https://github.com/etalab-ia/codebox/commit/b3357d12a963bc19b8f1ad22fea93f0b92db4316))
* **config:** résolution typée, schémas et provenance (P04) ([abbc6ef](https://github.com/etalab-ia/codebox/commit/abbc6ef54ea0a843b6dfd9dc7a19ae71391d7268))
* **config:** résolution typée, schémas et provenance (P04) ([24e9869](https://github.com/etalab-ia/codebox/commit/24e98693a78892c94ae0f1a0c8927581d8b98eb2))
* **init:** commande just-code init et contrat non-TTY (P12b-3) ([#97](https://github.com/etalab-ia/codebox/issues/97)) ([651d22d](https://github.com/etalab-ia/codebox/commit/651d22d2fcbb659baa0b7df937d0cd54493be483))
* **init:** moteur de configuration minimale d'un projet (P12b-2) ([#96](https://github.com/etalab-ia/codebox/issues/96)) ([2c88b55](https://github.com/etalab-ia/codebox/commit/2c88b55d42a7241f986a90b906302bd3bef20e3a))
* **init:** proposer la configuration au lancement nu (P12b-4) ([#98](https://github.com/etalab-ia/codebox/issues/98)) ([b7cd92a](https://github.com/etalab-ia/codebox/commit/b7cd92aad59bf6686a4e176d69b73ff11727ff79))
* Microsandbox en isolation complète par défaut (P12a) ([#94](https://github.com/etalab-ia/codebox/issues/94)) ([c4a0ec4](https://github.com/etalab-ia/codebox/commit/c4a0ec4ab9e84b3039def633ebfb98d0d49cfcb7))
* **msb:** honorer le dimensionnement invité du projet (P12b-1) ([#95](https://github.com/etalab-ia/codebox/issues/95)) ([e801216](https://github.com/etalab-ia/codebox/commit/e801216b7f7da2f253be6badd4d22234a34c9bd7))
* **runtime:** réconciliation et redémarrage non destructif (P07) ([bd4e068](https://github.com/etalab-ia/codebox/commit/bd4e06882f812754331ba033b565966f4606beca))
* **runtime:** réconciliation et redémarrage non destructif (P07) ([a7e1e79](https://github.com/etalab-ia/codebox/commit/a7e1e79cd4f44d5b163898d148439c31b52c0687))
* **tui:** reprendre la dernière session à l’attachement (P12) ([#102](https://github.com/etalab-ia/codebox/issues/102)) ([aae0599](https://github.com/etalab-ia/codebox/commit/aae059927308e1ae0e96c8e26a6ff7fab6271bc5))
* **workspace:** clone invité scellé, transfert filtré et retour revu (P22) ([#91](https://github.com/etalab-ia/codebox/issues/91)) ([b8ce092](https://github.com/etalab-ia/codebox/commit/b8ce0922e92e08c6001c8a097c76a1bd2ec14fe8))


### Bug Fixes

* **auth:** assouplir l'assertion de permissions pour Windows (PR [#86](https://github.com/etalab-ia/codebox/issues/86)) ([643743d](https://github.com/etalab-ia/codebox/commit/643743d94dc8b41cdef41f5fdc3629bd5ded893a))
* **auth:** baseline Go 1.22 preserve et collections Secret Service verrouillees (revue Codex PR [#86](https://github.com/etalab-ia/codebox/issues/86)) ([cb7b10d](https://github.com/etalab-ia/codebox/commit/cb7b10d1d6cbddcd29895b03a2cc8ef1c395b034))
* **auth:** branche darwin et disposition CREDENTIALW exacte (revue Codex PR [#86](https://github.com/etalab-ia/codebox/issues/86)) ([f97f4b0](https://github.com/etalab-ia/codebox/commit/f97f4b0b046292e0d4ad4e66903e132f38d5931b))
* **auth:** fake Runner partage entre les tests d'adaptateurs (PR [#86](https://github.com/etalab-ia/codebox/issues/86)) ([05ff75a](https://github.com/etalab-ia/codebox/commit/05ff75acf91fc9a43baa33b4a648d4cb241daa21))
* **auth:** fixture denied distinct de locked (PR [#86](https://github.com/etalab-ia/codebox/issues/86)) ([3ad0696](https://github.com/etalab-ia/codebox/commit/3ad0696ff100ee942eb4320aa8e51ab7e876592d))
* **auth:** injection op/cible dans le script wincred, statut et remove honnetes (revue Codex PR [#86](https://github.com/etalab-ia/codebox/issues/86)) ([ac6ffac](https://github.com/etalab-ia/codebox/commit/ac6ffac66c64bd98d58ffd3abd34c4b93afc45df))
* **config:** traversée des flags, erreurs de manifeste et valeurs par défaut dans explain ([bbe2e80](https://github.com/etalab-ia/codebox/commit/bbe2e802bec496df2be6ddfcc116367c05472672))
* **lifecycle:** balayages globaux par instance (revue Codex PR [#81](https://github.com/etalab-ia/codebox/issues/81)) ([7fa14a4](https://github.com/etalab-ia/codebox/commit/7fa14a47821a3b6842dab2bc88efd5bda0e2d125))
* **msb:** ne pas publier le port serveur en isolation full (P12) ([#101](https://github.com/etalab-ia/codebox/issues/101)) ([81fd115](https://github.com/etalab-ia/codebox/commit/81fd115599a65daff4ce3876eef7787ed26606a7))
* **msb:** refuser un montage sans preuve de propriété ([#92](https://github.com/etalab-ia/codebox/issues/92)) ([#100](https://github.com/etalab-ia/codebox/issues/100)) ([4278a2a](https://github.com/etalab-ia/codebox/commit/4278a2adac49bdc5f9c68e427e0f67aeff8f1c5c))
* **registry:** verrouiller les mutations entre processus (revue Codex PR [#80](https://github.com/etalab-ia/codebox/issues/80)) ([908b9d6](https://github.com/etalab-ia/codebox/commit/908b9d6444fe31b2a640e191f6a92eb866b4e138))
* **runtime:** brancher la réconciliation et durcir le journal (revue Codex PR [#82](https://github.com/etalab-ia/codebox/issues/82)) ([0a5fa3b](https://github.com/etalab-ia/codebox/commit/0a5fa3bf88def3afc9427f40dffa08a5ac259a91))
* **tui:** éviter l’erreur de reprise au premier lancement ([#103](https://github.com/etalab-ia/codebox/issues/103)) ([49fe767](https://github.com/etalab-ia/codebox/commit/49fe767f182e740b286c0d9671495f07f4e7fab6))

## [0.4.2](https://github.com/etalab-ia/codebox/compare/codebox-v0.4.1...codebox-v0.4.2) (2026-09-22)


### Bug Fixes

* **agent-vm:** créer le workspace avant la pré-vérification du restart ([1ad8bc9](https://github.com/etalab-ia/codebox/commit/1ad8bc92b3674ccbfdef42a12db02c313688e5e7))
* **agent-vm:** passerelle de sécurité workspace au démarrage et au restart ([7952609](https://github.com/etalab-ia/codebox/commit/7952609575891730c82736f93e60b57cd8a45ba8))
* **agent-vm:** passerelle de sécurité workspace au démarrage et au restart ([9dd9eb9](https://github.com/etalab-ia/codebox/commit/9dd9eb9207cdbd846d8706ff27368b6b3defa645))
* **microsandbox:** lancement du script au premier démarrage et détection du montage obsolète ([e7f2dc0](https://github.com/etalab-ia/codebox/commit/e7f2dc0ce6414f71121da1614bcc3cebd55062a9))
* **microsandbox:** launch the start script after creation ([a130914](https://github.com/etalab-ia/codebox/commit/a130914891350f1c53cfe6b9a442415504a35cae))
* **microsandbox:** lire le montage /workspace depuis le document persisté ([8272c0e](https://github.com/etalab-ia/codebox/commit/8272c0ebc466b0300bd943e77e86a9102e94ae4c))
* **microsandbox:** relancer un invité full inactif et refuser un timeout invalide ([a549f93](https://github.com/etalab-ia/codebox/commit/a549f93c0d98c5e244797bc1cb4e036036b26386))
* **microsandbox:** revérifier la préparation full sur sandbox existant ([867ee50](https://github.com/etalab-ia/codebox/commit/867ee5029b85d3eaee91d307d80a5ad01b0dd96e))
* **microsandbox:** vérifier la survie du lanceur et attendre la toolchain en mode full ([b94801a](https://github.com/etalab-ia/codebox/commit/b94801a4777c1a71b0b66f00cede6e1c108557e5))
* **tart,msb:** créer le workspace avant la pré-vérification du restart ([1278ace](https://github.com/etalab-ia/codebox/commit/1278ace19548e00edf02b6e80a2eb8da1ed95c0d))
* **tart,msb:** créer le workspace avant la pré-vérification du restart ([1ee7c57](https://github.com/etalab-ia/codebox/commit/1ee7c5725031e4952d5443734e6307db87bc389f))
* **tart,msb:** valider la configuration avant le Clean du restart ([f5b6d6b](https://github.com/etalab-ia/codebox/commit/f5b6d6b97bc3d6ab2f8d90a54706e029a49348f1))

## [0.4.1](https://github.com/etalab-ia/codebox/compare/codebox-v0.4.0...codebox-v0.4.1) (2026-09-21)


### Bug Fixes

* **microsandbox:** le garde legacy lit vraiment la config persistée ([dbe32df](https://github.com/etalab-ia/codebox/commit/dbe32dfae7976f69d603b6738de1528cefd0b552))
* **microsandbox:** protéger la clé Albert en mode full ([ec770a8](https://github.com/etalab-ia/codebox/commit/ec770a80a82d3b0032d63d8df231c826a434fa23))
* **microsandbox:** protéger la clé Albert en mode full ([2d18e50](https://github.com/etalab-ia/codebox/commit/2d18e507a3788c7d3283147af5b417688bf3e113))

## [0.4.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.3.0...codebox-v0.4.0) (2026-09-20)


### Features

* add --isolation backend|full execution mode ([db3d1ab](https://github.com/etalab-ia/codebox/commit/db3d1ab983e0a0bd96a048797dae50a371171ba7))
* add --isolation backend|full execution mode ([1b02925](https://github.com/etalab-ia/codebox/commit/1b02925a2a49b2e8ee97cd0ddc6bbc0ec4d35f1b))
* **agent-vm:** build the base template ourselves ([e063e33](https://github.com/etalab-ia/codebox/commit/e063e331dec72a558c0fcb5d3c62cc65b5f1012c))
* **agent-vm:** construire nous-mêmes le template de base ([ae7fd5f](https://github.com/etalab-ia/codebox/commit/ae7fd5f8f863a2673f2ed2099092ed7b1575bf28))


### Bug Fixes

* address review findings on the isolation mode ([3bd6681](https://github.com/etalab-ia/codebox/commit/3bd6681584218efdf68ea39457e487c36c6ef9da))
* **agent-vm:** address review findings on the base template ([6e14779](https://github.com/etalab-ia/codebox/commit/6e14779d1ba23f888846442d24307ba00ea64b7d))
* **msb:** télécharge le runtime depuis la release amont immuable ([1db0d15](https://github.com/etalab-ia/codebox/commit/1db0d151ac755be4b87bc0e2f84366483ec2310d))
* **msb:** télécharge le runtime depuis la release amont immuable ([2e9fd19](https://github.com/etalab-ia/codebox/commit/2e9fd1991b77a609377883f18db2a31fd258b4ba))

## [0.3.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.2.0...codebox-v0.3.0) (2026-09-17)


### Features

* add agent-vm runtime (Lima VM backend) ([3de7848](https://github.com/etalab-ia/codebox/commit/3de78483a6ee8bba2ecd50cfb8d370bf3fc6417e))
* aligner le CLI sur la version TypeScript et ajouter CI/CD ([96a09be](https://github.com/etalab-ia/codebox/commit/96a09bedf1528723e2d99cf3a6c6c7df131b57b6))
* default to microsandbox and reject docker/tart on Windows ([b5a3467](https://github.com/etalab-ia/codebox/commit/b5a346789f9b1ed2d99791835c78b61f797a1734))
* embarquer les ressources de runtime dans le binaire Go ([900029c](https://github.com/etalab-ia/codebox/commit/900029cf503dddcdd16c42b3d622bf2722bdc9e8))
* embed the Microsandbox Go SDK ([0249ebe](https://github.com/etalab-ia/codebox/commit/0249ebe90ae72ad1d6776bc751dd21321849b61c))
* héberger et vérifier le runtime Microsandbox ([2d10395](https://github.com/etalab-ia/codebox/commit/2d10395555f70c7c102977cb649d761821f18058))
* identifier le binaire avec la commande version ([9c96b4f](https://github.com/etalab-ia/codebox/commit/9c96b4faac66e45a4fa73375b2610ed6253e36b5))
* intégrer le SDK Go Microsandbox ([d59bc44](https://github.com/etalab-ia/codebox/commit/d59bc44e5550c5239d13d33f635794a9716555ef))
* **msb:** passage du runtime Microsandbox en v0.7.0 ([389adb3](https://github.com/etalab-ia/codebox/commit/389adb3794b04ec17c4cad5e48acacce7aadba47))
* **msb:** passe le runtime Microsandbox en v0.7.0 ([3e512fb](https://github.com/etalab-ia/codebox/commit/3e512fb20c468cf0548f2a24afc0e499e632c0f9))
* porter Docker et Microsandbox en Go et retirer le justfile ([1182ba7](https://github.com/etalab-ia/codebox/commit/1182ba7708db7f646e0b4e389cac5d38175507bb))
* porter just-code en Go (remplace le justfile) ([e252382](https://github.com/etalab-ia/codebox/commit/e252382a0cb3296f1a5b6551bab86cc0b33040c7))
* porter le bootstrap Tart en Go et supprimer le dernier script shell ([2f5987d](https://github.com/etalab-ia/codebox/commit/2f5987d526423648805047c2c7795b919bbdefdd))
* porter le cycle de vie Tart en Go (durcissement) ([cd250b3](https://github.com/etalab-ia/codebox/commit/cd250b31f93e81161b7530eb3b346dd6a9589e3d))
* refuse startup when the workspace contains secrets ([05fc252](https://github.com/etalab-ia/codebox/commit/05fc252aa3f08af52f2f9abaf116a9073773b276))
* renomme les tags de release (runtime et application) ([2f63e0b](https://github.com/etalab-ia/codebox/commit/2f63e0bbbe1b5718ac9b9827d690a7634aa077ef))
* renomme les tags de release du runtime et de l'application ([79286a5](https://github.com/etalab-ia/codebox/commit/79286a5aa1d0cd61bfdb5569d4002b9260c93cdf))
* retirer la commande build ([d348223](https://github.com/etalab-ia/codebox/commit/d348223770585671b69e847e8ab60a3725f203e5))
* runtime agent-vm (VM Lima persistante) ([a8449d5](https://github.com/etalab-ia/codebox/commit/a8449d59f7567f5a03a080f5480dbfbe9dfad20b))
* supporter Windows via le runtime Microsandbox (msb) ([b2a9d6d](https://github.com/etalab-ia/codebox/commit/b2a9d6d93d71598f510d47a9d72b81183840e273))
* supporter Windows via le runtime Microsandbox (msb) ([46a6ceb](https://github.com/etalab-ia/codebox/commit/46a6ceb8ba5411ab06526f5849b6752a5c7804d3))
* vérifier le runtime Microsandbox hébergé ([0907d4d](https://github.com/etalab-ia/codebox/commit/0907d4dfc51f022a25d3da9c5d0b1ff290205b32))


### Bug Fixes

* accepter les reponses /provider plus larges que 4 MiB ([baf6746](https://github.com/etalab-ia/codebox/commit/baf67461901bc1b58c31244fa7d34e98ff299d28))
* address codex review on release assets and docs ([83c3916](https://github.com/etalab-ia/codebox/commit/83c39161a6994e91adf7d8962db9f220d3e084cf))
* address Codex review on Windows attach and test portability ([b9b64cc](https://github.com/etalab-ia/codebox/commit/b9b64cc14f810ba7bd6e619fe1daa4661a8939cf))
* address review findings on the agent-vm backend ([08f5c8d](https://github.com/etalab-ia/codebox/commit/08f5c8dda85370bee720c5484742d6b4d2ffbfe9))
* address review findings on the workspace gate ([8476903](https://github.com/etalab-ia/codebox/commit/8476903068377c80dcaad81c8a1ed6915f67d3db))
* align Windows process detachment with Setsid semantics ([b81f4f1](https://github.com/etalab-ia/codebox/commit/b81f4f133c42c4576b7e0612e6f4585d411ad58d))
* annoncer la suppression avant de detruire un sandbox ([84092b7](https://github.com/etalab-ia/codebox/commit/84092b77e64c2c11b9f31c5b54e4d5ecaf84f40a))
* clean up the legacy Docker container on upgrade ([15d6b81](https://github.com/etalab-ia/codebox/commit/15d6b81cf75e5183904b61b762a1d9763b963b37))
* diagnostiquer un backend deja demarre mais non sain ([327a6a8](https://github.com/etalab-ia/codebox/commit/327a6a8d3c11421f0da853594007566f2c5eae6e))
* doctor doit toujours dire ce qu il a trouve ([542765d](https://github.com/etalab-ia/codebox/commit/542765db6e9fefbfca9200dbc86e2e7f7c53cf85))
* faire de just-code sans argument l equivalent de just code ([ef4b390](https://github.com/etalab-ia/codebox/commit/ef4b390d2df85ce3f1ec518bb4fadde0bf8565bc))
* livrer stdin aux processus detaches et chercher .env aupres du binaire ([7f9b5d5](https://github.com/etalab-ia/codebox/commit/7f9b5d54016d9a51a73325850e0a58f5ecb73515))
* **msb-watch:** titre la release miroir avec le nom du tag ([e131f29](https://github.com/etalab-ia/codebox/commit/e131f294df93ea97677f9f3579ee490696c49c9c))
* **msb-watch:** titre la release miroir avec le nom du tag ([504b71b](https://github.com/etalab-ia/codebox/commit/504b71b4925ba4295b94297bdd8a5daf1e1bf2ed))
* **msb:** pointe l'URL du runtime vers microsandbox-v0.7.0 ([29d5015](https://github.com/etalab-ia/codebox/commit/29d5015293d4ae64ba3e39cb0d3523be78c0d191))
* normalize scanned workspace paths to forward slashes ([bb247bf](https://github.com/etalab-ia/codebox/commit/bb247bf40e73849febf16c1d826d42a309c5088a))
* reparer le backend microsandbox et aligner la config sur le port TS ([77c7efd](https://github.com/etalab-ia/codebox/commit/77c7efdd426280bc25757c3a718f898f5c9255a7))
* reset release-please to start at 0.1.0 instead of 1.0.0 ([bad1648](https://github.com/etalab-ia/codebox/commit/bad1648f9d1eac96d97426e8772f4fce3b012541))
* **test:** skip 0600 permission assert on Windows ([7d1ad98](https://github.com/etalab-ia/codebox/commit/7d1ad989225c8521c9d0f86d5aa88c3adaf569b7))
* utilise le champ component supporté par release-please ([50ba13e](https://github.com/etalab-ia/codebox/commit/50ba13e1e6a7b6dec9224310479b2c86b5037588))

## [0.2.0](https://github.com/etalab-ia/codebox/compare/codebox-v0.1.0...codebox-v0.2.0) (2026-09-15)


### Features

* intégrer le SDK Go Microsandbox ([d59bc44](https://github.com/etalab-ia/codebox/commit/d59bc44e5550c5239d13d33f635794a9716555ef))


### Bug Fixes

* address codex review on release assets and docs ([83c3916](https://github.com/etalab-ia/codebox/commit/83c39161a6994e91adf7d8962db9f220d3e084cf))

## 0.1.0 (2026-09-14)


### Features

* aligner le CLI sur la version TypeScript et ajouter CI/CD ([96a09be](https://github.com/etalab-ia/codebox/commit/96a09bedf1528723e2d99cf3a6c6c7df131b57b6))
* default to microsandbox and reject docker/tart on Windows ([b5a3467](https://github.com/etalab-ia/codebox/commit/b5a346789f9b1ed2d99791835c78b61f797a1734))
* embarquer les ressources de runtime dans le binaire Go ([900029c](https://github.com/etalab-ia/codebox/commit/900029cf503dddcdd16c42b3d622bf2722bdc9e8))
* identifier le binaire avec la commande version ([9c96b4f](https://github.com/etalab-ia/codebox/commit/9c96b4faac66e45a4fa73375b2610ed6253e36b5))
* porter Docker et Microsandbox en Go et retirer le justfile ([1182ba7](https://github.com/etalab-ia/codebox/commit/1182ba7708db7f646e0b4e389cac5d38175507bb))
* porter just-code en Go (remplace le justfile) ([e252382](https://github.com/etalab-ia/codebox/commit/e252382a0cb3296f1a5b6551bab86cc0b33040c7))
* porter le bootstrap Tart en Go et supprimer le dernier script shell ([2f5987d](https://github.com/etalab-ia/codebox/commit/2f5987d526423648805047c2c7795b919bbdefdd))
* porter le cycle de vie Tart en Go (durcissement) ([cd250b3](https://github.com/etalab-ia/codebox/commit/cd250b31f93e81161b7530eb3b346dd6a9589e3d))
* retirer la commande build ([d348223](https://github.com/etalab-ia/codebox/commit/d348223770585671b69e847e8ab60a3725f203e5))
* supporter Windows via le runtime Microsandbox (msb) ([b2a9d6d](https://github.com/etalab-ia/codebox/commit/b2a9d6d93d71598f510d47a9d72b81183840e273))
* supporter Windows via le runtime Microsandbox (msb) ([46a6ceb](https://github.com/etalab-ia/codebox/commit/46a6ceb8ba5411ab06526f5849b6752a5c7804d3))


### Bug Fixes

* accepter les reponses /provider plus larges que 4 MiB ([baf6746](https://github.com/etalab-ia/codebox/commit/baf67461901bc1b58c31244fa7d34e98ff299d28))
* address Codex review on Windows attach and test portability ([b9b64cc](https://github.com/etalab-ia/codebox/commit/b9b64cc14f810ba7bd6e619fe1daa4661a8939cf))
* align Windows process detachment with Setsid semantics ([b81f4f1](https://github.com/etalab-ia/codebox/commit/b81f4f133c42c4576b7e0612e6f4585d411ad58d))
* annoncer la suppression avant de detruire un sandbox ([84092b7](https://github.com/etalab-ia/codebox/commit/84092b77e64c2c11b9f31c5b54e4d5ecaf84f40a))
* clean up the legacy Docker container on upgrade ([15d6b81](https://github.com/etalab-ia/codebox/commit/15d6b81cf75e5183904b61b762a1d9763b963b37))
* diagnostiquer un backend deja demarre mais non sain ([327a6a8](https://github.com/etalab-ia/codebox/commit/327a6a8d3c11421f0da853594007566f2c5eae6e))
* doctor doit toujours dire ce qu il a trouve ([542765d](https://github.com/etalab-ia/codebox/commit/542765db6e9fefbfca9200dbc86e2e7f7c53cf85))
* faire de just-code sans argument l equivalent de just code ([ef4b390](https://github.com/etalab-ia/codebox/commit/ef4b390d2df85ce3f1ec518bb4fadde0bf8565bc))
* livrer stdin aux processus detaches et chercher .env aupres du binaire ([7f9b5d5](https://github.com/etalab-ia/codebox/commit/7f9b5d54016d9a51a73325850e0a58f5ecb73515))
* reparer le backend microsandbox et aligner la config sur le port TS ([77c7efd](https://github.com/etalab-ia/codebox/commit/77c7efdd426280bc25757c3a718f898f5c9255a7))
* reset release-please to start at 0.1.0 instead of 1.0.0 ([bad1648](https://github.com/etalab-ia/codebox/commit/bad1648f9d1eac96d97426e8772f4fce3b012541))

## Changelog

All notable changes to this project will be documented in this file.
