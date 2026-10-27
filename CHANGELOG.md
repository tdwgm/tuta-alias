# Changelog

All notable changes to tuta-alias. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Versions before 0.13.0 were not kept here; `git log` is their record. Neither
0.12.0 nor 0.13.0 was ever released: 0.12.0 exists only as a version string in
the history, and 0.13.0 was tagged locally while the audit was being written
and never pushed, so no tag of it exists and nothing was built from one. The
first release this project tags is 0.14.0, and it contains both sections
below.

## [0.14.2]

### Fixed

- The binary `make publish` commits is the one stage built from the tagged,
  clean commit. The build that proves prod compiles on its own ran before
  prod was committed, so the go tool stamped it `-dev+dirty`, and since it
  writes to the same `bin/tuta-alias` it replaced the binary just copied in:
  the first published binary reported `0.14.1-dev+dirty`. That check now
  builds to a throwaway path and the copy happens after it.

## [0.14.1]

### Added

- `make publish` (`scripts/publish.sh`): the only thing that writes to the
  prod repository `tdwgm/tuta-alias`. It copies the published subset, writes
  prod's own `.gitignore`, commits the binary built from this tree, proves
  prod builds on its own, scans the result with this repo's gitleaks rules,
  then commits and tags. It refuses a stage tree that is dirty, off `main`,
  or not pushed, so a prod commit always names the stage commit it came from.
  `make publish-dry` shows what it would do and touches nothing.

### Changed

- This repository is now stage (`tdwgm/tuta-alias_stage`), not the published
  one. CLAUDE.md names the three repositories and what each holds.

## [0.14.0]

What the branch review turned up, the version machinery that decides what this
file's newest heading means, and the gates that keep the rest from coming
back. It is a minor release and not a patch because `serve`, `doctor` and the
TUI each behave differently where a user can see it, and because the
standard-library advisories are fixed by the toolchain this now builds with,
not by anything in this tree.

### Security

- **With no `HOME` and no `XDG_CACHE_HOME`, the access token was written into
  the current directory.** `os.UserHomeDir`'s error was discarded and
  `filepath.Join("", ".cache", …)` is a relative path, so `whoami` looked for
  `.cache/tuta-alias/session.json` wherever it ran and a login wrote the token
  there, outside the home directory and outside everything `uninstall --purge`
  removes. The session path is now empty when there is nowhere to keep it, and
  every caller refuses rather than guesses, which is what `internal/config`
  and `internal/vault` already did. A relative `XDG_CACHE_HOME` is ignored,
  as the XDG spec requires, for the same reason. The resolved client-version
  cache had the same bug and the same fix.
- **`tuta-alias logout` no longer exits 0 with the token still on disk.**
  `session.Clear`'s error was dropped, so a session file that could not be
  deleted printed "No session to clear." and exited 0, and the next command
  logged straight back in. `login.Logout` now reports it, wrapped in
  `ErrSessionFileRemains` so a caller can tell it from the server-side close,
  which is allowed to fail because the token expires on its own. The CLI
  prints the path and exits 1; the TUI shows the error instead of saying
  "logged out".
- **`CACHE_TTL` in hours can no longer overflow into a negative duration.**
  A bare number is hours, and 2562048 of them is more than a `time.Duration`
  holds: `CACHE_TTL="3000000"` wrapped to a negative value that the validator
  accepted, the credential cache then stored entries that were already
  expired, and the master password was asked for every single time with
  nothing to show why. Negative and oversized values are refused, and the
  error names `forever` as the way to say what the number was trying to say.
- **A logged value can no longer forge a log line or reach the terminal.**
  What gets logged is not always this program's own text: an error quoted from
  the server, git's stderr, an address typed into a form. A newline in one of
  those forged a second line with a timestamp and a level of the sender's
  choosing, a carriage return overwrote the line before it, and an escape
  sequence was executed by the terminal when `tuta-alias logs` or the Logs
  screen printed it (OSC 52 writes the clipboard). Control characters,
  U+2028/2029 and invalid UTF-8 are written out as escapes now, in `write` and
  in `Tail`, so a line an older version wrote raw is neutralised when it is
  read back. Printable text, umlauts and emoji included, is unchanged.
- **`tuta-alias update` no longer reads the git config of the directory you
  happen to stand in.** `git ls-remote` inherited the working directory, so a
  repository that arrived as an archive could point the check at another
  server with `url.<base>.insteadOf`, or run a command of its own through
  `core.sshCommand` or `credential.helper`. git now runs in a temp directory,
  as the `go install` step already did. The trade-off: a per-repository
  credential (an `includeIf "gitdir:"` block, a per-repo deploy key) no longer
  applies to the update check and has to be in the global config.
- **`serve` keeps a token file it cannot read.** Any read error other than
  "does not exist" fell through to generate-and-overwrite, so a descriptor
  limit or a permission problem rotated the token and locked out every client
  that had it. Only a missing or empty file means "no token" now, and the
  bearer check already fails closed on the error.
- **The api-token and the log ignore a relative `XDG_CACHE_HOME` and an
  unusable `HOME`**, the same rule the session file got: the token used to be
  written into whatever directory the command ran in, a different one per
  directory, and `uninstall --purge` elsewhere left it behind.
- `logs --clear` with no log path no longer deletes a file called `.1` in the
  working directory.
- `logs --redact` and `doctor --redact` remove an upper-case domain
  (`MyDomain.SE`) and the Nordic TLDs that were missing from the list: `.nu`,
  which is the usual second choice in Sweden, plus `.ax`, `.fo` and `.gl`.
- **The secret scan was failing in CI and nobody looked.** gitleaks reported
  seven findings on this branch, all of them deliberate test data: a fake
  address at a domain the redaction test must not keep, a fake credential in a
  git error, and the vault's documented JSON shape. The documentation domains
  now cover their subdomains (`proxy.example.com` was flagged), a vault's
  shape in `internal/vault`'s own Go files is allowed by a narrow path rule
  (a real `vault.json` is still caught by name), and the lines that have to
  carry a fake secret say so with `gitleaks:allow`.
  The scan reads the working tree **and the whole history**, which is the
  point of it, so fixing today's line left the commit that introduced it red:
  the five fixtures (`@mydomain.se`, which the redaction test needs because
  `example.com` is a domain the redactor keeps, `@proxy.local`,
  `s3cretT0ken@github.com` and `/home/someone/`) are allowlisted by value,
  since a marker on a current line cannot reach the commit that added it.
  Two of the seven were not test data at all: `internal/tui/font.go` and
  `internal/tui/theme_std.go` went into the root commit with a "GENERATED by"
  header whose path named the author's sync service, desktop environment and
  tooling directory, which is precisely what `tuta-private-cloud-path` is for.
  The lines had been scrubbed from the working tree long ago, so only the
  history scan saw them, and the root commit cannot be edited in place: the
  history of `main` and of every branch was rewritten with `git filter-repo`
  instead, which is why every commit in this repository has a new hash. The
  rule needs no exemption and has none, and the path is not quoted in this
  file or in any commit message. GitHub keeps objects a force-push makes
  unreachable, so the old commits are still there to anyone who already had a
  hash; only support can purge those.
- **A response that stops short is an error, not an empty success.**
  `api.Client.handle` read the body with `io.ReadAll` and threw the error
  away, so a 2xx response cut off by a proxy, a dropped connection or the
  30-second client timeout came back as "no body, no error", and every caller
  reads that as data: a DNS check reported OK with no missing records and
  `dns-check` exited 0, `quota` printed "used 0/0", the alias and domain lists
  came back empty, and `waitForSecondFactor` counted an unanswered approval as
  approved. The non-2xx branch still ignores the read error on purpose,
  because `send` recognises the 474 client-version rejection by status alone
  and a short error body only shortens the message.

- **`serve` no longer accepts an empty api-token, and the bearer check is not
  trimmed.** An empty or whitespace-only `~/.cache/tuta-alias/api-token`
  (`> api-token` in a shell, or a crash between truncating and writing) read
  back as the empty token and was never regenerated, so `serve` started with
  an empty expected token. `strings.TrimSpace` on the provided token then
  removed Unicode spaces that the HTTP header parser keeps, so
  `Authorization: Bearer \u00a0` trimmed down to "" and matched: any local
  process that could reach the port had full alias create and delete, while
  doctor reported the token as "not created yet". An empty file counts as no
  token now, the provided value is compared as sent, an empty expected token
  fails closed, and the file is written through `internal/atomicfile` so a
  crash cannot leave it empty in the first place.
- **The log no longer records a proxy password.** Saving a value in the Config
  screen logged it verbatim, and `TUTA_PROXY` accepts credentials
  (`socks5://user:pass@host`), so the password landed in a file that `logs`
  prints, the Logs screen renders and rotation copies into `.1`. Everywhere
  else that value was already protected. `config.LogValue` is the one place
  that decides how a value may be written down, and it removes the user name
  as well as the password, because url.Redacted() keeps the user name and that
  identifies the person on its own.
- **The Go-team modules are up to date, which clears 16 of 19 advisories.**
  `golang.org/x/crypto` 0.51.0 to 0.55.0, `x/term` 0.43.0 to 0.45.0,
  `x/sys` 0.44.0 to 0.47.0 and `x/text` 0.37.0 to 0.41.0. None of the
  advisories was in code this binary compiles, which is why nothing here
  behaved differently, but a public repository reports them on the module
  versions alone and a reader cannot tell the difference without the call
  graph. The three that remain are in `x/crypto`'s `ssh` and `openpgp`
  packages, which this program does not import: one has no fix anywhere, and
  two are fixed in 0.56.0, which requires Go 1.26. Every checksum was checked
  against sum.golang.org rather than taken on trust. The one place the
  upgrade touches compiled code is ChaCha20-Poly1305, where the pre-AVX2
  assembly was dropped: the crypto, vault and session tests pass with
  `-tags purego`, which forces that generic path, on both the old and the new
  version.
- **Built with Go 1.26.10, which is what makes the standard library's
  advisories go away.** The modules were never the interesting half: go.mod
  said `go 1.25.9`, CI installed exactly that, and Go 1.25 left support when
  1.27 shipped. govulncheck counts 20 standard-library advisories this code
  reaches on 1.25.9, and half of them are fixed in no 1.25 at all: even
  1.25.14, the last one, leaves 10 standing. GO-2026-6603 and GO-2026-6617
  (HTTP/2 in `net/http`, which `serve` runs) are fixed only in 1.26.9 and
  1.27.2; GO-2026-6809
  (`crypto/x509` distrusting compromised intermediates, which every call to
  Tuta goes through) only in 1.26.10 and 1.27.3. There is nothing to fix in
  this repository and no version of Go 1.25 that helps.
  go.mod now says `go 1.25.0` with `toolchain go1.26.10`: 1.25 is still the
  minimum this module compiles with, 1.26.10 is what builds it, and the `go`
  line staying at 1.25 is what keeps the behaviour. The binary records
  `DefaultGODEBUG=cryptocustomrand=1,httpservecontentmaxranges=1,tlssecpmlkem=0,urlstrictcolons=0`,
  which holds those four Go 1.26 changes at their 1.25 values; none of them is
  a security fix, so nothing is given up by pinning them. The patch-level
  `go 1.25.9` was wrong on its own terms as well: the `go` line is the
  minimum a build needs, not a floor for security, and it locked out anyone on
  an earlier 1.25 for no reason.
- **An error quoted from Tuta can no longer forge a line or drive the
  terminal.** `internal/logx` already escaped what it writes to the log, but
  `redact` masked the secrets in an error body and passed every other
  character through, and that body is what the CLI prints to stderr and the
  TUI puts in its error line. A newline in it forged a second line of output,
  a carriage return overwrote the line before it, and an OSC 52 sequence
  wrote the user's clipboard. The body is now cut on a rune boundary, masked,
  and only then escaped, in that order: cutting first keeps the limit a
  measure of how much of the reply is quoted, and masking before escaping
  keeps a secret masked when a newline separates it from its key.
  `APIError.Error()` escapes the whole line as well, because the `Error`
  header and the error-id fallback never go through `redact`, and Go only
  refuses C0 and DEL in a header, so U+009B CSI gets through. A message can
  come out up to four times the 200 bytes it quotes.
- **`install` and every atomic write set the mode on the file, not on the
  name.** `atomicfile.Write` and `selfinstall.CopySelf` closed the temp file
  and then called `os.Chmod` on its path. Between the two the name is all
  that reaches the file, and chmod follows a symlink, so anyone who can write
  to the target directory could point the new binary's 0755, or a config
  file's 0600, at a file of their own choosing. Both now chmod the open
  descriptor, before the `Sync`, so the one flush covers the mode as well as
  the bytes. On its own this is hardening, because that write access already
  lets the same person replace the file; it stops being only that when the
  installer has more privilege than the directory's writers, which
  `sudo tuta-alias install` has on a sudo that keeps `HOME`. The rename is
  still by name: nothing in the standard library renames by descriptor.
  `update` installs through the same path and gets the change with it.
- **`serve` no longer serves a token file that other users can read.** A
  token this program creates is 0600, but one that is already there was used
  as it was, and `echo tok > api-token` under a 022 umask, a restore from a
  backup and a sync tool all leave it 0644. The token is the only thing
  between another account on the machine and the local API. `Run` now takes
  group and other access off it before it listens, says so on stderr and in
  the log, and says to run `serve --rotate-token` if someone else could have
  read it, because tightening the mode cannot take back a copy. Bits are only
  removed, so an owner-only 0400 stays. The file is opened through `os.Root`,
  so a symlink leaving the cache directory is not what gets chmod-ed. A chmod
  that fails is reported and `serve` starts anyway: it runs unattended, and a
  service that stays down until somebody reads its log protects nothing that
  the same chmod does not. The check is in `Run` and not in
  `GetOrCreateToken`, which every request calls, so the auth path is
  unchanged.

### Added

- **govulncheck as a gate, pinned to x/vuln v1.9.0.** Its own CI job, because
  it analyses the whole call graph and has nothing to say about the tests or
  the format, and because a red build there means a released Go or a released
  dependency grew an advisory this code can reach, which no change to this
  repository causes. It gates the `tag` job, so a release cannot carry a
  reachable advisory, and `make vulns` runs the same thing locally. Source
  mode, so an advisory in a package this program does not import is listed
  rather than failing the run: that is why the three left in `x/crypto` do not
  turn CI red.
- **golangci-lint as a gate, pinned and verified.** 28 linters plus gofmt and
  goimports, chosen so the tree passes today and a regression goes red:
  govet with every analyzer but fieldalignment and shadow, staticcheck,
  errcheck, errorlint, nilerr, forcetypeassert, bodyclose, gosec, gocritic and
  the rest, each with its reason in `.golangci.yml`, and 57 names listed as
  deliberately out so nobody has to work it out twice. `internal/tui/layout.go`
  and `font.go` are excluded from every linter, formatter and `--fix`, because
  they must stay identical to the template. CI installs release 2.15.0 from
  its tarball with the checksum verified, the same pattern the gitleaks job
  uses, and `make lint` reads the version from the workflow so the number
  lives in one place.
  The gate found four things, all fixed here: the one error type that broke
  the `APIError`/`ProxyError` naming convention, a `spinnerColor` helper that
  was written for the themed spinner and never wired to anything, an
  assignment overwritten in both branches, and an import in the wrong group.

- **The version is tagged automatically on push.** The git tag is now the
  version: the go tool derives the module version from the tag it builds at
  (`v0.13.0` at the tag, the pseudo-version `v0.13.1-0.<date>-<sha>` after
  it, `+dirty` on a modified tree) and `internal/consts/version.go` reads it
  back. The number itself is written in one place, the newest `## [x.y.z]`
  heading in this file; `consts.fallbackVersion` repeats it for a build with
  no tag to read (a tarball, a shallow clone) and a test keeps the two equal.
  `.githooks/pre-push` tags that version when it has no tag yet, for the
  default branch only (a tag on a feature branch points at a commit a squash
  merge throws away), prints what the push would release on any other branch,
  and honours `VERSION_TAG_ANY_BRANCH=1` and `SKIP_VERSION_TAG=1`. Push after
  a release and nothing is tagged, because the changelog still names a
  released version; the hook then says which number to write next, asking
  [svu](https://github.com/caarlos0/svu) (MIT) what the Conventional Commits
  since the last tag add up to. svu is an adviser, not a dependency: the hook
  tags without it. The `tag` job in CI does the same on the merge path, where
  no local hook runs, and `make version` shows all four numbers at once.
  Side effect: a binary installed by `tuta-alias update` knows its own commit,
  which it did not before — `go install` does not run the Makefile, so it
  carried no stamp and every later update check said it could not tell.

### Fixed

- **Four tests passed without testing what they claimed.** The review that
  found them broke each behaviour and watched the old test pass anyway.
  `TestWizardOffersEveryConfigChoice` compared the wizard's lists with the
  same `config.Choices` slices they are built from, so it would have passed
  with a wizard that offered nothing the file accepts; it now walks all four
  radio lists through the real `Update` loop, reads what the form would write
  at every stop, and requires `config.ValidateKey` to accept it.
  `TestWizardOwnedKeysAreRealAndPreserved` started from an empty home
  directory, so there was nothing to preserve; it now writes a config where
  every key the form does not ask about holds a non-default value and proves
  those survive a wizard run while the asked keys change.
  `TestWriteCreatesWithRequestedMode` asked `atomicfile` for the mode
  `os.CreateTemp` already gives, so it could not fail; it now measures that
  default in the test, requests modes that differ from it, and reads the mode
  back after the rename. `TestSharedLayerMatchesTemplate` skipped whenever the
  template repository was absent, which is every CI run, so the guard that
  keeps `layout.go` and `font.go` identical to the template never ran where it
  mattered; a checked-in checksum in `internal/tui/testdata/shared_layer.sha256`
  is compared on every run, the live comparison still happens when the
  template is reachable, and the record is renewed with
  `go test ./internal/tui -run TestSharedLayerMatchesTemplate -update-shared-layer`,
  which refuses unless the template is there and agrees.

- **Bitwarden: `BW_ITEM` names an item, but `bw get` wants an id.** `bw get
  username tuta` is a search, not a lookup: names, URIs, notes and custom
  fields all match, so an item named exactly `tuta` was not enough and bw
  exited 1 with "More than one result was found" plus one bare id per match.
  Login was impossible with no way to tell why. `BW_ITEM` is resolved through
  `bw list items --search` first, where the names come with the ids, and the
  item named exactly `BW_ITEM` wins; an id in `BW_ITEM` is used as-is. When
  the name really is ambiguous the error names the candidates and says to set
  the id, and a stale session is still retried once, while "no such item" is
  not (it would ask for the master password for nothing).
- **A backend's error is folded onto one line and wrapped.** The credential
  CLIs answer in paragraphs, and the paragraph was pasted into the frame
  as-is: the half of the sentence that said what went wrong was cut off at
  the right edge.
- **A lit bottom menu owns the keyboard on Home too.** The list kept reading
  its own keys under it: `j`/`k` moved an invisible cursor, and `/` put the
  focus in the search field while the menu was still active, a state where
  `inTextField()` is false, so the next `q` typed into the field quit the
  program. Domains already gated its keys this way.
- **The Home cursor is clamped when the list shrinks.** Narrowing the search
  or `SHOW_ALIASES_FOR` could leave it past the end, where no row is drawn,
  Enter matches nothing, and the next down-arrow jumps into the menu from an
  invisible cursor. The only clamp compared against the unfiltered rows.
- **`^s`, `^t` and `^x` say why they did nothing.** With a field focused, or
  the cursor on the Domains or Config row, they returned silently while the
  bottom menu advertised them on every frame; Enter on the same entries has
  always answered "select an alias in the list first".
- **Help and Doctor scroll offsets are clamped when the window grows.** The
  render was clamped but the offset was not, so the page looked right while
  the up-arrow did nothing for as many presses as the stale offset.
- **Help names every credential backend.** The paragraph was written by hand
  and listed four while `CRED_BACKEND` accepted five: the `vault` this branch
  added was the one a reader could not guess and the one Help left out. It
  comes from `config.BackendNames` now, the same list the Config screen and
  the wizard offer.
- **Help's navigation row says which key quits.** It promised that Esc quits
  on the first screen; Esc deliberately does nothing there, because there is
  no level to go back to and quitting on a stray Esc is not something to do
  to someone. `q` is the one that quits.
- **The Default domain chooser can clear the setting.** `DEFAULT_DOMAIN` is
  allowed to be empty (the Create-alias field then falls back to the first
  domain), but the chooser offered only the account's domains, so once one was
  picked there was no way back to empty from the screen.
- Help says where the saved `+` addresses are listed (`tuta-alias plus list`),
  which the TUI has no screen for, and the `config` command's one-line
  description names `get`, `set` and `check`, which it predates.
- **The bottom menu kept the keyboard after Plus, Note or Delete were run
  from it.** The three entries opened their screen but left the menu active,
  so `globalKey` answered the next Enter with the same menu entry: a note
  typed into the editor was thrown away and `notes.Set` never ran, the delete
  confirmation lost the address and never dispatched, and Plus opened with the
  selection still on Home's index 1, which on the Plus screen is Back, so
  Enter left without saving. Log out from the menu had the same shape: it
  switched to the login form with the selection past the end of its shorter
  menu, so the menu drew as active with nothing highlighted and swallowed the
  first Tab.
- **A DNS check result was dropped if you left the Domains screen while it
  ran.** `domainCheckMsg` was only routed when Domains was the current screen,
  so opening Help or Config during the round trip lost it: the row said
  "checking…" for good and every later check answered "already checking" with
  no command. It is routed at the top of `Update` now, like the login results.
- **Esc from Config goes back one level**, as the navigation row says. It went
  to Home from everywhere: opened over the Domains screen it threw that screen
  away, and opened over the login form it set the screen to Home while the view
  still drew the form, so the frame carried Home's bottom menu (Copy, Delete,
  Log out) under a form with no session behind it.
- **The Domains screen's "refreshing domains…" line is cleared when the list
  arrives.** `syncDomains` cleared the screen's loading flag but not the
  flash, so the line stayed under a list that had already been refreshed.
- **`uninstall --purge` is a purge again.** It left the log file behind, and
  the log is the one file that names what was done with the account: the
  address logged in, every alias created or removed, the config values that
  were changed. Its `.1` rotation held the same thing one generation back, the
  update check cache stayed, and so did `~/.cache/tuta-alias/` itself. All of
  them go now, `LOG_FILE` is followed wherever it points (the file goes, the
  directory the user chose for it stays), and the cache directory is removed
  only when this program's own files were all that was in it.
- **The Config screen no longer destroys a value its chooser does not list.**
  `CACHE_TTL` takes any Go duration in the file and from `config set`, while
  the screen offers five common ones. A value outside that list fell back to
  the first entry, so opening the chooser on a hand-written `45m` and pressing
  Enter silently made it `8h`. The current value is now an entry of its own
  whenever the list does not already hold it.
- **A choice value is stored the way the choice list spells it.**
  `config set CRED_BACKEND BitWarden` was accepted, because a hand-edited file
  should not fail over a capital letter, and then matched nothing: every
  consumer compares the value exactly, so the backend was silently inactive
  and the login form offered no shortcut for it. The same held for
  `LOG_LEVEL="INFO"`, `AUTO_UPDATE="Check"`, `THEME="GREEN"` and
  `CACHE_TTL="Reboot"`. Values are canonicalised on the one path into the
  struct, which covers the file, `config set`, the Config screen and the
  install wizard.
- **`config check` and doctor no longer call `COLOR_ACCENT` unknown.** It is
  the old name of `THEME`: the loader reads it and the next save rewrites it,
  so reporting it as "a key this version does not know" sent the user looking
  for a problem that does not exist. It is reported as the old name, with
  what it became, and its value is validated against the new key.
- **A relative `INSTALL_PATH` or `LOG_FILE` is not acted on.** `config set`
  refuses one and `config check` reports it, but the file is edited by hand:
  `INSTALL_PATH="~/bin/tuta-alias"`, with no shell to expand the tilde, had
  install write the binary to `$PWD/~/bin/tuta-alias` while reporting the path
  as typed, and uninstall run from another directory looked somewhere else
  again. `LOG_FILE="~/tuta.log"` created a directory literally named `~`
  wherever the command happened to run. Both fall back to the default until
  the value is absolute.
- **A logout or a login froze the whole TUI while Tuta was asked to close a
  session.** Log out called `login.Logout` and a login called
  `login.SaveSession` inside `Update`, and both send a DELETE. The API client
  gives a request 30 seconds, and the 474 client-version refresh can make one
  close take three times that; for all of it the TUI drew nothing, answered no
  key and did not turn its spinner, which on a dropped network looks like a
  hang. The session file is still written or deleted before `Update` returns,
  because the next screen depends on it and it is local; the close is a
  command, and the flash says "closing the previous session on the server…"
  until its result lands, while the screen keeps answering keys. A close that
  fails is shown beside "logged out" or "logged in as …" as the warning it is,
  and a session file that could not be deleted is still the red error it was.
  A logout also no longer leaves the token on disk while the server is asked:
  `login.Logout` closes first and deletes second, so `internal/login` gained
  `ReplaceSession` and `ClearSession`, the local halves of `SaveSession` and
  `Logout`, which hand back the token to close. `SaveSession` and `Logout`
  keep their order and their results. Quitting while a close is on the wire
  abandons the request, because bubbletea does not wait for a running
  command.
- **A second-factor poll that does not say "pending" is no longer read as
  "approved".** `waitForSecondFactor` took `m["secondFactorPending"].(bool)`
  and ignored whether the assertion held, so an empty body, an object without
  the field and an explicit null all gave `false`, which means "no longer
  pending": the wait ended and `Login` went on to load the account and hand
  back a session the user never approved. The field is required in Tuta's type
  model (`internal/typemodel/entities.go` has it as cardinality One, and
  upstream's Rust SDK declares it a plain `bool`), so a reply without it means
  the protocol changed or this is not a Tuta server, and neither of those is
  an approval. It now fails at once and says the flag is missing. A reply that
  says "not pending" still ends the wait as before.
- **`update --check` answers for the ref it was asked about.** `git ls-remote
  <url> main` matches the end of any ref name, so it also lists
  `refs/heads/feature/main`, and it lists in name order, which puts that
  branch first. `Check` took the first line, so the running build was
  compared with a branch nobody asked for: `--check` reported a different
  build when there was none, and with `AUTO_UPDATE=install` rebuilt on every
  interval. Only a name that is exactly the ref counts now, a tag before a
  branch, which is the order `go install <module>@<ref>` resolves one in, so
  the check compares what `install` would build. The same look found a second
  bug: for a plain tag name git never lists the `refs/tags/<name>^{}` line
  that holds the commit, so an annotated tag, which is what the pre-push hook
  makes, never matched the build installed from it. `Check` asks for that
  pattern too.
- **An install that crashes can no longer leave the old binary behind a
  rename that reported success.** `CopySelf` flushed the file and renamed it
  but never flushed the directory entry, so the rename could still be only in
  the page cache. `atomicfile`'s `syncDir` is exported as `SyncDir` and
  `CopySelf` calls it, rather than carrying a second copy.
- **The `export PATH=…` line that `install` and `doctor` print is safe to
  paste.** The install directory went into a double-quoted shell line as it
  was, so a name with a `$` in it expanded to something else, one with a
  backquote ran as code, and one with a double quote ended the string early.
  The four characters a POSIX shell still interprets inside double quotes are
  escaped now, and a directory without any of them comes out byte for byte as
  before. `selfinstall.ExportPathLine` is the one place that builds the line;
  `doctor` had its own unescaped copy and now calls it. Both tests run the
  printed line in `sh`, because reading it cannot tell `\$HOME` from `$HOME`.
- **A keyring entry that never expires no longer carries a year-1 expiry.**
  `cacheEntry.ExpiresAt` was `omitempty`, and `encoding/json` never treats a
  struct as empty, so every "forever" entry stored
  `"expiresAt":"0001-01-01T00:00:00Z"`. It is `omitzero` now. Both shapes read
  back as no expiry, so entries already in a keyring stay valid and an older
  build reads the new ones.
- **`doctor` reports the api token's mode, and stops calling an unreadable
  token "not created yet".** The check was `LevelOK` whatever the file's mode
  was, and anything that failed to read fell into the "not created yet"
  branch, so a token another account can read, and a token that exists and
  cannot be read, both looked fine on the checklist. It warns with the mode
  and `chmod 600` now, through the read-only `serve.LooseTokenMode`, because
  a checklist that fixes what it reports cannot be run twice for the same
  answer; `serve` is what makes the file private.
- **`serve` does not name a token file it has not got.** The banner printed
  `Token file: <path>` and `Listening on …` before anything had created the
  token, so with no `HOME` and no absolute `XDG_CACHE_HOME` it printed an
  empty path and claimed to be listening, and the reason came last. The token
  is created first, and its failure is all that is printed.

## [0.13.0]

An audit of config, CLI, TUI, Help, install and uninstall against each other.
Several settings and menu entries existed in one place and not in another; the
theme group was the worst case, with 14 config keys that the Config screen did
not show and a "Reset theme" entry that nothing implemented.

### Security

- **The install wizard no longer clears `TUTA_PROXY`.** It built a fresh
  config from its own form and saved it, and a save writes every known key, so
  re-running `tuta-alias install` silently turned a fail-closed proxy into
  direct Tuta traffic. It also reset `PLUS_SUFFIX_LEN`, `SHOW_ALIASES_FOR` and
  the whole theme group. The wizard now starts from the config on disk and
  changes only the keys its form asks about.
- **`serve`: `/v1/healthz` no longer hands out the account address without a
  token.** `Access-Control-Allow-Origin` is `*`, so any page the user visited
  could read it cross-origin. The address now needs the same bearer token as
  every other route; the endpoint still answers `{"ok": true|false}` without
  one.
- **`serve`: the `Host` header is checked.** CORS does not stop DNS
  rebinding, a Host check does. Binding to a non-loopback address is taken as
  deliberate: the check steps aside and the command warns instead.
- **`serve`: a request body is bounded** (64 KiB) and the server has read,
  write and idle timeouts.
- `SERVE_HOST` and `SERVE_PORT` were config keys nothing read. `serve` uses
  them as its flag defaults now, and warns when the result is reachable beyond
  this machine.
- Opening the Config screen no longer creates `~/.cache/tuta-alias/api-token`
  as a side effect of showing whether a token exists.
- The API token can be rotated: `serve --rotate-token`, or the Config screen.
- Copying the API token to the clipboard says so, because the clipboard is
  readable by every program in the session.

### Added

- **`tuta-alias update`, and `AUTO_UPDATE` in the config.** There is no
  release pipeline and the repository is private, so an update is not a
  download: it asks the remote what `UPDATE_REF` points at (`git ls-remote`,
  using the credentials your git already has), and `go install` rebuilds that
  ref and replaces the installed binary atomically. `AUTO_UPDATE` is `off`,
  `check` or `install` when the TUI starts, no more often than
  `UPDATE_INTERVAL`. Builds made with `make` stamp their commit, which is
  what the check compares, and `tuta-alias version` prints it; a build with
  no stamp says it cannot tell rather than guessing. doctor reports whether
  an update is possible here at all (git, go, a writable install path).
- **Sensitive data is stopped on the way to git.**
  [gitleaks](https://github.com/gitleaks/gitleaks) runs on the staged diff
  before every commit (`make hooks`), over the working tree and the whole
  history (`make scan`), and in CI. Besides its own key and token rules,
  `.gitleaks.toml` refuses an absolute `/home/<name>` path, a personal cloud
  directory, an email address outside the documentation domains, a Tuta
  session or vault file, the serve api-token and a password database. This
  project had to scrub exactly those by hand once already.
- `doctor --redact` and `logs --redact` remove addresses, paths, the user name
  and tokens from the output, so a checklist or a log can be pasted into a bug
  report. `internal/redact` is the one implementation, and it over-redacts on
  purpose.
- **A Domains screen in the TUI.** `domains`, `dns-check` and `verify-record`
  existed only in the CLI. The screen lists the account's custom domains,
  checks one's DNS records (`^k` or Enter) and copies the ownership TXT value
  (`^v`), opened from the Home list or the Config menu.
- **An encrypted vault as a credential backend** (`CRED_BACKEND="vault"`), for
  systems with no Secret Service keyring: Termux and Android above all, where
  the `keyring` backend cannot work and the alternative is typing the password
  on every login. One file, `~/.config/tuta-alias/vault.json`, mode 0600,
  argon2id (4 passes, 64 MiB) + XChaCha20-Poly1305, a fresh salt and nonce on
  every write, and the KDF parameters authenticated with the payload so a file
  edited to ask for a weaker derivation fails to open rather than quietly
  using it. The TOTP secret is not stored and there is no option to store it.
  `tuta-alias vault set|passwd|rm|status`, or tick "Save login" in the TUI.
- `tuta-alias doctor [--json]`: install, config, session, credential backend,
  proxy, serve, local stores, clipboard, editor, log and leftovers from the
  pre-0.10 name. Offline and side-effect free. Also a TUI screen (Config →
  Doctor).
- `tuta-alias logs [-n N] [--path] [--clear]` and a log file
  (`LOG_LEVEL`, `LOG_FILE`): the errors the TUI used to flash for a second and
  lose. Also a TUI screen (Config → Logs).
- `tuta-alias config get KEY`, `config set KEY VALUE` and `config check`, so
  every setting is reachable without an editor and without the TUI. Values are
  validated before they are written.
- `tuta-alias tui`: with `TUI_ENABLED="false"` there was no way to open the
  TUI at all.
- Fish completion (`COMPLETION_FISH`).
- `INSTALL_PATH`: the wizard could already choose where the binary goes, but
  `uninstall` only ever looked in `~/.local/bin`.
- The live info block under the banner (`LIVE_INFO`, `COLOR_LIVE`): account,
  alias count, quota, default domain and proxy. Both keys existed and painted
  nothing.
- Grey fine print on every TUI screen and in `tuta-alias version`: this is an
  independent tool, not affiliated with Tutao GmbH.

### Changed

- **The Go module is `github.com/tdwgm/tuta-alias`.** It declared
  `github.com/tdwgm/tuta-alias/tuta_alias_go` while `go.mod` sat in the
  repository root, so the go tool looked for a module in a `tuta_alias_go/`
  subdirectory that does not exist and `go install` could not resolve the
  package at all. `go mod tidy` also dropped four dependencies nothing
  imported (`golang.design/x/clipboard`, `x/exp/shiny`, `x/image`,
  `x/mobile`).
- `make build-all` builds linux/amd64 and linux/arm64. arm64 is where the
  vault matters: Termux runs Linux binaries and has no Secret Service
  keyring. Other systems still build from source, they are simply not
  released.
- `COLOR_RULE_FOOTER`, `COLOR_ICON` and `COLOR_BRACKET` are implemented
  rather than documented as deviations from the theme standard: the rule
  above the bottom menu, the `▶` cursor and the `✓ ! ✗` marks, and the
  `[ ] ( ) ›` punctuation. The theme group now matches the standard key for
  key, which `standard_parity_test.go` enforces.
- The Config screen is generated from the config field table, so a key cannot
  be in the file without a row, or in a row without being in the file. The
  theme group is a sub-page, opened with Enter on the **Theme** row.
- `THEME` is the accent key, as in the TUI Scaffold standard and its reference
  implementation. `COLOR_ACCENT` is still read and is dropped from the file on
  the next save. `BANNER_FONT`'s drawn logo is called `logo` (`default` still
  reads), and `light grey` is a colour.
- Help is `^h` on every screen, including inside a text field. That costs the
  field's `^h` backspace; the Backspace key is unaffected.
- Help's command list comes from cobra, and Help and the Config screen scroll.
- `install` rewrites the config file in the current format, keeping your
  values, your comments and keys it does not know.
- `uninstall --purge` says what it takes: the config directory also holds the
  saved `+` addresses and the alias notes.
- The binary is installed through a temp file and a rename, so overwriting a
  running binary does not fail with `ETXTBSY` and a partial write is not
  possible.
- Install and uninstall are one implementation (`internal/selfinstall`); the
  CLI and the TUI had drifted apart.

### Fixed

- **Ten of the twenty-one bottom-menu entries did nothing.** Enter on Copy,
  Plus, Note, Delete, Refresh, Log out, Reload, Reset theme, Uninstall or
  Regenerate printed "that menu entry is not wired up yet".
- **The Config screen never handled `^r`, `^t` or `^x`**, which its menu and
  Help had advertised from the start.
- **A login that finished while Help, Config, Doctor or Logs was open was
  thrown away.** Those four screens are reachable without a session, so the
  no-session guard in `Update` leaves the current screen alone while one of
  them is up, and nothing below it routed `loginDoneMsg`: the session was
  never saved, and back on the form every key was ignored because the phase
  was still "submitting". `^q` was the only way out. The login results and the
  spinner's ticks are routed at the top of `Update` now, like the saved-email
  lookup before them.
- **The login form's saved-login shortcut was dead.** It was `^g`, which the
  global key handler takes for Help before the login screen sees a key. It is
  `^l` now, and the test that covered it drives the real update loop instead
  of calling the screen's handler directly.
- **The alias hint advertised keys that did not exist** (`+ plus · n note ·
  x delete`); the keys are `^s`, `^t` and `^x`.
- `q` and `Esc` go back one level, as the navigation rows always claimed. Only
  the Help screen implemented it.
- **A newline in a config value could inject a key.** The writer puts
  `KEY="value"` on one line, so a value containing a newline split it and the
  second half was read back as another setting: `config set BANNER_TEXT
  $'x\nINSTALL_PATH="/elsewhere"'` rewrote a key the user never touched. Both
  the validator and `Save` refuse control characters now.
- **A duplicated key in a hand-edited file no longer looks ignored.** The last
  assignment wins, as it would in a shell, and writing the key collapses the
  earlier lines instead of leaving a stale one that disagrees with what the
  program reads.
- **Tab reached no screen.** The menu handler took it first, so Home could not
  move between its fields with it, the login form could not Tab from email to
  password, and the Plus screen's note field was unreachable by any key at
  all. Tab moves within the screen and enters the bottom menu from the last
  row.
- **A disabled alias can be re-enabled from the TUI** (`^x` on one that is
  already disabled). The TUI could disable an alias and show it as
  "[disabled]" forever; only `delete --restore` could undo it.
- `uninstall --purge` asked to remove the working directory when there was no
  home directory, and was saved only by the kernel refusing it.
- **Values round-trip through the config file again.** Writing escaped `\` and
  `"`, reading never unescaped them, so every save doubled them: a
  `SHOW_ALIASES_FOR` regexp stopped matching after two edits.
- Settings are validated where they are typed. `PLUS_SUFFIX_LEN="abc"` was
  accepted and silently became 5; an invalid `SHOW_ALIASES_FOR` regexp
  degraded to substring matching without a word.
- Opening Config from `^o` or the menu showed a bare "loading..." until the
  next keystroke, because only one of the three entry points built the
  screen's state.
- An unknown command or a missing argument exited 2 with no message at all.
- Uninstall's report no longer lands in the middle of a bubbletea frame.
- **The install wizard fits the terminal it is running on.** It drew at a
  fixed width, so on a narrow terminal the completion path wrapped mid-line
  and the "Press any key" footer scrolled out of view, which read as a hang.
  Every screen is measured against the real window size now, long paths and
  lines are elided rather than wrapped, the text inputs are resized on
  `WindowSizeMsg`, and any key leaves the done screen. A test renders every
  step at 61 columns and fails on an overflow.
- `COLOR_TEXT` and the other theme keys are checked against the standard by a
  test; `theme_std.go` was generated into the project and then referenced by
  nothing.

### Internal

- Parity tests drive the real update loop: every advertised shortcut and every
  menu entry must change something, every config key must have a row, every
  choice a screen offers must be a value the config file accepts.
- The guard that compares `layout.go` and `font.go` with the TUI Scaffold
  template now finds the template and actually runs.
- `config.Apply` is the single key-to-field setter. The Config screen's switch
  and the install wizard's struct literal were the second and third copies,
  and the third one is what dropped `TUTA_PROXY`.
- The references to a private machine path and to the author's own desktop
  setup are out of the source, the help text and the generated config file.
