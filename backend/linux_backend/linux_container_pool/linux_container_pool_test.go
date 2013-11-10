package linux_container_pool_test

import (
	"errors"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/vito/garden/backend"
	"github.com/vito/garden/backend/linux_backend/linux_container_pool"
	"github.com/vito/garden/command_runner/fake_command_runner"
	. "github.com/vito/garden/command_runner/fake_command_runner/matchers"
)

var _ = Describe("Setup", func() {
	var fakeRunner *fake_command_runner.FakeCommandRunner
	var pool *linux_container_pool.LinuxContainerPool

	BeforeEach(func() {
		fakeRunner = fake_command_runner.New()
		pool = linux_container_pool.New("/root/path", "/depot/path", "/rootfs/path", fakeRunner)
	})

	It("executes setup.sh with the correct environment", func() {
		err := pool.Setup()
		Expect(err).ToNot(HaveOccured())

		Expect(fakeRunner).To(HaveExecutedSerially(
			fake_command_runner.CommandSpec{
				Path: "/root/path/setup.sh",
				Env: []string{
					"POOL_NETWORK=10.254.0.0/24",
					"ALLOW_NETWORKS=",
					"DENY_NETWORKS=",
					"CONTAINER_ROOTFS_PATH=/rootfs/path",
					"CONTAINER_DEPOT_PATH=/depot/path",
					"CONTAINER_DEPOT_MOUNT_POINT_PATH=/",
					"DISK_QUOTA_ENABLED=true",

					"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				},
			},
		))
	})

	Context("when setup.sh fails", func() {
		nastyError := errors.New("oh no!")

		BeforeEach(func() {
			fakeRunner.WhenRunning(
				fake_command_runner.CommandSpec{
					Path: "/root/path/setup.sh",
				}, func() error {
					return nastyError
				},
			)
		})

		It("returns the error", func() {
			err := pool.Setup()
			Expect(err).To(Equal(nastyError))
		})
	})
})

var _ = Describe("Creating", func() {
	var fakeRunner *fake_command_runner.FakeCommandRunner
	var pool *linux_container_pool.LinuxContainerPool

	BeforeEach(func() {
		fakeRunner = fake_command_runner.New()
		pool = linux_container_pool.New("/root/path", "/depot/path", "/rootfs/path", fakeRunner)
	})

	It("returns containers with unique IDs", func() {
		container1, err := pool.Create(backend.ContainerSpec{})
		Expect(err).ToNot(HaveOccured())

		container2, err := pool.Create(backend.ContainerSpec{})
		Expect(err).ToNot(HaveOccured())

		Expect(container1.ID()).ToNot(Equal(container2.ID()))
	})

	It("executes create.sh with the correct args and environment", func() {
		container, err := pool.Create(backend.ContainerSpec{})
		Expect(err).ToNot(HaveOccured())

		Expect(fakeRunner).To(HaveExecutedSerially(
			fake_command_runner.CommandSpec{
				Path: "/root/path/create.sh",
				Args: []string{"/depot/path/" + container.ID()},
				Env: []string{
					"id=" + container.ID(),
					"rootfs_path=/rootfs/path",
					"allow_nested_warden=false",
					"container_iface_mtu=1500",

					"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				},
			},
		))
	})

	Context("when executing create.sh fails", func() {
		nastyError := errors.New("oh no!")

		BeforeEach(func() {
			fakeRunner.WhenRunning(
				fake_command_runner.CommandSpec{
					Path: "/root/path/create.sh",
				}, func() error {
					return nastyError
				},
			)
		})

		It("returns the error", func() {
			_, err := pool.Create(backend.ContainerSpec{})
			Expect(err).To(Equal(nastyError))
		})
	})
})

var _ = Describe("Destroying", func() {
	var fakeRunner *fake_command_runner.FakeCommandRunner
	var pool *linux_container_pool.LinuxContainerPool

	BeforeEach(func() {
		fakeRunner = fake_command_runner.New()
		pool = linux_container_pool.New("/root/path", "/depot/path", "/rootfs/path", fakeRunner)
	})

	It("executes destroy.sh with the correct args and environment", func() {
		container, err := pool.Create(backend.ContainerSpec{})
		Expect(err).ToNot(HaveOccured())

		err = pool.Destroy(container)
		Expect(err).ToNot(HaveOccured())

		Expect(fakeRunner).To(HaveExecutedSerially(
			fake_command_runner.CommandSpec{
				Path: "/root/path/destroy.sh",
				Args: []string{"/depot/path/" + container.ID()},
				Env: []string{
					"id=" + container.ID(),
					"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
				},
			},
		))
	})
})