# git-sync

`git-sync` allows near-instant syncing of arbitrarily large working directories.

`git-sync` uses `SSH`, `rsync` and `git` (and optionally `watchman` via fsmonitor) to efficiently copy local working directory changes to a mirrored copy on a different machine. The goal is generally to be able to reliably sync (even on an LTE connection) within 500ms even if the repo contains more than 250k files. It's often possible to sync in under 250ms, which is a reasonable threshold for "instant".

`git-sync` is destructive to the target working directory - it will `git {clean,reset,checkout}` to ensure the source and
destination working directories are equivalent.

## git-sync Config
`git-sync` reads a few variables from the `[sync]` section of the git config:

### sync.remoteName (default "sync")

This sets remote target to use for syncing changes, including the SSH URL used for `rsync` operations.

### sync.excludePaths (default empty)

A colon-delimited list of patterns that will be passed to `git clean` on the remote target.  This allows some remote data to persist, even if it does not exist in the source workdir.

### sync.rsyncRemotePath (default "/usr/local/bin/rsync")

The path for the remote `rsync` binary.

### core.fsmonitor

If `core.fsmonitor` is configured, it will be used to find changes quickly. A good implementation of `git-fsmonitor` is included in this repo.

## git-sync Quick Start

For the fastest performance, you will need `watchman` installed. On OS X this is easy, your mileage may vary.
```
brew install watchman
```

Install tools and configure git:
```
# Get the tools installed
go install -v github.com/msolo/git-mg/cmd/git-sync
go install -v github.com/msolo/git-mg/cmd/git-fsmonitor

# watch the repository
watchman watch ./

# Configure git - more relevant for large repos, but generally harmless.
git config core.fsmonitor git-fsmonitor
git config core.fsmonitorhookversion 1
git config core.untrackedcache true
git update-index --index-version 4 --split-index --untracked-cache --fsmonitor --refresh
```

Bash nuts can get albeit simplistic completion by appending the following to their `.bashrc`:
```
which git-sync > /dev/null && complete -C git-sync git-sync
```

Configure git-sync in repo of your choosing:
```
git remote set-url sync phoenix.casa:src/my-project
git-sync push
```

You can also pull changes from the remote workdir. This is not without some risk, and depending on your development model might not be necessary or even a good idea. That said, it has proved handy in a number of cases where the development platform (usually OS X) does not match the test/deploy platform (usually Linux) and the development environment does not have a full set of cross-compiling tools.

```
git-sync pull
```

However, most of the time you will end up using in a batch of commands like so:
```
git-sync push && ssh remote "cd src; run-horrible-codegen" && git-sync pull
```
