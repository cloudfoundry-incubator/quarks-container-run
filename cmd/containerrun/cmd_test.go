package containerrun_test

import (
	"fmt"
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	. "code.cloudfoundry.org/quarks-container-run/cmd/containerrun"
	pkg "code.cloudfoundry.org/quarks-container-run/pkg/containerrun"
)

var _ = Describe("NewContainerRunCmd", func() {
	It("constructs a new command", func() {
		cmd := NewContainerRunCmd(nil, nil, nil, nil, nil, pkg.Stdio{})
		Expect(cmd).ToNot(Equal(nil))
	})

	It("fails when the run argument returns an error", func() {
		expectedErr := fmt.Errorf("failed")
		run := func(
			_ pkg.Runner,
			_ pkg.Runner,
			_ pkg.Checker,
			_ pkg.PacketListener,
			_ pkg.Stdio,
			_ []string,
			_ string,
			_ string,
			_ bool,
			_ string,
			_ []string,
			_ string,
			_ []string,
		) error {
			return expectedErr
		}
		cmd := NewContainerRunCmd(run, nil, nil, nil, nil, pkg.Stdio{})
		origArgs := os.Args[:]
		os.Args = os.Args[:1]
		err := cmd.Execute()
		os.Args = origArgs[:]
		Expect(err).To(Equal(expectedErr))
	})

	It("succeeds when the run argument returns no error", func() {
		run := func(
			_ pkg.Runner,
			_ pkg.Runner,
			_ pkg.Checker,
			_ pkg.PacketListener,
			_ pkg.Stdio,
			_ []string,
			_ string,
			_ string,
			_ bool,
			_ string,
			_ []string,
			_ string,
			_ []string,
		) error {
			return nil
		}
		cmd := NewContainerRunCmd(run, nil, nil, nil, nil, pkg.Stdio{})
		origArgs := os.Args[:]
		os.Args = os.Args[:1]
		err := cmd.Execute()
		os.Args = origArgs[:]
		Expect(err).To(BeNil())
	})
})

var _ = Describe("NewDefaultContainerRunCmd", func() {
	It("constructs a new command", func() {
		cmd := NewDefaultContainerRunCmd()
		Expect(cmd).ToNot(Equal(nil))
	})
})
