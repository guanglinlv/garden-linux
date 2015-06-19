package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"

	"flag"

	"github.com/cloudfoundry-incubator/garden-linux/container_daemon"
	"github.com/syndtr/gocapability/capability"
)

// proc_starter starts a user process with the correct rlimits and after
// closing any open FDs.
func main() {
	runtime.LockOSThread()

	rlimits := flag.String("rlimits", "", "encoded rlimits")
	dropCapabilities := flag.Bool("dropCapabilities", true, "drop capabilties before starting process")
	uid := flag.Int("uid", -1, "user id to run the process as")
	gid := flag.Int("gid", -1, "group id to run the process as")
	flag.Parse()

	closeFds()

	mgr := &container_daemon.RlimitsManager{}
	mgr.Apply(mgr.DecodeLimits(*rlimits))

	args := flag.Args()

	programPath, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Program '%s' was not found in $PATH: %s\n", args[0], err)
		os.Exit(255)
	}

	if *dropCapabilities {
		limitCapabilities()
	}

	runAsUser(*uid, *gid, programPath, args)
}

func runAsUser(uid, gid int, programPath string, args []string) {
	_, _, errNo := syscall.RawSyscall(syscall.SYS_SETGID, uintptr(gid), 0, 0)
	if errNo != 0 {
		fmt.Fprintf(os.Stderr, "setgid: %s", errNo.Error())
		os.Exit(255)
	}
	_, _, errNo = syscall.RawSyscall(syscall.SYS_SETUID, uintptr(uid), 0, 0)
	if errNo != 0 {
		fmt.Fprintf(os.Stderr, "setuid: %s", errNo.Error())
		os.Exit(255)
	}

	err := syscall.Exec(programPath, args, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: exec: %s\n", err)
		os.Exit(255)
	}
}

func limitCapabilities() {
	caps, err := capability.NewPid(os.Getpid())
	mustNot(err)

	caps.Clear(capability.BOUNDING)
	caps.Set(capability.BOUNDING,
		capability.CAP_DAC_OVERRIDE,
		capability.CAP_FSETID,
		capability.CAP_FOWNER,
		capability.CAP_MKNOD,
		capability.CAP_NET_RAW,
		capability.CAP_SETGID,
		capability.CAP_SETUID,
		capability.CAP_SETFCAP,
		capability.CAP_SETPCAP,
		capability.CAP_NET_BIND_SERVICE,
		capability.CAP_SYS_CHROOT,
		capability.CAP_KILL,
		capability.CAP_AUDIT_WRITE,
	)

	must(caps.Apply(capability.BOUNDING))
}

func closeFds() {
	fds, err := ioutil.ReadDir("/proc/self/fd")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: read /proc/self/fd: %s", err)
		os.Exit(255)
	}

	for _, fd := range fds {
		if fd.IsDir() {
			continue
		}

		fdI, err := strconv.Atoi(fd.Name())
		if err != nil {
			panic(err) // cant happen
		}

		if fdI <= 2 {
			continue
		}

		syscall.CloseOnExec(fdI)
	}
}

var must = mustNot

func mustNot(err error) {
	if err != nil {
		panic(err)
	}
}
