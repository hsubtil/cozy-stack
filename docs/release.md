[Table of contents](README.md#table-of-contents)

# Building a release

To build a release of cozy-stack, a `build.sh` script can automate the work. The `release` option of this script will generate a binary with a name containing the version of the file, along with a SHA-256 sum of the binary.

You can use a `local.env` at the root of the repository to add your default values for environment variables.

See `./scripts/build.sh --help` for more informations.

```sh
COZY_ENV=development GOOS=linux GOARCH=amd64 ./scripts/build.sh release
```

The version string is deterministic and reflects entirely the state of the working-directory from which the release is built from. It is generated using the following format:

        <TAG>[-<NUMBER OF COMMITS AFTER TAG>][-dirty][-dev]

Where:

 - `<TAG>`: closest annotated tag of the current working directory. If no tag is present, is uses the string "v0". This is not allowed in a production release.
 - `<NUMBER OF COMMITS AFTER TAG>`: number of commits after the closest tag if the current working directory does not point exactly to a tag
 - `dirty`: added if the working if the working-directory is not clean (contains un-commited modifications). This is not allowed in production release.
 - `dev`: added for a development mode release
