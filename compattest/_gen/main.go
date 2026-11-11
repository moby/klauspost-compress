// Command gen records compattest results. Run it with gen.sh, which builds it
// against an upstream release, an upstream source tree or this fork.
package main

import (
	"flag"
	"fmt"
	"os"

	"compatgen/compattest"
)

func main() {
	corpus := flag.String("corpus", "", "corpus directory")
	out := flag.String("o", "", "output file")
	source := flag.String("source", "", "source description stored in the results")
	flag.Parse()
	if *corpus == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	res, err := compattest.Record(*corpus)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	res.Source = *source
	if err := res.Save(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
