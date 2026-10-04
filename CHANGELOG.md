# Changelog

## Unreleased

### Features

* **tui:** chart a vApp in its workspace by adding up its member VMs: summed CPU against the vApp's CPU limit and summed active memory, plus each member's peak CPU, ready and limited time, with `s` to sort busiest first; members are read in batches under vCenter's 64-metric query limit
* **assessment:** capture vApps as their own `vapp` collection (inventory schema 20) with their CPU/memory allocation, status colours and startup order, and list them on `vRP` after the resource pools as RVTools does; `vapp show` and the TUI vApp workspace show the allocation and startup order ([#276](https://github.com/Easonliuuuuu/vsfleet/issues/276))
* **assessment:** add `assessment metadata`, a fixed-schema export of stored tags and custom attributes, `assessment metadata-report` for saved TOML report definitions with membership comparison (`--base`), and an opt-in `vsfleetMetadata` export sheet handled by the sharing profiles ([#217](https://github.com/Easonliuuuuu/vsfleet/issues/217))
* **assessment:** collect bounded, opt-in VM performance history (`assessment perf`) with a conservative sizing signal, stored apart from inventory runs and shown in `assessment report` and a `vsfleetPerformance` export sheet ([#214](https://github.com/Easonliuuuuu/vsfleet/issues/214))
* **assessment:** attribute datastore growth and project free-space thresholds
* **health:** add an absolute datastore free-space floor

### Bug Fixes

* **tui:** keep a chart title's statistics at widths where they fit with one column to spare; the title used to drop them altogether
* **assessment:** count metadata coverage over every object in a collection instead of the first one, so a partial tag failure is reported as `partial`; report metadata sources as `denied`, `unsupported` or `not_recorded` instead of folding them into `unavailable` ([#217](https://github.com/Easonliuuuuu/vsfleet/issues/217))
* **assessment:** make `assessment perf collect -o json` exit non-zero when collection fails for every context, matching the text output
* **assessment:** stop reporting a clean orphan scan when datastore browse evidence is missing ([#115](https://github.com/Easonliuuuuu/vsfleet/issues/115))
* **vsphere:** make tag lookups honor the context's TLS policy and transport, so `--tls thumbprint` no longer fails the tagging endpoint against a private-CA certificate ([#190](https://github.com/Easonliuuuuu/vsfleet/issues/190))
* **vsphere:** pass the connection test with the built-in ReadOnly role instead of requiring `Sessions.ValidateSession` ([#189](https://github.com/Easonliuuuuu/vsfleet/issues/189))
* **decommission:** rename the clean verdict from `ready` to `no-blockers` so it never reads as authorization to delete ([#98](https://github.com/Easonliuuuuu/vsfleet/issues/98))

## [0.6.1](https://github.com/Easonliuuuuu/vsfleet/compare/v0.6.0...v0.6.1) (2026-10-02)


### Features

* **assessment:** add opt-in datastore file inventory and vFileInfo export ([1ccce3f](https://github.com/Easonliuuuuu/vsfleet/commit/1ccce3f5cd947863438f891aea12002905e02f8d)), closes [#219](https://github.com/Easonliuuuuu/vsfleet/issues/219)
* **assessment:** add stable metadata export and saved tag-based reports ([0dc29b1](https://github.com/Easonliuuuuu/vsfleet/commit/0dc29b11b4790d08c46fb4a309e8e9f453388599)), closes [#217](https://github.com/Easonliuuuuu/vsfleet/issues/217)
* **assessment:** capture source identity and export RVTools vSource sheet ([f0bce2f](https://github.com/Easonliuuuuu/vsfleet/commit/f0bce2fa11e332d1d5068b932851fbc351df058a)), closes [#218](https://github.com/Easonliuuuuu/vsfleet/issues/218)
* **assessment:** collect bounded VM performance history for sizing ([281c42f](https://github.com/Easonliuuuuu/vsfleet/commit/281c42fce47f0e48b352e554b245efd1a434ce51))
* **assessment:** collect opt-in license metadata and export key-safe vLicense ([ab073c8](https://github.com/Easonliuuuuu/vsfleet/commit/ab073c886a9d593d30feaf3339cd8da718300221)), closes [#220](https://github.com/Easonliuuuuu/vsfleet/issues/220)
* **cli:** add datastore files list/find commands ([1d06f6e](https://github.com/Easonliuuuuu/vsfleet/commit/1d06f6eb53bd3e63b74be86d6f5874ab9fbccfff))
* **cli:** add show commands and expose collected infrastructure subresources ([73b363c](https://github.com/Easonliuuuuu/vsfleet/commit/73b363cd7084d2ca7e3599763de03f0998d42bee)), closes [#158](https://github.com/Easonliuuuuu/vsfleet/issues/158)
* **cli:** make the command tree discoverable from the terminal ([4caf716](https://github.com/Easonliuuuuu/vsfleet/commit/4caf7161b632e63b434b4f0140b012e3d4642207))
* **demo:** generate a production-scale estate with five-run history ([7ac10d1](https://github.com/Easonliuuuuu/vsfleet/commit/7ac10d1ce2c442ae037535389896b339a9c58e00))
* **history.go:** lay out Trends by section and let it scroll ([b984c81](https://github.com/Easonliuuuuu/vsfleet/commit/b984c814ccc79346b6332d875b88dff1dbadff00))
* **import:** add RVTools XLSX import into assessment history ([6321fcc](https://github.com/Easonliuuuuu/vsfleet/commit/6321fcc896b00dc40af886f7f86700b95ba920e1))
* **query:** add metadata-aware inventory filtering ([7f47d52](https://github.com/Easonliuuuuu/vsfleet/commit/7f47d527fe205621457c52904af2bbf91a80a35b))
* **report:** add scoped sharing profiles and deterministic pseudonymization ([66e5fd2](https://github.com/Easonliuuuuu/vsfleet/commit/66e5fd2a6b95e3b72a23c77ce901d4ad22f8ad0f))
* **report:** export vCD and vUSB worksheets ([ca4f9da](https://github.com/Easonliuuuuu/vsfleet/commit/ca4f9dad337250decf2569fb6c679d28ff4238b4))
* **rvimport:** never let missing workbook evidence improve a verdict ([426febf](https://github.com/Easonliuuuuu/vsfleet/commit/426febfd81957134cf186914d2a54ba8b5c775b1))
* **sizing:** evaluate destination capacity with offline sizing scenarios ([04dc596](https://github.com/Easonliuuuuu/vsfleet/commit/04dc596bb82de6419c8225a8bcc94c24cd53ee74))
* **ssh:** discover OpenSSH aliases and remember per-VM destinations ([d69a0f7](https://github.com/Easonliuuuuu/vsfleet/commit/d69a0f726cc9ec662904bd41ca8f02023d1b67a8))
* **ssh:** route VM handoff by context and destination CIDR ([c72b808](https://github.com/Easonliuuuuu/vsfleet/commit/c72b8087371f2637cc966a16f0484cc5bd84aa62)), closes [#178](https://github.com/Easonliuuuuu/vsfleet/issues/178)
* **tui:** add remembered SSH identity selection ([ffb422b](https://github.com/Easonliuuuuu/vsfleet/commit/ffb422b59b3d199f04484b609ab85ea590fbe59f))
* **tui:** move selection with arrow keys while the filter is focused ([eb3217a](https://github.com/Easonliuuuuu/vsfleet/commit/eb3217a897c049e1d84c5dae88acad019d9b4461))
* **tui:** show the real ssh user and let it be chosen per machine ([e55e6b6](https://github.com/Easonliuuuuu/vsfleet/commit/e55e6b626e6c236e6ceedf654ce1b2d2e90a2f85))


### Bug Fixes

* **assessment_perf.go:** fail perf collect -o json when every context fails ([b6573ee](https://github.com/Easonliuuuuu/vsfleet/commit/b6573ee36eec9298743eb3516d3a202d7cd0cd02))
* **assessment:** mark permission-hidden inventory partial, not clean ([31ca81d](https://github.com/Easonliuuuuu/vsfleet/commit/31ca81d011cf652be4531388856024b1d2128a6c)), closes [#202](https://github.com/Easonliuuuuu/vsfleet/issues/202)
* **assessment:** name the missing privilege when datastore browse is denied ([01e0f22](https://github.com/Easonliuuuuu/vsfleet/commit/01e0f22a997e2851ed7836814b7fd410779f39cf))
* **assessment:** render snapshot ages in compact units ([203e9dc](https://github.com/Easonliuuuuu/vsfleet/commit/203e9dc86e9ecb036a27160c6bffcf2a3b670954))
* **capacity.go:** list multi-datastore VMs only under datastores they use ([985b922](https://github.com/Easonliuuuuu/vsfleet/commit/985b922b45f2a0bcb59e629ea73aba16f4107b51)), closes [#201](https://github.com/Easonliuuuuu/vsfleet/issues/201)
* **ci:** make automatic labels follow explicit components ([10bcd7f](https://github.com/Easonliuuuuu/vsfleet/commit/10bcd7feacaa3c04740b88a308e7980fb7fa57f9))
* **cli:** reject invalid scope, run IDs, non-finite thresholds, and topology depth ([cc99460](https://github.com/Easonliuuuuu/vsfleet/commit/cc99460d8ff79ca11d69b482aa87a96db3f46bb5)), closes [#157](https://github.com/Easonliuuuuu/vsfleet/issues/157)
* **cli:** show empty datastore files as 0B instead of unknown ([cb66403](https://github.com/Easonliuuuuu/vsfleet/commit/cb66403fb77e71cd5d1705323e83dfe820c61ef2)), closes [#193](https://github.com/Easonliuuuuu/vsfleet/issues/193)
* **cluster.go:** read cluster capacity through the summary interface ([684b391](https://github.com/Easonliuuuuu/vsfleet/commit/684b391a1c672b9be3903d84173fe7631626ee24)), closes [#203](https://github.com/Easonliuuuuu/vsfleet/issues/203)
* **datastore_browse.go:** report on-disk VMDK size in the orphan scan ([abc8064](https://github.com/Easonliuuuuu/vsfleet/commit/abc80641ad156e976b4116e5a78f5ba960152e21))
* **datastore_files.go:** keep the inventory filter out of the datastore browser ([6a0a949](https://github.com/Easonliuuuuu/vsfleet/commit/6a0a94968d00bc5cd8a1e8f569f2e3816564ad91)), closes [#225](https://github.com/Easonliuuuuu/vsfleet/issues/225)
* **datastore_files.go:** show disk label and backing path in VMDK inspector ([e6c1bbb](https://github.com/Easonliuuuuu/vsfleet/commit/e6c1bbb7590729595b2b29051ba1465e8e5f2c05)), closes [#227](https://github.com/Easonliuuuuu/vsfleet/issues/227)
* **datastore_files.go:** show listing modification times in local time ([d8b3123](https://github.com/Easonliuuuuu/vsfleet/commit/d8b31234621ffc01f0d406ac21840e098879e867)), closes [#226](https://github.com/Easonliuuuuu/vsfleet/issues/226)
* **diff.go:** fingerprint migration-relevant disk fields so collector upgrades are not changes ([4153624](https://github.com/Easonliuuuuu/vsfleet/commit/4153624c2b115338c5b82b079fde63b92efcf5f7))
* **diff.go:** report migration configuration changes per sub-field ([82ae5a9](https://github.com/Easonliuuuuu/vsfleet/commit/82ae5a9bb0d43d3e9370552b71382011286da5f9))
* **dvswitch.go:** invert rolling order when reporting failback ([f3ed582](https://github.com/Easonliuuuuu/vsfleet/commit/f3ed582a73b780edfbf88f382e536d4ceb0a2dd6))
* **health:** ignore automatic CPU topology in findings ([2a69ec0](https://github.com/Easonliuuuuu/vsfleet/commit/2a69ec0c5f189f49de8042641016fc65727f0547))
* **metadata.go:** honor the pinned TLS thumbprint for tag lookups ([619b92e](https://github.com/Easonliuuuuu/vsfleet/commit/619b92e4f5b5a65abb4abcacb20fc136d7fef236)), closes [#190](https://github.com/Easonliuuuuu/vsfleet/issues/190)
* **orphans.go:** print the datastore prefix once in orphan output ([8b8adc4](https://github.com/Easonliuuuuu/vsfleet/commit/8b8adc4878e2234d8d8c3f634b9bbdbcba5f9a92)), closes [#191](https://github.com/Easonliuuuuu/vsfleet/issues/191)
* **report:** align RVTools 4.8 workbook structure ([#213](https://github.com/Easonliuuuuu/vsfleet/issues/213)) ([4515828](https://github.com/Easonliuuuuu/vsfleet/commit/4515828fe205958d8b892b9ee5fd99715d9d39cc))
* **report:** satisfy staticcheck in device ordering ([23e03c8](https://github.com/Easonliuuuuu/vsfleet/commit/23e03c8c4d6d90dac6a314cf2571ed9a49f32e18))
* **rows.go:** widen Hosts CPU and MEMORY columns so capacity is not truncated ([4002601](https://github.com/Easonliuuuuu/vsfleet/commit/4002601fdb4cd06c4471f26b909f58f314089225))
* **rules.go:** skip uplink port groups in dvportgroup-promiscuous ([be63e53](https://github.com/Easonliuuuuu/vsfleet/commit/be63e53cafa2bf41615ae24d44d34481f87fce78))
* **rvimport:** support RVTools 4.8 headers and metadata ([bfd8fa5](https://github.com/Easonliuuuuu/vsfleet/commit/bfd8fa5befdf10d839d672a651a5299e282a806b)), closes [#206](https://github.com/Easonliuuuuu/vsfleet/issues/206)
* **testbed:** choose available scenario ports ([9e97e26](https://github.com/Easonliuuuuu/vsfleet/commit/9e97e26e47dcc478aa551f7810ecf0063c3560aa))
* **testbed:** pin the scenario harness to UTC so goldens are portable ([40feaf6](https://github.com/Easonliuuuuu/vsfleet/commit/40feaf670b6ddffb4219e61fbed99e46e425c2f9))
* **tui:** default scoped History Changes to runs that reached the context ([b7f203a](https://github.com/Easonliuuuuu/vsfleet/commit/b7f203aca55025671921a9d54229ac2296cad218)), closes [#224](https://github.com/Easonliuuuuu/vsfleet/issues/224)
* **tui:** keep the key hints on the bottom row of short screens ([46a1bf4](https://github.com/Easonliuuuuu/vsfleet/commit/46a1bf4b7898df0d8f40a7d80ef4a03e262f6292))
* **tui:** keep Trends key hints in the footer ([a9f45b8](https://github.com/Easonliuuuuu/vsfleet/commit/a9f45b8b852dcadf96e03ecc4c23abd9d05d223e))
* **tui:** move the History Runs pane key hints into the footer ([e4514e3](https://github.com/Easonliuuuuu/vsfleet/commit/e4514e342463202f05c95d44abac677e0fdcc9b6))
* **tui:** scope History changes and trends to the vCenter in scope ([306531a](https://github.com/Easonliuuuuu/vsfleet/commit/306531a7aee3a0e81123f77846b02269c2cfe236))
* **tui:** show tab/shift+tab as the history pane switch hint ([9503c81](https://github.com/Easonliuuuuu/vsfleet/commit/9503c81c61b4d9c71425d4cc676f246f35a2ced3))
* **vsphere:** pass the connection test with a ReadOnly account ([442d505](https://github.com/Easonliuuuuu/vsfleet/commit/442d505d4d4583ea58cb823e5f59bbdb71d8a782)), closes [#189](https://github.com/Easonliuuuuu/vsfleet/issues/189)
* **vsphere:** resolve distributed port group names for VM NICs ([3f7979e](https://github.com/Easonliuuuuu/vsfleet/commit/3f7979e2819cd051540bba861d1d997e6a4f5f7c))

## [0.6.0](https://github.com/Easonliuuuuu/vsfleet/compare/v0.5.0...v0.6.0) (2026-09-12)


### ⚠ BREAKING CHANGES

* **health:** health --fail-on-findings --severity warning now fails only on verified-unreferenced orphan VMDKs; suspected and referenced-other-context results are informational, and incomplete coverage is unknown.

### Features

* **assessment:** attribute datastore growth and project thresholds ([17542e7](https://github.com/Easonliuuuuu/vsfleet/commit/17542e794fbabe01eeff899dabd8b8d6db4d8d7a))
* **assessment:** persist VM templates in captures ([4aa5729](https://github.com/Easonliuuuuu/vsfleet/commit/4aa572955cf3761e675fef8efffd30bcad935fdc))
* **cli:** add assessment orphans drill-down ([15aab1c](https://github.com/Easonliuuuuu/vsfleet/commit/15aab1c739ceeac22cffa500185d18faa036fbd4))
* **cli:** add offline VM decommission checks ([24a2af3](https://github.com/Easonliuuuuu/vsfleet/commit/24a2af395da1bf45a6121f9fafa289474fff7ff5))
* **config:** record the parent context a nested context was added from ([bd66eb7](https://github.com/Easonliuuuuu/vsfleet/commit/bd66eb7d0b73c026b7d31be49a067ee776e13d6e))
* **health:** add migration readiness assessment ([95272f4](https://github.com/Easonliuuuuu/vsfleet/commit/95272f41234a38323848d36be879768d7dabcf67))
* **health:** add persisted assessment health findings ([48df5c3](https://github.com/Easonliuuuuu/vsfleet/commit/48df5c354bca3159b7faf7e06f4cbe8b03b51519))
* **health:** classify orphan VMDK confidence across the estate ([e01ee22](https://github.com/Easonliuuuuu/vsfleet/commit/e01ee222f4a2b8a448dc07d9883caedfdfcb59e7))
* **health:** detect connected CD-ROM and USB devices ([769a633](https://github.com/Easonliuuuuu/vsfleet/commit/769a633c6edd620f4e30281a0b82d6e8c7462a76))
* **health:** detect inaccessible and orphaned VMs ([078ece7](https://github.com/Easonliuuuuu/vsfleet/commit/078ece7956d513478b988047af5648bab6acae27))
* **health:** expand migration readiness evidence ([4feb022](https://github.com/Easonliuuuuu/vsfleet/commit/4feb02214f86e927170d7f3f696a7d35a4e71dd0))
* **health:** report orphaned VMs and zombie VMDKs ([0d9bb73](https://github.com/Easonliuuuuu/vsfleet/commit/0d9bb7311a95130a15ed871d198d717f00920334))
* **network:** compare cross-cluster migration readiness ([79e4f3f](https://github.com/Easonliuuuuu/vsfleet/commit/79e4f3f3ff99ab7261ff0070a9b570f743f6aecb))
* **report:** export resource pools as vRP ([e658225](https://github.com/Easonliuuuuu/vsfleet/commit/e6582256ee707118324550c2e0c224b7f6dfb730))
* run headless, report partial captures, and describe the export profile ([b67a3e1](https://github.com/Easonliuuuuu/vsfleet/commit/b67a3e143cc12bedc200e7e77d7a59122e7f80ec))
* **topology:** add cross-vCenter relationship queries ([7d9397e](https://github.com/Easonliuuuuu/vsfleet/commit/7d9397eeac76599152389bbdc255ac1aacdbb061))
* **tui:** add a nested vCenter as a context from a VM's detail pane ([8072d07](https://github.com/Easonliuuuuu/vsfleet/commit/8072d07474a7652bacd1afdd1bc36dc529386b8a))
* **tui:** add read-only datastore file browser and search ([ed85f61](https://github.com/Easonliuuuuu/vsfleet/commit/ed85f615ca9bf92da22b505b060ba98628d32650))
* **tui:** add SSH, browser, copy, and jump actions to the detail pane ([fa624cb](https://github.com/Easonliuuuuu/vsfleet/commit/fa624cb0907cf2df5c5a5fd80047defe92de47c6))
* **tui:** connect datastore files to VM ownership evidence ([7397e9f](https://github.com/Easonliuuuuu/vsfleet/commit/7397e9f18fcaa20c8806035a1ce78b02f6d92901))
* **tui:** rank history changes by migration impact on a run axis ([d945b38](https://github.com/Easonliuuuuu/vsfleet/commit/d945b38fc17aa4447bf93c1e6d32ee55c8aaed18))
* **vsphere:** browse datastore disk files on demand ([006a1c8](https://github.com/Easonliuuuuu/vsfleet/commit/006a1c8f586a1b20891296d701a0ffc9bdb14eef))
* **vsphere:** collect datastore backing identity ([2274e9f](https://github.com/Easonliuuuuu/vsfleet/commit/2274e9fdd039b68bedb97e1d08e4db907374df87))
* **vsphere:** collect distributed switch inventory ([2d51390](https://github.com/Easonliuuuuu/vsfleet/commit/2d513908de984680b008d447e0a24777294404e7))
* **vsphere:** collect host storage and network inventory ([59ae488](https://github.com/Easonliuuuuu/vsfleet/commit/59ae488da280a6f7b8dd6fda33ece4937d6fbf3b))


### Bug Fixes

* **assessment:** use total host CPU capacity in metric projection and trends ([3118271](https://github.com/Easonliuuuuu/vsfleet/commit/3118271744adf7235573918830a7b111cc69ec7a))
* **capacity.go:** stop leaking NUL-delimited context keys into blindness diagnostics ([edba631](https://github.com/Easonliuuuuu/vsfleet/commit/edba6319afa530906ab4028fdee450438393a039))
* **cli:** close the history database when a command finishes ([370a05e](https://github.com/Easonliuuuuu/vsfleet/commit/370a05e28d7a9af62c6736c01034cfc0f0ee77d9))
* **cli:** honor context selectors for stored assessments ([fa5adfb](https://github.com/Easonliuuuuu/vsfleet/commit/fa5adfb82b5dd9b3577e5386d1d382b815395faa))
* **decommission:** rename clean verdict so it never reads as delete authorization ([122b07d](https://github.com/Easonliuuuuu/vsfleet/commit/122b07d8ba747a56de01bb7230b02b6b6f6f0183)), closes [#98](https://github.com/Easonliuuuuu/vsfleet/issues/98)
* **demo:** disable context management and add-vcenter actions in demo UI ([0d00dc7](https://github.com/Easonliuuuuu/vsfleet/commit/0d00dc7bef8d413397d6c65f78e62488a3d2fc5f))
* **demo:** wire seeded assessment history into standalone launcher ([9584754](https://github.com/Easonliuuuuu/vsfleet/commit/9584754483c40c4226de5f65cd1480832ab73cae))
* **health:** exclude local storage from path redundancy findings ([#132](https://github.com/Easonliuuuuu/vsfleet/issues/132)) ([7c660cf](https://github.com/Easonliuuuuu/vsfleet/commit/7c660cf9821a57e3c7fa649d55140400cca1983b))
* **health:** remove obsolete orphan matchers ([ab9255c](https://github.com/Easonliuuuuu/vsfleet/commit/ab9255c37225793e16b5ad7074d7212b173caae1))
* **health:** satisfy staticcheck partition sorting ([3986e71](https://github.com/Easonliuuuuu/vsfleet/commit/3986e71b555e38ddfde3fa22a67ea296e26c521b))
* **health:** stop treating VMXNET3 UPT as passthrough ([b5c8e00](https://github.com/Easonliuuuuu/vsfleet/commit/b5c8e006529b5a1d91875ca63189c078bb63124a))
* **history.go:** restore browse filter placeholder on every History exit ([1e7f6e6](https://github.com/Easonliuuuuu/vsfleet/commit/1e7f6e634c318d255d024e8c53dafe504a6bc2e4))
* **kubernetes:** prepare kind hostPath permissions ([5ebc5e4](https://github.com/Easonliuuuuu/vsfleet/commit/5ebc5e4a520b87bf79aa7477fc30aa486747f1de))
* **kubernetes:** provide temporary storage for vcsim ([0a013bc](https://github.com/Easonliuuuuu/vsfleet/commit/0a013bc62a26f5a3a5df04dd0fcef73cfc2efb2d))
* **kubernetes:** tolerate interleaved partial diagnostics ([f278e68](https://github.com/Easonliuuuuu/vsfleet/commit/f278e6850c59d222e73d5adfc32fadb7ba64531c))
* **lint:** resolve Staticcheck findings ([1f900f1](https://github.com/Easonliuuuuu/vsfleet/commit/1f900f1cafa7844e01b4b6eed897cf9a58b77440))
* **orphans:** report scan coverage so missing browse evidence is not a clean result ([b87037c](https://github.com/Easonliuuuuu/vsfleet/commit/b87037cdfd1436f1562b8c23e1699cc6299513a3))
* **topology:** keep same-named objects in one vCenter distinct ([a6f92ce](https://github.com/Easonliuuuuu/vsfleet/commit/a6f92ce676ea50e1f79e6b4ce87724a83f71cfaf))
* **topology:** preserve run context coverage metadata for blind analysis ([a72b8d9](https://github.com/Easonliuuuuu/vsfleet/commit/a72b8d9fe4d01bab82b4a83df3084d3edc4523db))
* **trends:** make context selection authoritative for scoped trends ([49ecb82](https://github.com/Easonliuuuuu/vsfleet/commit/49ecb820c833e5aba78135207d0ee1da9eb2ae98))
* **tui:** distinguish resolved child vapps from missing in legacy pass ([5aa848e](https://github.com/Easonliuuuuu/vsfleet/commit/5aa848ea4ce4a0dc9dc46a59941550fefc41fdf7))
* **tui:** give the vApp VM pane the real detail cursor and actions ([8c46832](https://github.com/Easonliuuuuu/vsfleet/commit/8c4683236f99eb52c18a7e98a5f6fc0bc2415f58))
* **tui:** hide History capture when no assessment collector is configured ([adfa251](https://github.com/Easonliuuuuu/vsfleet/commit/adfa251faa81bb3f771a5de4c1b4eec72cfffa13))
* **tui:** make SSH handoffs diagnosable and proxy-safe ([9a8580a](https://github.com/Easonliuuuuu/vsfleet/commit/9a8580a5e598a9ce172015abcbd8bbf52a5952b2))
* **tui:** remove unused datastore action helper ([ffc31cc](https://github.com/Easonliuuuuu/vsfleet/commit/ffc31cc13a0d73a6b0e25ccaa6a935b13105e098))
* **tui:** render dedicated headers for timeline and timeline-detail views ([504b892](https://github.com/Easonliuuuuu/vsfleet/commit/504b892997c33a49f1f15a7bab023740070b9d8d))
* **tui:** wrap credential overlay instructions at narrow widths ([1920cf7](https://github.com/Easonliuuuuu/vsfleet/commit/1920cf7fe0e0272c284fb0e0c3cf46cca365060a))
* **vsphere:** remove unused host mapper ([43469ce](https://github.com/Easonliuuuuu/vsfleet/commit/43469ce128ca332b6f6bc19114ebe371173142b2))
* **vsphere:** use case-insensitive path comparison ([1872499](https://github.com/Easonliuuuuu/vsfleet/commit/1872499e26b5db1e80357aad8bcabb67d2d46faa))

## [0.5.0](https://github.com/Easonliuuuuu/vsfleet/compare/v0.4.0...v0.5.0) (2026-09-05)


### Features

* **demo:** ship a credential-free demo and lead with the RVTools export ([bc77734](https://github.com/Easonliuuuuu/vsfleet/commit/bc777341669914b1fdfac064119788ec6c2825d1)), closes [#75](https://github.com/Easonliuuuuu/vsfleet/issues/75)
* **rvtools:** add the vPartition Disk Key column that joins to vDisk ([b4f9672](https://github.com/Easonliuuuuu/vsfleet/commit/b4f967233ca700088f50fe0b517601ba59c8d175))
* **rvtools:** add the vPartition tab from guest filesystem inventory ([97178f6](https://github.com/Easonliuuuuu/vsfleet/commit/97178f631d79cca68e5dbf4f5a3ef51eb3e7f36d))
* **tui:** add a comparison bar to the History Changes screen ([64e3210](https://github.com/Easonliuuuuu/vsfleet/commit/64e32100941fd484976b6390c47899d07455f253))
* **tui:** open vApp member workspace ([d0d03b9](https://github.com/Easonliuuuuu/vsfleet/commit/d0d03b9c83abac5ad41ed73d907f477100b2f746))
* **tui:** split the Changes list and its inspector side by side ([1128931](https://github.com/Easonliuuuuu/vsfleet/commit/11289319a0a3f4d393b2ad2fb76cb804a047aadd))


### Bug Fixes

* **rvtools.go:** report vDisk/vNetwork coverage from the VM collection status ([b95ceb0](https://github.com/Easonliuuuuu/vsfleet/commit/b95ceb0fa99981f73a55fc6e32a4fd2a439b70c3))
* **tui:** load large estates in pages instead of timing out at 30s ([40d9a08](https://github.com/Easonliuuuuu/vsfleet/commit/40d9a0887122e8b091620333d30512c78a8b3b44))

## [0.4.0](https://github.com/Easonliuuuuu/vsfleet/compare/v0.3.2...v0.4.0) (2026-09-04)


### Features

* **report:** add RVTools CSV export and vCPU/vMemory/vTools tabs ([0ae6c2a](https://github.com/Easonliuuuuu/vsfleet/commit/0ae6c2a2ddf7c9328876524c022a93a6ab5df084))
* **testbed:** add authenticated connected local simulator ([3153678](https://github.com/Easonliuuuuu/vsfleet/commit/3153678f36e3eb9e9167cd01314d84c3c673aeb9))


### Bug Fixes

* **assessment:** apply sqlite pragmas to every pooled connection ([01ca04e](https://github.com/Easonliuuuuu/vsfleet/commit/01ca04e51183de006dfdf990ebb2b36b6ecb2710))
* **tui:** defer interactive credentials until explicit load ([ed23bc0](https://github.com/Easonliuuuuu/vsfleet/commit/ed23bc09f711b46c3836324ae8265ef839f01895))
* **tui:** give the history hub one meaning for n and honest pane hints ([f567694](https://github.com/Easonliuuuuu/vsfleet/commit/f5676945c9ed0565086b6f1ef0fbe6c0951e85da))
* **tui:** scope history capture to the vCenter in scope and gate its prompts ([40aed9b](https://github.com/Easonliuuuuu/vsfleet/commit/40aed9bc3d76e0116bc1b05c2cfcef5630dfe6be))

## [0.3.2](https://github.com/Easonliuuuuu/vsfleet/compare/v0.3.1...v0.3.2) (2026-09-04)


### Bug Fixes

* **tui:** keep vApps and history discoverable ([ee1bda7](https://github.com/Easonliuuuuu/vsfleet/commit/ee1bda7b09608fce895cf579be29c5b4aad01d87))
* **tui:** show loading pane before startup credential prompt ([07560c3](https://github.com/Easonliuuuuu/vsfleet/commit/07560c38f81b931e101f0946d1e687562caf82d2))
* **tui:** stop unsolicited cross-context password prompts ([5c784d4](https://github.com/Easonliuuuuu/vsfleet/commit/5c784d4c3b069ca67e064697d5d57bb243838f81))

## [0.3.1](https://github.com/Easonliuuuuu/vsfleet/compare/v0.3.0...v0.3.1) (2026-09-04)


### Bug Fixes

* **release:** match Homebrew cask to the archive id ([61e42a2](https://github.com/Easonliuuuuu/vsfleet/commit/61e42a2850c345bdcbd665b87b4b7adb8b75dce0))

## [0.3.0](https://github.com/Easonliuuuuu/vsfleet/compare/v0.2.0...v0.3.0) (2026-09-04)


### Features

* **assessment:** add deterministic RVTools exports ([ba3004d](https://github.com/Easonliuuuuu/vsfleet/commit/ba3004ddf230d3aa4a9bbf438d46409c62d4387b))
* **assessment:** add estate trends and ledger maintenance ([a14a814](https://github.com/Easonliuuuuu/vsfleet/commit/a14a81431775c9d237f04aa3050c0c1921369689))
* **assessment:** add labeled VM timelines and drift policies ([11cddf6](https://github.com/Easonliuuuuu/vsfleet/commit/11cddf611a4c102581851ed6fe92bd054b27e500))
* **assessment:** add persistent VM drift history and TUI changes ([e31382c](https://github.com/Easonliuuuuu/vsfleet/commit/e31382c704b8ded2810bc9cc86d659f555b77935))
* **report:** add per-VM disk and network inventory ([5c977fe](https://github.com/Easonliuuuuu/vsfleet/commit/5c977fea3186cdb3449226293c8a1e923fd16414))
* **skills:** add show-me visual skill and enhance git-commit with visual summaries ([7b92f4b](https://github.com/Easonliuuuuu/vsfleet/commit/7b92f4b7413c86ed73eee7081bb93633d8968d27))
* **tui:** prioritize the visible kind and fetch the rest concurrently ([5fb7a24](https://github.com/Easonliuuuuu/vsfleet/commit/5fb7a247095d237dcf8448ba46c4dd98633e98b9))
* **tui:** re-read inventory in the background so the table stays current ([eb7ebe9](https://github.com/Easonliuuuuu/vsfleet/commit/eb7ebe913cb7d2e98a52a88dd69dedb294e499de))
* **tui:** tier the background refresh by what is on screen ([00953c3](https://github.com/Easonliuuuuu/vsfleet/commit/00953c35c300a47f7fbb60dbb75dcc04f5c0223f))
* **vsphere:** add read-only vApp inventory support ([7c6249c](https://github.com/Easonliuuuuu/vsfleet/commit/7c6249c4d8f593e04cc5d33aeed7caaafeb02efb))
* **vsphere:** scope inventory retrieval to the configured datacenter ([46a4f7d](https://github.com/Easonliuuuuu/vsfleet/commit/46a4f7dceaae805b51b3cffbea7fb80d72f9c6f9))


### Bug Fixes

* **assessment:** remove unused timestamp helper ([1c225fa](https://github.com/Easonliuuuuu/vsfleet/commit/1c225fae1aae856506e42ba63b6e955f71945eac))
* **cli:** close export test history on windows ([a6dba87](https://github.com/Easonliuuuuu/vsfleet/commit/a6dba87382631edd842c0102c4b6a994e66f2b00))
* **contextops.go:** fall back to a prompt credential when the keyring is unavailable ([8c228cc](https://github.com/Easonliuuuuu/vsfleet/commit/8c228ccdebaf1502bbfb003b824597f02ad57df0))
* **session:** bound inventory enumeration by --timeout, not just connecting ([5564a74](https://github.com/Easonliuuuuu/vsfleet/commit/5564a744dcf57a13a4d57cde54100860c5dc7c6e))
* **tui:** lazily authenticate context panes ([391c1e6](https://github.com/Easonliuuuuu/vsfleet/commit/391c1e6a3fb5a09eac455351752aeac8105ac24f))
* **tui:** prioritize controls in focused search inputs ([2ef2733](https://github.com/Easonliuuuuu/vsfleet/commit/2ef2733fa2f9a7baa13d949cf9f639b229f55a54))
* **tui:** require a terminal before launching the interface ([7b70fbe](https://github.com/Easonliuuuuu/vsfleet/commit/7b70fbe149485e40734b01677de8733a6dc02398))
* **tui:** resolve the selected context's credentials before Bubble Tea and stop prefetching offscreen contexts ([7d18e78](https://github.com/Easonliuuuuu/vsfleet/commit/7d18e78733c54fd8e121ef0142f66194e10ec703))
* **tui:** stop background credential prompts from racing Bubble Tea for stdin ([71eafb0](https://github.com/Easonliuuuuu/vsfleet/commit/71eafb0d1df0f5705181d4d9ed47ebe25681edc1))
* **tui:** track inventory freshness per resource kind ([bf8cd25](https://github.com/Easonliuuuuu/vsfleet/commit/bf8cd25771d081ebb2c69cd239de8b5a58944c86))
* **ui.go:** keep pflag an indirect dependency ([eeb1363](https://github.com/Easonliuuuuu/vsfleet/commit/eeb1363e3a600c2d3f62d3df05386c6bc290a7a1))

## [0.2.0](https://github.com/Easonliuuuuu/vsfleet/compare/v0.1.0...v0.2.0) (2026-09-03)


### ⚠ BREAKING CHANGES

* **tui:** TUI keybindings changed. tab now opens the estate-wide search instead of switching panes, and n/e/x moved to the contexts screen behind c. Resource kinds gained 1-7 alongside the existing h/l cycling.

### Features

* **tui:** flatten the browse screen and add estate-wide search ([42cb93b](https://github.com/Easonliuuuuu/vsfleet/commit/42cb93b0bcd5a641177dd518ab618d16ea60fa8c))


### Bug Fixes

* **model.go:** clear the load note when the scope changes ([f89f93f](https://github.com/Easonliuuuuu/vsfleet/commit/f89f93f035b0a9e1e2ac4b6798f2a2eb81001ac9))
* **session:** invalidate a context's session and cache when it is edited ([b50762e](https://github.com/Easonliuuuuu/vsfleet/commit/b50762e03c13f72941fbc1f33de4531fe09c76b7))

## [0.1.0](https://github.com/Easonliuuuuu/vsfleet/compare/v0.0.1...v0.1.0) (2026-09-02)


### ⚠ BREAKING CHANGES

* **repo:** the vctui executable, Go module path, VCTUI_CONFIG/VCTUI_STATE variables, vctui config and state directories, and vctui keyring service are replaced by their vcfleet equivalents. Existing users must migrate configuration and credentials manually.

### Features

* **cache:** add a bounded, stale-preserving inventory cache; wire into the TUI ([5a4a075](https://github.com/Easonliuuuuu/vsfleet/commit/5a4a075ffa82172901f64274c279ac88b87508fb))
* **cli:** add proxy flags, wizard prompts and show output for the new routes ([e5fa27b](https://github.com/Easonliuuuuu/vsfleet/commit/e5fa27bcc8290222f90bb9ed2c9f5a204bec791f))
* **cli:** add the command line, diagnostics and cross-vCenter search ([4437247](https://github.com/Easonliuuuuu/vsfleet/commit/443724780ad0760db4312a3f11287f8ca28eeaf8))
* **internal:** add configuration, credential, transport and vSphere layers ([9117202](https://github.com/Easonliuuuuu/vsfleet/commit/911720206d043b6fe183fe4b90e02093bb916393))
* **readme:** add reproducible terminal demo ([9565598](https://github.com/Easonliuuuuu/vsfleet/commit/9565598f7e81ad48e21cd657aa245cb3caba6ee0))
* **testbed:** add synthetic vCenter development environment ([c7e0122](https://github.com/Easonliuuuuu/vsfleet/commit/c7e0122fa812d2d874b037fcec77aa7d5e50c5e6))
* **transport:** add HTTP and HTTPS CONNECT proxy routes ([fc19d91](https://github.com/Easonliuuuuu/vsfleet/commit/fc19d91798af6587bf34d469e472e9964106bd06))
* **tui:** add the terminal interface for browsing every vCenter ([3d364bc](https://github.com/Easonliuuuuu/vsfleet/commit/3d364bc4100c6d146a35ce31f168a24e22044e0a))
* **tui:** draw the row status glyph in its own gutter ([d788387](https://github.com/Easonliuuuuu/vsfleet/commit/d78838753c302991d493373f2b0cea3e0a7cc366))
* **tui:** launch the interface by default and manage contexts from it ([af00847](https://github.com/Easonliuuuuu/vsfleet/commit/af00847ee795df7fde4d76487959dc959520e8b2))
* **tui:** remember the last context, tab and sort mode between runs ([b6afa64](https://github.com/Easonliuuuuu/vsfleet/commit/b6afa649c202b11862df0528254ffa2ddce12c75))
* **tui:** support http/https proxy routes in the context form ([d56d6b8](https://github.com/Easonliuuuuu/vsfleet/commit/d56d6b892ee768d7c56bad23523af22da9f913cc))
* **vsphere:** return a partial inventory when one resource kind fails to list ([06f6f4c](https://github.com/Easonliuuuuu/vsfleet/commit/06f6f4cf766ef4012c399aac6ae1d01b66ab3807))


### Bug Fixes

* **cli_test.go:** bound the bare-command TTY test with a context deadline ([dfba606](https://github.com/Easonliuuuuu/vsfleet/commit/dfba60649640ca523072d07f0bb62191dfa709aa))
* **cli_test.go:** stop launching a real terminal program in CI ([d51cb47](https://github.com/Easonliuuuuu/vsfleet/commit/d51cb4754e040f2cde40ea81777aad4102aadea4))
* **release:** keep initial release in pre-major range ([51c01cc](https://github.com/Easonliuuuuu/vsfleet/commit/51c01cc8aefff92b30c9bc2ee19c6a962ca17d00))
* **tui:** widen the power column and drop the redundant scope marker ([c0373a4](https://github.com/Easonliuuuuu/vsfleet/commit/c0373a47adaa4ba9f5f5a4fe9db1bb67c328b2f5))
* **types.go:** pluralise the inventory count summary ([2e79c2c](https://github.com/Easonliuuuuu/vsfleet/commit/2e79c2c4a58d75348d9001b2b784b06d19112c33))
* **vsphere:** distinguish a rejected proxy password from a dead connection ([8cd540e](https://github.com/Easonliuuuuu/vsfleet/commit/8cd540e05de39e38cf0df40bd78cd095fe6255db))
* **vsphere:** name the context in interactive password prompts ([0d73d8e](https://github.com/Easonliuuuuu/vsfleet/commit/0d73d8eb83d947c9d7c43cf12a995b25a38fb320))
* **vsphere:** resolve a proxy credential once per diagnosis, not once per dialer ([319cb03](https://github.com/Easonliuuuuu/vsfleet/commit/319cb03c5e3b5a4ba7292a93586066a9a4a01b53))


### Miscellaneous Chores

* **repo:** rename project to vcfleet ([eadc588](https://github.com/Easonliuuuuu/vsfleet/commit/eadc588b2d7a15b24df803bd9ff0b7bd1c2f5f16))
