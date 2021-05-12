package main

import (
	"os"

	cmd "code.cloudfoundry.org/quarks-container-run/cmd/containerrun"
	pkg "code.cloudfoundry.org/quarks-container-run/pkg/containerrun"
)

func main() {
	pkg.WriteBPMscript()
	if err := cmd.NewDefaultContainerRunCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
