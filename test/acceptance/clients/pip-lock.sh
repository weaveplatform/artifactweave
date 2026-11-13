#!/bin/sh
# pip-lock [UV-ARGS...] writes each Python client's hashed requirements file,
# NAME.txt, from the pins in NAME.in. Run it with uv on PATH after moving a pin
# in a .in file; pass --upgrade to move the dependencies to their newest
# releases as well, which without it stay where the .txt file has them.
#
# The images and the Hugging Face and Conan workflow steps install these files
# with --require-hashes, first from PyPI and then from a second index, so the
# second index can only serve the bytes PyPI holds (#1321, #1439, #1455). uv
# writes the hash of every file PyPI holds for each pinned version, so one file
# serves the amd64 runners and an arm64 laptop alike. Each file is resolved for
# Linux, the only OS the clients run on, and for the Python that installs it:
# the image's base for the images; the runner's own python3, which is 3.14 on
# ubuntu-26.04, for the Hugging Face step; and setup-python's 3.12 for the
# Conan step.
set -eu
cd "$(dirname "$0")"

python_of() {
	sed -n 's/^FROM python:\([0-9]*\.[0-9]*\)\..*/\1/p' "$1"
}

lock() {
	in=$1
	python=$2
	shift 2
	[ -n "$python" ] || {
		echo "pip-lock: no Python version for $in" >&2
		exit 1
	}
	uv pip compile --quiet --generate-hashes --python-platform linux \
		--python-version "$python" \
		--custom-compile-command test/acceptance/clients/pip-lock.sh \
		--output-file "${in%.in}.txt" "$@" "$in"
}

lock python.requirements.in "$(python_of python.Dockerfile)" "$@"
lock poetry.requirements.in "$(python_of python.Dockerfile)" "$@"
lock ansible.requirements.in "$(python_of ansible.Dockerfile)" "$@"
lock ansible.build-requirements.in "$(python_of ansible.Dockerfile)" "$@"
lock frogml/requirements.in "$(python_of frogml/Dockerfile)" "$@"
lock huggingface.requirements.in 3.14 "$@"
lock conan2.requirements.in 3.12 "$@"
lock conan1.requirements.in 3.12 "$@"
lock conan1.build-requirements.in 3.12 "$@"
