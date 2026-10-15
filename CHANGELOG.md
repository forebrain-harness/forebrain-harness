# Changelog

## [0.2.0](https://github.com/forebrain-harness/forebrain-harness/compare/v0.1.1...v0.2.0) (2026-10-15)


### Features

* **composer:** open the / and @ pickers anywhere and load mid-sentence skills ([e23f4f4](https://github.com/forebrain-harness/forebrain-harness/commit/e23f4f413030cdcee11f19ac34631ae7a1f16f85))
* **frontend:** adopt the design sheet's scope badge, sizes and content card ([98f27b1](https://github.com/forebrain-harness/forebrain-harness/commit/98f27b146078385d4cdb26fd78e8001ffdd7dd45))
* **frontend:** chat run blocks, pending approvals and permission label parity ([1a6d708](https://github.com/forebrain-harness/forebrain-harness/commit/1a6d70808031356e9576c99b492744cd8f8346c1))
* **gateway:** save web attachments into the chosen project folder ([b5e6ead](https://github.com/forebrain-harness/forebrain-harness/commit/b5e6ead384fcec2b2e753d1a7edc7f6564f2936c))
* **gateway:** web ui redesign with tenant and project surfaces ([#33](https://github.com/forebrain-harness/forebrain-harness/issues/33)) ([a4fe61c](https://github.com/forebrain-harness/forebrain-harness/commit/a4fe61c8fa76f0dbe65035e083bc57a042218b1c))
* land logging hardening, tui performance work and rebuilt web ui ([ac90041](https://github.com/forebrain-harness/forebrain-harness/commit/ac900412c468068fa099378d59d9c9435c6b5ca1))
* **lsp:** add language-server code intelligence runtime and surfaces ([bda9505](https://github.com/forebrain-harness/forebrain-harness/commit/bda95051e2b9db9a06b659fe7c35536678314721))
* **mcp:** manage servers from CLI and web, with run/state/memory fixes ([1527c46](https://github.com/forebrain-harness/forebrain-harness/commit/1527c467c3f1f5c8ff7ae8a1518e14349c38998b))
* projects own sessions, skill versions, per-model tokenizers, web redesign ([9ae6406](https://github.com/forebrain-harness/forebrain-harness/commit/9ae640647c52b3d57181a0cd553619323a210f1e))
* **run:** scheduled runs, subagent transcripts and lsp recommendation fix ([13ff5cf](https://github.com/forebrain-harness/forebrain-harness/commit/13ff5cf45a8ba3edd257ac092b91f7078541edeb))
* **run:** subagent conversation milestone across tui, gateway and frontend ([6d433ad](https://github.com/forebrain-harness/forebrain-harness/commit/6d433ad0a38be979f6324b268f05ec77dc1b1ab0))
* **state:** channels and cron jobs belong to a project ([d907f44](https://github.com/forebrain-harness/forebrain-harness/commit/d907f44058dfabdda4455a80503d6f7a20be2c91))
* **state:** clear stored stream deltas in migration to v11 ([38f42a0](https://github.com/forebrain-harness/forebrain-harness/commit/38f42a0ab1058e51bedf35195e0d7f2399d1ef55))
* **state:** name the other surface when a session is busy elsewhere (plan 021) ([ca39d9c](https://github.com/forebrain-harness/forebrain-harness/commit/ca39d9ce742bd0e2ba65536569de68072166697b))
* **tui:** persist the main agent's turn diff, budget, queue and mode events (plan 020) ([d6a4344](https://github.com/forebrain-harness/forebrain-harness/commit/d6a43442261c563359834675beb74cfa9e8e735f))
* **tui:** record the main agent's turn lifecycle in the shared log ([a01c3bf](https://github.com/forebrain-harness/forebrain-harness/commit/a01c3bf9c262d517c4c809f3d6f36b04645c4010))
* **tui:** show running tools' elapsed time and hint when the clipboard holds an image ([#29](https://github.com/forebrain-harness/forebrain-harness/issues/29)) ([33e439a](https://github.com/forebrain-harness/forebrain-harness/commit/33e439ade3f1329a186e79b652458b7d2be855f9))
* **tui:** show the first-token wait on the Working and Worked lines (plan 015) ([7da491c](https://github.com/forebrain-harness/forebrain-harness/commit/7da491c6ded9b571a32b9da9bc4c9d5be55e6789))
* **tui:** startup card palette pinned to the design sheet ([c9a4e43](https://github.com/forebrain-harness/forebrain-harness/commit/c9a4e431cf3070031f5e1b101a7cc92c801ead8d))
* **tui:** tab the /skills panel, two-column pickers, one origin table ([1ec4a3b](https://github.com/forebrain-harness/forebrain-harness/commit/1ec4a3bfa12a375be119274b6af7fb92f66efdef))
* **turn:** auto-deliver finished plan reviews to the planner ([b7eda22](https://github.com/forebrain-harness/forebrain-harness/commit/b7eda220a072cee798203e2a62ee0b0e51ca45b8))
* **turn:** continue a conversation by itself once its usage limit resets ([#31](https://github.com/forebrain-harness/forebrain-harness/issues/31)) ([6305ea9](https://github.com/forebrain-harness/forebrain-harness/commit/6305ea98bb7bcd933b861daae5a5a716f429251b))
* **turn:** measure the first-token wait once per run (plan 015) ([052f8db](https://github.com/forebrain-harness/forebrain-harness/commit/052f8db0f831af91126f3cfde1efe91bff592bd1))
* **web:** localize the elsewhere busy refusal (plan 021) ([6e9486b](https://github.com/forebrain-harness/forebrain-harness/commit/6e9486b5367729a8ffe9cc343fbc8d7f7d0d0130))


### Bug Fixes

* **acceptance:** assert the healthy gateway-status line the CLI prints ([8f2af22](https://github.com/forebrain-harness/forebrain-harness/commit/8f2af2204c2e0450de42398a408f19285db99f50))
* **ci:** restore every required check to green on main ([#62](https://github.com/forebrain-harness/forebrain-harness/issues/62)) ([bd2b8b8](https://github.com/forebrain-harness/forebrain-harness/commit/bd2b8b8c961a8c625eb8d37f227b06166985d1fa))
* **frontend:** settle the markdown imports before the test env teardown ([0fe3777](https://github.com/forebrain-harness/forebrain-harness/commit/0fe37774dff20ad2f92687497938be4c0b0ccd73))
* **gateway:** push stream deltas without persisting them ([d03476f](https://github.com/forebrain-harness/forebrain-harness/commit/d03476fa3ab79babfb0ce2611d941d5139bfd4c7))
* **lsp:** keep shutdown state when a crash restart aborts mid-launch ([d939495](https://github.com/forebrain-harness/forebrain-harness/commit/d93949560d80bfcf01f05dab12caf485b39b4c75))
* **lsp:** pull diagnostics from servers that register the provider dynamically ([e28e450](https://github.com/forebrain-harness/forebrain-harness/commit/e28e450f0876235303618c92ab184d5a0bd748e7))
* **lsp:** treat a server that stops reading its input as a crash ([c098ccb](https://github.com/forebrain-harness/forebrain-harness/commit/c098ccb2e4d5e5dffe051c7bce1a22c2deebb6cc))
* **plan-review:** silence delivery handoff and drop exit wait card ([d501e09](https://github.com/forebrain-harness/forebrain-harness/commit/d501e0996325458f4a9002d0b5a7896b552b03e7))
* rebuild subagent views from their worker transcript on reload ([3f443f7](https://github.com/forebrain-harness/forebrain-harness/commit/3f443f760575541c3c0cb327896030df9a019181))
* **run:** recall in-flight steers, scope plan files per session, serialize run queue hook ([9656051](https://github.com/forebrain-harness/forebrain-harness/commit/9656051e32dd4d209ad49181a771385c40dee52e))
* **run:** start the subagents of one answer together ([e2161b2](https://github.com/forebrain-harness/forebrain-harness/commit/e2161b2e81d6ca09f05bf0b2a81cd7fa6f08ed0c))
* **runtime:** branch audit fixes across gateway, lsp, turn, state and frontend ([936df40](https://github.com/forebrain-harness/forebrain-harness/commit/936df40fba178817210348ab065632efc13b6345))
* **test:** pin the cmd/forebrain tests to a throwaway home ([a507668](https://github.com/forebrain-harness/forebrain-harness/commit/a507668d66e675a8acfab7050cf9a8ff05ef72ad))
* **test:** pin the home of the cancelled-onboard terminal test ([0da18fb](https://github.com/forebrain-harness/forebrain-harness/commit/0da18fbe3bbc84befa44773521019e1c96dd34f7))
* **tui:** decouple viewport paint writes from the input pipeline ([a7151f5](https://github.com/forebrain-harness/forebrain-harness/commit/a7151f5f24db1e34a43fee542c019aab2525b30f))
* **tui:** do not close a turn the database refused ([3d4ec7c](https://github.com/forebrain-harness/forebrain-harness/commit/3d4ec7c7108ca9c342c7d9911c8bde5ad70b1036))
* **tui:** keep first-line indent of multi-line tool payloads ([b459d85](https://github.com/forebrain-harness/forebrain-harness/commit/b459d85c2e2e384161644ef531c1c34fd919f381))
* **tui:** keep live subagent gauge when opening its view ([6667bb5](https://github.com/forebrain-harness/forebrain-harness/commit/6667bb5927cc433dcc9f6e8ef373cf90a176839e))
* **tui:** repaint the rows a box-drawing glyph drew over ([a329b89](https://github.com/forebrain-harness/forebrain-harness/commit/a329b89c9ac344ec2e69d5fe25f7d36ccbcc3cfc))
* **tui:** replay draws no card for canceled exit gates ([08a3145](https://github.com/forebrain-harness/forebrain-harness/commit/08a3145579e19be5b75e4ed446136230392eb2e3))
* **tui:** stop mirroring a repeated subagent failure into its open view ([88eb320](https://github.com/forebrain-harness/forebrain-harness/commit/88eb320dcb2eab66aa69f5a7c8468b202c181e9c))
* **ui:** mcp failure card paint-once, heartbeat composable unify, web cardless exit gate ([005c1a8](https://github.com/forebrain-harness/forebrain-harness/commit/005c1a88646998ac02ba1e50db144505fd2492fe))


### Performance

* **run:** answer subagent input preview from live channel ([71c0449](https://github.com/forebrain-harness/forebrain-harness/commit/71c04491a2cf88c4a5d474c99bd31e9619a27e6d))

## [0.1.1](https://github.com/forebrain-harness/forebrain-harness/compare/v0.1.0...v0.1.1) (2026-09-29)


### Bug Fixes

* **release:** pass the cross C compiler as one assignment ([#21](https://github.com/forebrain-harness/forebrain-harness/issues/21)) ([2f4e623](https://github.com/forebrain-harness/forebrain-harness/commit/2f4e623e9940bdf39df034d1d28c8548a0438d89))

## 0.1.0 (2026-09-29)


### Features

* **memory:** download dictionaries on first need ([74b7bf9](https://github.com/forebrain-harness/forebrain-harness/commit/74b7bf9d899c6f2aaa87db526c978b4235b8d562))


### Bug Fixes

* **ci:** run the Linux sandbox and stop the suite racing the clock ([#14](https://github.com/forebrain-harness/forebrain-harness/issues/14)) ([65e6a0c](https://github.com/forebrain-harness/forebrain-harness/commit/65e6a0c45e1e892cb1dc3623578279e23fc6a869))
* **deps:** patch reachable vulnerabilities in grpc, x/net and Go ([b92a1b1](https://github.com/forebrain-harness/forebrain-harness/commit/b92a1b16fae46ceed207c6541bdaa7e5f81e7049))
* **state:** explain a build without cgo or FTS5 in one sentence ([56d62c2](https://github.com/forebrain-harness/forebrain-harness/commit/56d62c2f01cca4cf32d879f85f33dbb70a6a3ac8))


### Chores

* import the existing codebase ([1227968](https://github.com/forebrain-harness/forebrain-harness/commit/12279681a50e9868b798ff87ff5af802bce9bd8d))
