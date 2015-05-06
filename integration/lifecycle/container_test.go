package lifecycle_test

import (
	"github.com/cloudfoundry-incubator/garden"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Container operations", func() {
	var container garden.Container
	var args []string

	BeforeEach(func() {
		args = []string{}
	})

	JustBeforeEach(func() {
		client = startGarden(args...)
	})

	AfterEach(func() {
		if container != nil {
			Expect(client.Destroy(container.Handle())).To(Succeed())
		}
	})

	Context("with a default rootfs", func() {
		PIt("the container is created successfully", func() {
			var err error

			container, err = client.Create(garden.ContainerSpec{RootFSPath: ""})
			Expect(err).ToNot(HaveOccurred())
		})

		PIt("the container is in a new namespace", func() {})
	})
})
