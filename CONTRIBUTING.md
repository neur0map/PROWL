# Contributing

Thanks for taking the time to improve Prowl.

## Before you start

Open an issue before a large change. Explain the problem, who it affects, and
what you want to change. Small fixes can go straight to a pull request.

Prowl is built first for Ryoku Arch Linux. Changes for other systems are
welcome when they do not add weight or make the Ryoku path harder to maintain.

## Sending a change

1. Keep the change focused.
2. Follow the style already used in the nearby files.
3. Format Go files with `gofumpt -w .`.
4. Run the test that covers the changed path.
5. Run `go build .` before opening the pull request.
6. Explain what changed and how you checked it.

Do not mix a Crush update into an unrelated Prowl change. The only Crush
changes considered for direct cherry-picking are security fixes that also
affect Prowl.

## License

Only send work that you have the right to share. By sending a contribution, you
agree that it may be distributed under the terms in [LICENSE.md](LICENSE.md).

No separate contributor agreement is required.
