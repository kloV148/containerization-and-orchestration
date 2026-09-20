#!/bin/bash
set -euo pipefail

# process
COMMAND="./api"

IDENTIFIER="${COMMAND}-$$"

CGROUP_PATH=""
UNSHARE_PID=""
CONTAINER_PID=""

# namespace settings
UNSHARE_ARGS=(-mpnuifr --mount-proc --kill-child=SIGTERM)

# capabilities settings, all restricted by default
BOUNDING_CAPS="-all"
INHERITABLE_CAPS="-all"
AMBIENT_CAPS="-all"

# Docker-inspired seccomp denylist for the demo.
# enosys can only block whole syscalls, so this is not an exact copy of
# Docker's allowlist with argument- and capability-dependent rules.
BLOCKED_SYSCALLS=(
	acct
	add_key
	bpf
	clock_adjtime
	clock_settime
	delete_module
	finit_module
	get_mempolicy
	init_module
	ioperm
	iopl
	io_uring_enter
	io_uring_register
	io_uring_setup
	kcmp
	kexec_file_load
	kexec_load
	keyctl
	lookup_dcookie
	mbind
	mount
	move_pages
	open_by_handle_at
	perf_event_open
	personality
	pivot_root
	process_vm_readv
	process_vm_writev
	ptrace
	quotactl
	reboot
	request_key
	set_mempolicy
	setns
	settimeofday
	swapoff
	swapon
	umount2
	unshare
	userfaultfd
)

ENOSYS_ARGS=()
for syscall in "${BLOCKED_SYSCALLS[@]}"; do
	ENOSYS_ARGS+=(-s "$syscall")
done

# cgroup settings
# 0.5 cpu by default
MAX_CPU="50000 100000"
# 32 MB RAM by default
MAX_RAM="32000000"
# 3 processes by default
MAX_PIDS="5"


cleanup() {
	set +e
	echo 1 > "$CGROUP_PATH/cgroup.kill"
	kill -KILL "$UNSHARE_PID"
	wait "$UNSHARE_PID"
	rmdir "$CGROUP_PATH"
}


create_cgroup() {
	CGROUP_PATH="/sys/fs/cgroup/${IDENTIFIER}"

	mkdir -p "$CGROUP_PATH"

	echo "$MAX_CPU" > "$CGROUP_PATH/cpu.max"
	echo "$MAX_RAM" > "$CGROUP_PATH/memory.max"
	echo "$MAX_PIDS" > "$CGROUP_PATH/pids.max"
}

run_process() {
	unshare "${UNSHARE_ARGS[@]}" -- \
		setpriv "--bounding-set=$BOUNDING_CAPS" "--inh-caps=$INHERITABLE_CAPS" "--ambient-caps=$AMBIENT_CAPS" -- \
		enosys "${ENOSYS_ARGS[@]}" -- "$COMMAND" &

	UNSHARE_PID=$!

	# wait until process start
	for _ in {1..5}; do
		sleep 1
		CONTAINER_PID=$(pgrep -P "$UNSHARE_PID" || true)
		if [[ -n "$CONTAINER_PID" ]]; then
			break
		fi
	done

	if [[ -z "$CONTAINER_PID" ]]; then
		echo "Container process was not started" >&2
		return 1
	fi

	nsenter --target "$CONTAINER_PID" --net -- ip link set lo up
	echo "$CONTAINER_PID" > "$CGROUP_PATH/cgroup.procs"
	wait "$UNSHARE_PID"
}


main() {
	create_cgroup
	run_process
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

main "$@"
