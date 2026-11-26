# Additional owned instances for the federated provider scenario. The primary
# fixture and its provider credentials continue to belong to run.sh.
peer_servers=()
peer_forwarders=()
peer_dirs=()
peer_coverage=()

federated_prepare() {
	if printf '%s\n' "${scenarios[@]}" | grep -qx deploy/artifactweave/terraform/federated-repositories; then
		read -r peer_port_one peer_port_two < <(python3 -I "$here/check-p13.py" --ports "$port")
		export TERRAFORM_FIXTURE_ORIGINS
		TERRAFORM_FIXTURE_ORIGINS=$(python3 -I "$here/check-p13.py" --origins "$port" "$peer_port_one" "$peer_port_two")
	fi
}

require_federated_servers() {
	if [ ${#peer_servers[@]} -eq 0 ]; then return; fi
	local pid path
	for pid in "${peer_servers[@]}" ${peer_forwarders[@]+"${peer_forwarders[@]}"}; do
		kill -0 "$pid" 2>/dev/null || { echo "Federated fixture child exited" >&2; exit 1; }
	done
	for path in ${peer_dirs[@]+"${peer_dirs[@]}"}; do
		[ ! -f "$path/forwarder.failed" ] || { echo "Federated fixture frontend quarantined" >&2; exit 1; }
	done
}

start_federated_fixture() {
	local number=$1 public_port=$2 fixture coverage pid backend frontend= deadline
	fixture="$work/federation-peer-$number"
	coverage="$work/federation-cover-$number"
	mkdir -p "$fixture" "$coverage"
	ln "$work/artifactweave" "$fixture/artifactweave"
	python3 -I "$here/fixture-server.py" launch "$fixture" "$public_port" "$coverage" >"$fixture/server.log" 2>&1 &
	pid=$!
	peer_servers+=("$pid")
	peer_dirs+=("$fixture")
	peer_coverage+=("$coverage")
	deadline=$((SECONDS + 10))
	while [ "$SECONDS" -lt "$deadline" ]; do
		require_server
		if backend=$(python3 -I "$here/fixture-server.py" address "$fixture/server.log"); then
			if [ -z "$frontend" ]; then
				python3 -I "$here/fixture-server.py" forward "$pid" "$backend" "$public_port" "$fixture/forwarder.ready" >"$fixture/forwarder.log" 2>&1 &
				frontend=$!
				peer_forwarders+=("$frontend")
			fi
			if [ -f "$fixture/forwarder.ready" ] && command curl --noproxy '*' --max-time 2 -fs "http://127.0.0.1:$public_port/artifactory/api/v1/system/readiness" >/dev/null; then
				require_server
				return
			fi
		fi
		sleep .1
	done
	echo "Federated fixture failed its owned-listener readiness check" >&2
	exit 1
}

federated_cleanup() {
	if [ ${#peer_servers[@]} -eq 0 ]; then return; fi
	local pid path result=0 joined
	for pid in ${peer_forwarders[@]+"${peer_forwarders[@]}"} "${peer_servers[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
	for pid in ${peer_forwarders[@]+"${peer_forwarders[@]}"} "${peer_servers[@]}"; do wait "$pid" || result=1; done
	for path in ${peer_dirs[@]+"${peer_dirs[@]}"}; do
		if [ "$result" -ne 0 ]; then tail -n 80 "$path/server.log" >&2; fi
	done
	# Each runtime emits into its own directory. Collection finishes before
	# run.sh stops the primary server and its coverage runtime writes.
	joined=$(IFS=,; echo "${peer_coverage[*]}")
	go tool covdata merge -i="$joined" -o="${GOCOVERDIR:-$work/cover}" || result=1
	return "$result"
}

apply_federated_repositories() {
	start_federated_fixture 1 "$peer_port_one"
	start_federated_fixture 2 "$peer_port_two"
	export P13_MEMBERS_FILE="$work/federation-members.json" P13_LEDGER="$work/p13-steps.jsonl" \
		P13_SERVER_LOG="$work/server.log" P13_UNPAIRED_RESULT="$work/p13-unpaired.json" TF_VAR_name_prefix=tf-fed
	python3 -I "$here/check-p13.py" --setup "$dir"
	for revision in 1 2; do
		export TF_VAR_description="Three-member federation revision $revision"
		tf apply -auto-approve
		python3 -I "$here/check-p13.py" --step "apply-$revision"
		tf apply -refresh-only -auto-approve
		tf plan -detailed-exitcode
		python3 -I "$here/check-p13.py" --step "plan-$revision"
		python3 -I "$here/check-p13.py" --check "$revision"
		require_server
	done
	cp "$here/federated-unpaired.tf.in" "$dir/unpaired.tf"
	tf plan -no-color -out="$work/unpaired.plan"
	if tf apply -no-color "$work/unpaired.plan" >"$work/unpaired-apply.log" 2>&1; then
		echo "Unpaired member create unexpectedly succeeded" >&2; exit 1
	fi
	tf state list >"$work/unpaired-state.txt"
	python3 -I "$here/check-p13.py" --unpaired "$work/unpaired-apply.log" "$work/unpaired-state.txt"
	rm "$dir/unpaired.tf"
}
