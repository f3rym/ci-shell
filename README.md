# ci-shell

*[Русская версия](README.ru.md)*

**Not "run CI locally". Rather "take me back into the run that failed".**

The pipeline failed. The container where it happened is long gone — all you have left
is a log. One command, and you are inside that very run: same image, same variables,
same commit, cursor on the step that broke.

The interface is a ribbon of columns: deeper means further right. `→` moves right,
`←` moves back, and that is the only navigation rule on every screen.

```
 ci-shell

 РЕПОЗИТОРИИ                ПАЙПЛАЙНЫ f3rym/k8s-vault
 ╭───────────────────────╮  ╭─────────────────────────────────────────────────────────────────────╮
 │                       │  │    #         ветка                      коммит         когда        │
 │▾ gitlab.com           │  │ ✗  #25       main                       1e38a223       14 ч назад   │
 │  ▾ ferym              │  │ ✓  #24       main                       cf52da5c       4 дн назад   │
 │      k8s+vault        │  │ ✗  #23       main                       6082ed29       5 дн назад   │
 │      argo-cd-test     │  │ ✗  #22       main                       d115e6ea       5 дн назад   │
 │      task-15          │  │ ✗  #21       main                       9f4a569a       5 дн назад   │
 │  ▸ ferym              │  │ ✓  #20       main                       fd6578f1       5 дн назад   │
 ╰───────────────────────╯  ╰─────────────────────────────────────────────────────────────────────╯

 ↑↓ выбор · → глубже · ← назад · esc в начало · ? помощь · ctrl+c выход

 ▸ выберите проект слева, чтобы увидеть его пайплайны
```

> The interface speaks Russian. Localisation is not implemented yet.

The chain runs left to right all the way down: repositories → pipelines → jobs → steps
→ environment, secrets and step log. Columns split the frame by content, columns you
are done with collapse into a narrow strip, and `←` expands them back on the same item.
The bottom line always says which keys are available right now.

<!-- TODO: gif — commit history `fix ci`, `fix ci 2`, `please work` on the left; one command and a shell on the right -->

## Why

The familiar loop: edit a line → `git commit -m "fix ci"` → push → wait ten minutes →
read the log → guess again. Five iterations is an hour of your life and a garland of
junk commits.

Inside the container the same question takes seconds:

```
root@job:/builds/acme/app$ echo $DATABASE_URL      # right, it is empty
root@job:/builds/acme/app$ python --version        # 3.9, but 3.12 locally
root@job:/builds/acme/app$ pytest tests/test_x.py  # reproduced the failure in two seconds
```

And the point is that the fix gets verified right here, before the push: fix it, press
`R`, watch the step turn green, run it clean in a fresh container, take the patch back
into your repository. One clean commit instead of five.

## How it differs from act / gitlab-ci-local

|                        | act, gitlab-ci-local | ci-shell |
|------------------------|----------------------|----------|
| Starting point         | a yml config         | one specific failed run |
| Variables              | from the config      | from that exact run |
| Behaviour              | run everything from scratch | stop at the step that failed |
| Verifying a fix        | a new run from scratch | restart the step, then a clean run in place |
| Emulates the CI engine | yes                  | no — it restores state |

The last row is the important one: we do not emulate CI semantics, we restore the state
of one specific run. That is why "the same container" is meant literally.

## Install

Requires Go 1.25+ and Docker (or Apple `container` on macOS).

```bash
git clone https://github.com/f3rym/ci-shell && cd ci-shell
make build          # ./ci binary for your system
```

Cross builds: `make macos`, `make linux`, `make windows`, and `make macos-container` —
a macOS variant that calls Apple `container` instead of Docker.

## Usage

```bash
ci                          # the interface: a menu, and arrows from there on
ci shell <job url>          # straight into a specific job, no interface
ci shell 15789929204        # a number is enough from inside the repository
ci apply                    # carry the fixes into your repository
ci secrets                  # fill in the missing secrets
```

The first screen is a menu: your repositories, open a job by URL, add a key. You can
have several keys — GitLab and your own instances become separate roots of the
repository tree, and a job from one instance opens next to a project from another.

The token is asked for on first run and stored in `~/.config/ci-shell/config.yml` with
mode 0600. Scopes `read_api` and `read_repository` are required.

With no terminal (a pipe, `ssh` without `-t`, a run from CI) the interface does not
come up — the plain line-by-line mode works instead.

### Keys and commands

Moving around the ribbon is four keys, and they mean the same thing everywhere.

| Key | Movement | | Key | Action |
|---|---|---|---|---|
| `↑↓` / `kj` | within the current column | | `s` | shell in the container |
| `→` / `l` | to the column on the right, deeper | | `R` | restart the step |
| `←` / `h` / `q` | to the column on the left, back | | `A` | carry the fixes over |
| `⏎` | open whatever you are standing on | | `i` | change a variable's value |
| `esc` | to the leftmost column | | `g` | refresh |
| mouse click | same as `⏎` on that row | | `/` | filter, or search inside a log |
| `?` | help with the full key map | | `ctrl+c` | quit |

`⏎` and `→` differ in exactly one case: when the column on the right is already open,
`→` moves into it as it is, while `⏎` rebuilds it from whatever the cursor is on — so
picking a different job and pressing `⏎` opens the steps of that job.

| Command | Action | | Command | Action |
|---|---|---|---|---|
| `:R` | restart the failed step | | `:image <image>` | swap the image |
| `:rest` | run the remaining steps | | `:log` | the job's full log |
| `:clean` | clean run in a fresh container | | `:env` | environment full screen |
| `:A` | carry the fixes over | | `:secrets` | secrets file in your editor |
| `:commit` | commit what was carried over | | `:pull` | `git pull --ff-only` |
| `:!<command>` | run inside the container | | `:q` | quit |

### Variables and the log

The right column of the job screen switches between environment, secrets and step log.
Put the cursor on a variable and press `i` — an input field opens:

- **environment** — the value is visible while typing and lives for this session only;
- **secrets** — the value is hidden and goes into the secrets file, to be remembered.

Predefined `CI_*` variables are not editable: they are the identity of the run itself,
and drifting from them would make "the same commit" a lie. Neither are file variables —
there the value is the contents of a file, not a string.

Everything you changed during the session is left next to the work patch in an
`.edit_vars` file, as a list of names. There are deliberately no values in it, so the
file is safe to put anywhere.

The log belongs to a step, not to the job: the cursor on a step shows that step's log.
`R` restarts the step and replaces its log with a fresh one instead of appending. The
full-screen viewer opens with its own key — scrolling, `/` search with highlighting,
and a key that jumps straight to the point of failure.

## How it works

1. **Find the job** — by URL, by number, or through the browser of groups and projects.
2. **Pull the metadata** through the GitLab API: status, commit, image, steps.
3. **Assemble the environment**: predefined `CI_*`, project, group and pipeline
   variables; file variables are materialised as files. Masked values are handed over
   by the API, so we take them; `masked_and_hidden` ones are given to nobody, and for
   those there is a local secrets file.
4. **Prepare the code**: a `git worktree` at the job's commit if a repository is at
   hand; otherwise a shallow fetch into a cache mirror. The token reaches git through
   the environment and lands neither in `.git/config` nor in `ps`.
5. **Reproduce**: `docker run` of the same image with the code mounted and the
   environment assembled; steps run in order up to the one that failed, then a shell.

## When something is off

The tool does not exit with an error — it shows a screen with an input field or a ready
command: no token, missing scope, Docker not running, the snap build of Docker unable to
see the cache directory, secrets not filled in, an SSH key needed, a config using
`extends`. In every case it names what exactly to do, and the action can be retried
without leaving.

When reproduction is not exact, a banner is printed before the shell listing the
assumptions made — the image that was picked, submodules not fetched, artifacts not
restored. We do not pretend everything is perfect.

## Limits, stated honestly

- **shell-executor and hand-rolled runners** cannot be reproduced at all — the tool
  says so rather than pretending.
- **Runner cache** lives on the runner and is not available. Distributed cache in S3
  is planned.
- **Artifacts from earlier stages** are not restored yet. The solution is designed and
  written down — [docs/artifacts-design.md](docs/artifacts-design.md), in Russian — but
  no code exists for it.
- **There are no tests in this project.** That is a deliberate decision by the author,
  not an oversight: verification happens by reading the code and by live runs. Know
  this before relying on the tool for anything important.
- **`include`/`extends`** are not fully expanded — the tool says so explicitly and
  offers to set the image by hand rather than feeding you a wrong config.
- **A heavy image** takes a while on the first pull, and comes from the local cache
  afterwards.
- **GitHub Actions** are not supported yet. They will be — but only for jobs with
  `container:`: a plain `runs-on: ubuntu-latest` is a virtual machine, not a container,
  and cannot be honestly reproduced locally.

## License

The source is available to read, but this is **not** an open-source license.

Using it is free — run it for any purpose including work, build it from source, read
the code and modify it for yourself. Distribution is not allowed: no publishing copies,
no forks or derivative versions, no bundling the code into other products.

Full text — [LICENSE](LICENSE).
