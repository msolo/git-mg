# git-mg
A tool to facilitate working with very large git repos.

Originally, git-mg was intended to be a complete wrapper that made git work sanely in the typical degenerate work environment which is substantially different than how many open source projects are managed. Much of this is as-yet unrealized, but some of the core tools are useful regardless.

# [git-sync](cmd/git-sync)

`git-sync` allows near-instant bidirectional syncing of arbitrarily large working directories by augmenting git in the common case of small incrememtal changes.

# [git-preflight](cmd/git-preflight)

`git-preflight` runs a set of triggers on files modified since the last merge base. The goal is to rapidly dispatch commands against the minimal set of files to ensure some basic repo hygiene. It can be thought of as a superset of build, test and lint-like processes.
