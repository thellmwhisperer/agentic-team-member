# Changelog

## [1.2.0](https://github.com/thellmwhisperer/agentic-team-member/compare/v1.1.3...v1.2.0) (2026-10-20)


### Features

* atm run checks config, base green, worker files and delivery before the run ([2bc91c1](https://github.com/thellmwhisperer/agentic-team-member/commit/2bc91c181e401ddd26b0c3f135e6e04e828b482b)), closes [#506](https://github.com/thellmwhisperer/agentic-team-member/issues/506)


### Bug Fixes

* Ponytail keeps every cut that compiles and passes: quoting the issue is no reason to undo, dependent files are judged together ([4134f2d](https://github.com/thellmwhisperer/agentic-team-member/commit/4134f2d01839a48d516e0266ac2f41f20d2fc090))
* Process substitution in an agent's command crashes atm serve and every run in it ([74775d2](https://github.com/thellmwhisperer/agentic-team-member/commit/74775d20589fe17a4ec1c14d77f56953f964182c))
* Release PRs from release-please arm auto-merge at once, without labels ([bb493ad](https://github.com/thellmwhisperer/agentic-team-member/commit/bb493ad837a63631c62ea19374c4edec2a15ca60))

## [1.1.3](https://github.com/thellmwhisperer/agentic-team-member/compare/v1.1.2...v1.1.3) (2026-10-20)


### Bug Fixes

* the fake agent takes its own session when FAKE_AGENT_SETSID is set ([715601d](https://github.com/thellmwhisperer/agentic-team-member/commit/715601d33fac41d45aa812a5be32f738d6e73893))
* the lost-server test waits for its released fake agent ([8360133](https://github.com/thellmwhisperer/agentic-team-member/commit/8360133aa8787307af41e16208d4acf48a2fd18d))

## [1.1.2](https://github.com/thellmwhisperer/agentic-team-member/compare/v1.1.1...v1.1.2) (2026-10-19)


### Bug Fixes

* held fakes of codex, pi, cursor and opencode exit when their test binary is killed ([06a3d18](https://github.com/thellmwhisperer/agentic-team-member/commit/06a3d18f521127399609b3b03e35e1ea54a2ed39))

## [1.1.1](https://github.com/thellmwhisperer/agentic-team-member/compare/v1.1.0...v1.1.1) (2026-10-19)


### Bug Fixes

* The implementing agent gets its own scratch directory, as the scope review does ([12a970b](https://github.com/thellmwhisperer/agentic-team-member/commit/12a970b3f58467f8979ab27d673404243d5b7eef))

## [1.1.0](https://github.com/thellmwhisperer/agentic-team-member/compare/v1.0.0...v1.1.0) (2026-10-19)


### Features

* Checks: an e2e row after test runs cover-diff, and resume after an e2e red gives the agent one turn before Checks ([d58450f](https://github.com/thellmwhisperer/agentic-team-member/commit/d58450fe6fb61439327b09cf1f46b40a0c49ae18))
* Checks: every changed line must be executed by an end-to-end run of the real atm binary, or test fails naming the lines ([3d63321](https://github.com/thellmwhisperer/agentic-team-member/commit/3d633216294c09645334577383fe2e9006a1cdfc))
* every push to main keeps the end-to-end coverage profile as the cover-main-&lt;sha&gt; artifact ([7668c46](https://github.com/thellmwhisperer/agentic-team-member/commit/7668c46a0f4c29f2bde19818afffe537c94b06e8))
* The Delivery row opens like Checks: one sub-row per part (commit, review, repair, command) with its state and time ([eb5e743](https://github.com/thellmwhisperer/agentic-team-member/commit/eb5e743a4b9fb496edac03132ae42b5415a938a4))

## 1.0.0 (2026-10-17)


### Features

* 303/A: Read gate findings and CI repair commits for the follow stream ([653ce71](https://github.com/thellmwhisperer/agentic-team-member/commit/653ce716eddff09e96ae6b58c34a8b45788c6746))
* 303/B: Follow one existing run as JSON until its PR or run ends ([7f135d8](https://github.com/thellmwhisperer/agentic-team-member/commit/7f135d8c4643c2c47334aba8a33e771d4455a487))
* 303/C: Expose atm run --follow with a lease owned by the launch process ([cd4979d](https://github.com/thellmwhisperer/agentic-team-member/commit/cd4979db79aa9df980daf2406a0770b997445cb4))
* 323/A: Bloquear entrega sin revisión de issue/diff del commit actual ([8cd3e89](https://github.com/thellmwhisperer/agentic-team-member/commit/8cd3e8910373de884db600f11a3ac50ce4b64c59))
* 323/B: Aplicar task-slicing al resultado de la revisión antes de entregar ([3451d5d](https://github.com/thellmwhisperer/agentic-team-member/commit/3451d5d5670cb812a5b4100c9e95b2e884a7aabf))
* 323/C: Corregir incumplimientos originales con un máximo de dos rondas y repetir todas las pruebas ([e2d255e](https://github.com/thellmwhisperer/agentic-team-member/commit/e2d255e23348c86c9ca02e33464c0fe8e6e7bb93))
* Agent box: thinking folds like tool calls, clickable rows marked by an underlined arrow, clearer t/h hints ([ef9d278](https://github.com/thellmwhisperer/agentic-team-member/commit/ef9d278ed034f5dd10564b2a0cf26df22b9866c1))
* An agent whose final JSON report is invalid gets it back once, with the exact error, in the same session ([3a50a5c](https://github.com/thellmwhisperer/agentic-team-member/commit/3a50a5ca97509eb98806d6022ee980ce2583f216))
* atm axi plan lists which plan tickets can launch now and, with --follow, keeps following the plan until every ticket is closed ([0c40728](https://github.com/thellmwhisperer/agentic-team-member/commit/0c40728dac7491e53d5775cc5e783278054b2e9a))
* atm runs and report.json name the version of the serve that executed each run ([dd44b70](https://github.com/thellmwhisperer/agentic-team-member/commit/dd44b7084cceec065d0732332252c8e12e58c192))
* ATM: cap additional follow-up work by cumulative gross diff and preserve mandatory gaps ([b0f53d3](https://github.com/thellmwhisperer/agentic-team-member/commit/b0f53d330c1cc1e43ab573c8dc73810b43551579))
* Block follow-up scope expansion before starting the next worker unit ([cd5a40a](https://github.com/thellmwhisperer/agentic-team-member/commit/cd5a40ae35bccfa0b4515d3801411b37f5c061be))
* Block merge when no-mistakes adds code; bounded review path to clear it ([751bf6a](https://github.com/thellmwhisperer/agentic-team-member/commit/751bf6acee1e093e1cb997d616bec470f28fc6a8))
* Go port: a run in herdr owns its tab: atm run --follow opens it and closes it on its last line ([b4d8e8b](https://github.com/thellmwhisperer/agentic-team-member/commit/b4d8e8b33cf6c5beee770c88bbbb995ddd276c88))
* Go port: atm run stays alive through delivery until merge, as one state machine with no-mistakes ([632a1df](https://github.com/thellmwhisperer/agentic-team-member/commit/632a1df1b4595ac0340b3ae021f43da84a8504c0))
* Go port: atm ships its own skill, atm skill prints it and atm skill install puts it in place ([12685f1](https://github.com/thellmwhisperer/agentic-team-member/commit/12685f1b52d3566e24ed0ef3bc85d54bf3f27686))
* Go port: ATM speaks ACP itself, without acpx, and runs Hermes through it ([2957e52](https://github.com/thellmwhisperer/agentic-team-member/commit/2957e525424bfb8f6a82e24653b6e325daa78cb7))
* Go port: Cursor as a native harness, cursor-agent with stream-json ([4d02f2b](https://github.com/thellmwhisperer/agentic-team-member/commit/4d02f2b1317f23fb2bd1c8c6108bacb35d6f1be9))
* Go port: independently scroll native pipeline and Agent history boxes ([dbf6eec](https://github.com/thellmwhisperer/agentic-team-member/commit/dbf6eecced2591832cae3f61f1ddad69d92b279c))
* Go port: report.json and atm runs say which harness, model and effort did the run ([e719884](https://github.com/thellmwhisperer/agentic-team-member/commit/e719884045212e0d814a2e134ccb168b1ff968e1))
* Make the agent time limit configurable in .atm.yaml, defaulting to 30 minutes ([0d71f29](https://github.com/thellmwhisperer/agentic-team-member/commit/0d71f298553b65dff99b6ac40c9f73163aecf762))
* The Agent box lists every tool call the agent makes, folded by default, `t` to unfold them all and a click to unfold one ([0db7c0d](https://github.com/thellmwhisperer/agentic-team-member/commit/0db7c0db234f256cce09843902abcbdd8bb1e291))
* Windows CI: internal/run tests exceed the 30-minute limit on main ([a2d5846](https://github.com/thellmwhisperer/agentic-team-member/commit/a2d5846b1ec739d781af1c9e9c6ef6b64a1d1b21))


### Bug Fixes

* A clone gets the pinned slopslint binary, so an agent without network can run lint ([6e0a17c](https://github.com/thellmwhisperer/agentic-team-member/commit/6e0a17c4f473618c81c4bb54a3813f01aab06207))
* A contract repair agent that merges main fails as moved HEAD, not as a protected test it never edited ([aea9b05](https://github.com/thellmwhisperer/agentic-team-member/commit/aea9b05f6a1fc6e4af35a7c8208d9d6230fd3f97))
* a delivery that failed before any gate step ends the follow on an unreadable status ([aef077e](https://github.com/thellmwhisperer/agentic-team-member/commit/aef077e907d777afc757804386538fc1bda482ec))
* A pi agent whose model call errors fails the run with the provider's message, not as a missing skill proof ([73f0efe](https://github.com/thellmwhisperer/agentic-team-member/commit/73f0efeabd59e0eaf4327612d9ffdf291d0269b6))
* A run never executes on an atm serve built from an older binary ([024021a](https://github.com/thellmwhisperer/agentic-team-member/commit/024021ad8fdd599566a0b09400ffaebc87e5f4c3))
* A run whose gate skipped its CI step sees its PR merge and ends ([b9f2dc8](https://github.com/thellmwhisperer/agentic-team-member/commit/b9f2dc8da9ed63d61a39d8191f2e6483e0ad6f47))
* Align feature and greenfield briefs with the executed-red rule from [#306](https://github.com/thellmwhisperer/agentic-team-member/issues/306) ([ab5eb9f](https://github.com/thellmwhisperer/agentic-team-member/commit/ab5eb9fd01c2ec256dc8a5885ef968d065f2c280))
* An agent running a checks command is not stopped as silent before check_timeout ([0499760](https://github.com/thellmwhisperer/agentic-team-member/commit/04997602f0a9dce003d62d5785e12f084be45a25))
* atm attach on a finished run exits 2 when the server's close resets the socket on Linux ([a94ad26](https://github.com/thellmwhisperer/agentic-team-member/commit/a94ad267e421b38677999f51a7a1f1549f53e5a3))
* atm axi plan treats a closed issue as going when its last run never recorded an end ([148e611](https://github.com/thellmwhisperer/agentic-team-member/commit/148e61185c3fbe56a08338320988781f6c2e84b0))
* ATM keeps ponytail's valid cuts and undoes only the files whose cut fails ([a397793](https://github.com/thellmwhisperer/agentic-team-member/commit/a3977937348cecb2621ffaebd7e1dc5c91f36a25))
* atm run --follow creates the herdr workspace when it is missing, instead of starting the run with no tab ([e7202bf](https://github.com/thellmwhisperer/agentic-team-member/commit/e7202bf1d3b27ad63f7bd5a2fdc824a7cf4f92bc))
* atm run --follow reports a terminal gate failure as run_failed at delivery, with the gate's real reason ([0e09de8](https://github.com/thellmwhisperer/agentic-team-member/commit/0e09de880727bea222f52fd5357dc645fb50aec9))
* atm run --follow reports every no-mistakes commit as gate_fixing_ci, with its step ([dab25bf](https://github.com/thellmwhisperer/agentic-team-member/commit/dab25bf81e900d524e40533119eae664ef755711))
* Bind Unix clone eligibility and removal to the same trusted directory identity ([2122f4b](https://github.com/thellmwhisperer/agentic-team-member/commit/2122f4bacc63d52ffdebf283c6b685d0ebdc5cc4))
* Brief: say the red runs with the source reverted, so a Go test may only name identifiers on the base ([9e6ae1c](https://github.com/thellmwhisperer/agentic-team-member/commit/9e6ae1c17c64a567e09c796e75b9a760e112b83f))
* check_timeout in .atm.yaml sets how long each checks command may run ([1ec753d](https://github.com/thellmwhisperer/agentic-team-member/commit/1ec753d2d0a3b5ecb341d6b0fe5ac034d4159316))
* Checks does not put back the files of an ignored nested repository the agent changed ([bac8c09](https://github.com/thellmwhisperer/agentic-team-member/commit/bac8c090374ed990eb751c934db2bfd58b843629))
* Checks keeps the agent's version of ignored files the clone already had, so a package manager skips the reinstall ([d66c375](https://github.com/thellmwhisperer/agentic-team-member/commit/d66c375f06f5daed513f01e8e309a9f4794daa68))
* Checks removes new ignored directories and read-only caches before proof ([9e687b0](https://github.com/thellmwhisperer/agentic-team-member/commit/9e687b08de0a2406e0b43175e9fa9a2eaaeae0a8))
* Codex harness: a skill read is not counted when the same command exits nonzero ([4ecd777](https://github.com/thellmwhisperer/agentic-team-member/commit/4ecd777f62648e364b93573dbb8eaff40615c1ef))
* Ctrl-C during delivery fails it again instead of passing a gate that never finished ([7d7cad7](https://github.com/thellmwhisperer/agentic-team-member/commit/7d7cad74a484e205606aca90956c48f3b5aeb2bb))
* Doctor: propagate a Cursor error result after an OK greeting ([fa0f906](https://github.com/thellmwhisperer/agentic-team-member/commit/fa0f9067524db5d81539b158c811dd500b6fef60))
* drop repair evidence with zero rounds ([8e74c36](https://github.com/thellmwhisperer/agentic-team-member/commit/8e74c3613b8b3e192c1a234b4a0b297d244121dc))
* drop the sleeping end-to-end test no-mistakes added; the worker's PR-order test covers both cases ([ced4877](https://github.com/thellmwhisperer/agentic-team-member/commit/ced487744ec73e3763437e8cf6aa6069431afda0))
* Fake agents of the test suites exit when their test is killed, instead of polling forever as orphans ([f785b78](https://github.com/thellmwhisperer/agentic-team-member/commit/f785b78d2253bc41afcfae79911210ef757f805f))
* Gate nodes: herdr names the node that failed, and report.json does not invent their start times ([1ac01b6](https://github.com/thellmwhisperer/agentic-team-member/commit/1ac01b6bb4e7014c8e97bef184514758524099c6))
* Go port: a red is classified at the red/green boundary for every runner and task type ([e1d2fe8](https://github.com/thellmwhisperer/agentic-team-member/commit/e1d2fe88bdd65d4023c8db0c1f1d93f46adb55aa))
* Go port: a unit that fails Checks keeps the follow-ups its agent reported ([7e11213](https://github.com/thellmwhisperer/agentic-team-member/commit/7e112133b8e097fbcfd1b40af650700b760721e4))
* Go port: atm watch stops polling a run whose delivery failed before any gate, and its tests use real no-mistakes output ([229dab8](https://github.com/thellmwhisperer/agentic-team-member/commit/229dab81a795b9e71ea933e8151e6630859a0f7c))
* Go port: render Markdown in native ATM agent messages within panel width ([35e5041](https://github.com/thellmwhisperer/agentic-team-member/commit/35e50413519a3201f19d423f780ec89fb14af3bc))
* keep retrying gate status when the delivery command ran before failing ([3ce6287](https://github.com/thellmwhisperer/agentic-team-member/commit/3ce6287246cc57db855cc39c7f3c524cc065351c))
* Ponytail tombstones name no producer: what_went_wrong does not say ATM ([7687e5c](https://github.com/thellmwhisperer/agentic-team-member/commit/7687e5c618ea6111c415c1d43ebf6ce86d5fe61d))
* Read-only reviewers get a scratch directory outside the clone for the files their checks write ([7e2acba](https://github.com/thellmwhisperer/agentic-team-member/commit/7e2acba7fd9fec19889c08e5be38c305a05b07a4))
* Remove eligible read-only clone contents without losing lease safeguards ([d7dd155](https://github.com/thellmwhisperer/agentic-team-member/commit/d7dd155d6aad2db17e1f1a80dffa90ddf711df15))
* report.json names the files a partly kept ponytail cut put back, and why ([38452b1](https://github.com/thellmwhisperer/agentic-team-member/commit/38452b16e913e52b5c09955aeafb67408821781f))
* report.json records whether ponytail's cut was kept and why it was discarded ([e20e122](https://github.com/thellmwhisperer/agentic-team-member/commit/e20e1221d6a5b07043360e8bfbf563cc734ef7d8))
* Revert the no-mistakes merge gate to the decided label-only circuit ([183202a](https://github.com/thellmwhisperer/agentic-team-member/commit/183202a8cd50e05fe45d34698709c8e48f39ff63))
* review-required stays on a PR that already has review-completed ([a59af45](https://github.com/thellmwhisperer/agentic-team-member/commit/a59af457bf15971f4bcf3fce5f962a02ba3d1b46))
* **run:** distinguish quoted heredoc fixture data from network reach ([2bd222b](https://github.com/thellmwhisperer/agentic-team-member/commit/2bd222bdf4e0dc1b0551d439bf83e5bcbc22ae68))
* **run:** judge the reach of a write by its path, not by the text it writes ([c6c8e4b](https://github.com/thellmwhisperer/agentic-team-member/commit/c6c8e4bd225667b1c8a393ec3c73bc924d76eb34))
* **run:** move the final report's parse out of agent.run ([fa63723](https://github.com/thellmwhisperer/agentic-team-member/commit/fa637236e4c58db8fff4e77abe39a010cdd8911a))
* **run:** rename reported to declared to avoid clashing with the watch test helper ([55951ba](https://github.com/thellmwhisperer/agentic-team-member/commit/55951ba47d756c18bddf0d79717c03bcca8b906f))
* **run:** stop flagging clone-local relative paths as reach ([05ac5f3](https://github.com/thellmwhisperer/agentic-team-member/commit/05ac5f3f818aea10892874721fae5ed72353e0af))
* **run:** tell unwatched runs only under the server lock ([833f484](https://github.com/thellmwhisperer/agentic-team-member/commit/833f484a8dabf3f77960f47c87e731c13e0f8dad))
* TestWatchingHoldsWhileItsWatcherRewritesItsLease still fails on Windows CI after PR 369 ([d561bf6](https://github.com/thellmwhisperer/agentic-team-member/commit/d561bf693a1f9fba30d4c995cf4a1f14c0ccbfe9))
* The ponytail brief forbids shell writes, URLs in commands and background commands, like the worker brief ([e8bb000](https://github.com/thellmwhisperer/agentic-team-member/commit/e8bb000a4bf298bea5cbfe451474c1b098147c13))
* The worker brief forbids file writes through shell, URLs in commands and background commands ([d448676](https://github.com/thellmwhisperer/agentic-team-member/commit/d448676bbd8ceed2926035a4b689831cec0a0b32))
* **tui:** retain completed native pipeline and restore saved node results ([455778f](https://github.com/thellmwhisperer/agentic-team-member/commit/455778f67616e43f6319213a2a09df995d09c748))
* Workers on the claude harness cannot end their turn while a background command runs ([c867455](https://github.com/thellmwhisperer/agentic-team-member/commit/c867455c489766a2c5fa5c74a9b9f2f7b082e4dd))
