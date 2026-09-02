package main

import (
	"flag"
	"io"
)

// Options captures the parsed command line configuration.
type Options struct {
	Info  string   // the "agent 信息说明" prompt / system context
	Files []string // -f reference files
	Model string   // model id
	URL   string   // OpenAI-compatible endpoint base path
	Key   string   // API key header value
	JSON  bool     // dump request JSON instead of calling
	Debug bool     // verbose diagnostics

	// runtime state
	once    bool // send once, then exit
	flagSet *flag.FlagSet
	out     io.Writer
}
