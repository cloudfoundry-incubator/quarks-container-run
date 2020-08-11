FROM golang:1.13.15 AS containerrun

ENV GO111MODULE on

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o "/usr/local/bin/container-run" code.cloudfoundry.org/quarks-container-run/cmd

################################################################################
FROM scratch
COPY --from=containerrun /usr/local/bin/container-run /usr/local/bin/container-run
