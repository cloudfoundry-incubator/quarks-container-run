package containerrun

import (
	"io/ioutil"
	"os"
	"path/filepath"
)

// WriteBPMscript creates a bpm script for drain script compatibility.
func WriteBPMscript() error {
	fileName := "/var/vcap/jobs/bpm/bin/bpm"

	script := `#!/bin/bash

function usage {
    echo "usage: $0 [start|stop|quit|running] JOBNAME [-p PROCESSNAME]"
    exit 1
}

if [ $# != 2 -a $# != 4 ]; then
    usage
fi
if [ "$1" != "start" -a "$1" != "stop" -a "$1" != "quit" -a "$1" != "running" ]; then
    usage
fi

CMD="$1"
JOB="$2"
PROCESS="$2"

if [ $# == 4 ]; then
    if [ "$3" != "-p" ]; then
        usage
    fi
    PROCESS="$4"
fi

CONTAINER_RUN="/var/vcap/data/${JOB}/${PROCESS}_containerrun"
if [ "$CMD" == "running" ]; then
    test -f "${CONTAINER_RUN}.running"
else
    echo "${CMD^^}" | nc -w 1 -uU "${CONTAINER_RUN}.sock"
fi
`
	if _, err := os.Stat(fileName); !os.IsNotExist(err) {
		// Nothing to do if the file already exists
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(fileName), 0755); err != nil {
		return err
	}
	return ioutil.WriteFile(fileName, []byte(script), 0755)
}
