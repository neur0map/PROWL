//go:build !sqlite_fts5

package main

// Without this tag the binary builds and then fails every index operation at
// runtime with "no such module: fts5", logged only as a warning. The
// identifier is undefined on purpose: its name is the compiler's message.
var _ = prowl_must_be_built_with_tags_sqlite_fts5_see_README_Install
