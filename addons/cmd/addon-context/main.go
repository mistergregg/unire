// Command addon-context writes the Docker build context Unire uses for a set of
// add-ons, as a tar stream, so CI (and anyone) can build the same image:
//
//	go run ./addons/cmd/addon-context 3d-print cad | docker build --build-arg BASE=unire-sandbox -t test -
package main

import (
	"fmt"
	"os"

	"github.com/mistergregg/unire/addons"
)

func main() {
	ids := addons.Clean(os.Args[1:])
	if len(ids) != len(os.Args[1:]) || len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "usage: addon-context <add-on id>... (known:", known(), ")")
		os.Exit(2)
	}
	tarball, _, err := addons.Context(ids)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(tarball)
}

func known() string {
	s := ""
	for _, a := range addons.All {
		s += " " + a.ID
	}
	return s
}
