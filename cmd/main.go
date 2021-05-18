package main

import (
	log "github.com/sirupsen/logrus"
	"os"

	cmd "code.cloudfoundry.org/quarks-container-run/cmd/containerrun"
	pkg "code.cloudfoundry.org/quarks-container-run/pkg/containerrun"
)

func main() {
	pkg.WriteBPMscript()
	if err := cmd.NewDefaultContainerRunCmd().Execute(); err != nil {
		log.Errorf("container-run failed: %v", err)
		os.Exit(1)
	}
}
