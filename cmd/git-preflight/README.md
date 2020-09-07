# git-preflight

git-preflight examines local changes to a git working directory and dispatches trigger commands based on the patterns matched by modified files.

## Why do this?
In small repos, this may not be particularly more valueable than a `Makefile` with a handy `make all`. In fact, you might ask why such a thing is necessary if you have an adequate build system with various tests, linting and change-detection mechanisms.  The answer is somewhat subtle. A good build system should not modify the contents of its repository. Similarly, a `Makefile` that modifies its inputs is generally a situation one avoids, even if it's something seemingly innocuous, like running a formatter. It's an arbitrary distinction, but one that proves valuable in gigantic repos.

Having a standard config for triggers permits easy subsetting, tagging or other forms of analysis later one. However attractive a shell script seems at first blush, its beauty is lost on the next poor soul that has to modify it.

# Config

Triggers are stored in the repository root in `.git-preflight`. The file is JSONC - which is simply JSON with the added wonderfeature of comments. Right now there is only one `.git-preflight` per repo - more didn't seem to make a lot of sense based on how it is used.

Includes and Excludes patterns are interpreted similarly to fnmatch rules, though patterns without a / character will be matched against the file name only, not the path.

```
{
  "Triggers": [
    {
      Name: "gofmt",
      Cmd: ["gofmt", "-w"],
      InputType: "args",
      Includes: ["*.go"],
      Excludes: ["vendor/", "thirdparty/"] // Don't bother other people's code.
    }
  ]
}
```

# Running
```
git-prelight <trigger name> [-v]
```

With `-v`, the tool logs verbosely to the console and injects GIT_PREFLIGHT_VERBOSE=1 into the environment of all triggers so that downstream processes can emit their own logs.
