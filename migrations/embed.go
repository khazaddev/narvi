// Package migrations embeds this directory's SQL migration files into the
// compiled binary so golang-migrate's iofs source driver
// (github.com/golang-migrate/migrate/v4/source/iofs) can run them without
// external file access — supporting the eventual single-binary self-host
// story (§12.1).
package migrations

import "embed"

// FS embeds every migration file (both .up.sql and .down.sql) in this
// directory. Wrap it with iofs.New(migrations.FS, ".") to get a
// source.Driver for golang-migrate.
//
//go:embed *.sql
var FS embed.FS
