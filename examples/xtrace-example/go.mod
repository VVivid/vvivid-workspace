module xtrace-example

go 1.26

require (
	github.com/mattn/go-sqlite3 v1.14.32
	github.com/xtrace-example/traceplugin v0.0.0
)

replace github.com/xtrace-example/traceplugin => ./traefik-plugin
