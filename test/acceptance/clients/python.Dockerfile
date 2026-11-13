# Client pins: Python 3.15.2; pip 26.3; Twine 7.0.0; uv 0.13.7; Poetry 2.5.1.
# The Python clients for the acceptance features (decision 0015, #534): pip,
# twine, uv and poetry on the newest Python 3. The base is pinned by digest
# and moved by Dependabot's docker entry; the four tools are pinned in
# python.requirements.in and poetry.requirements.in and moved by hand, since
# Dependabot reads only FROM lines. Poetry gets a virtual environment of its
# own, as its installation guide asks, so its dependencies never constrain
# twine's. The job builds it as artifactweave-accept-python.
#
# Each install has a second source, so one PyPI fault, such as a connection
# reset part way through a wheel, does not fail the build (#1321, #1439). pip
# installs the .txt file that pip-lock.sh writes from the .in file, which pins
# the tools and every dependency to the SHA-256 of each file PyPI holds for
# that release. So the second index can serve only PyPI's bytes: one that
# serves other bytes fails like one that does not answer. A second index
# without the hashes would let the mirror decide what runs. --only-binary keeps
# pip from building an sdist, whose build requirements pip would fetch with no
# hash check (ansible.Dockerfile, which has to build one, hashes them with
# --build-constraint). The second index is TUNA, Tsinghua University's mirror
# of the whole of PyPI, whose status page shows it syncing from pypi.org every
# few minutes. It keeps files.pythonhosted.org's packages/ paths and the simple
# API's digests. A caching proxy is no fallback, as it fetches a file it lacks
# from PyPI, and nor are release assets: only hf-xet attaches its wheels to its
# GitHub releases, and the dependencies need an index anyway. Each loop stops
# at the first index that installs the whole file, and fails the build when
# neither does.
FROM python:3.15.2-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
COPY python.requirements.txt poetry.requirements.txt /tmp/
RUN for index in https://pypi.org/simple https://pypi.tuna.tsinghua.edu.cn/simple; do \
      pip install --no-cache-dir --root-user-action=ignore --require-hashes --only-binary=:all: \
        --index-url "$index" -r /tmp/python.requirements.txt && break; \
    done \
 && python -m venv /opt/poetry \
 && for index in https://pypi.org/simple https://pypi.tuna.tsinghua.edu.cn/simple; do \
      /opt/poetry/bin/pip install --no-cache-dir --require-hashes --only-binary=:all: \
        --index-url "$index" -r /tmp/poetry.requirements.txt && break; \
    done \
 && ln -s /opt/poetry/bin/poetry /usr/local/bin/poetry \
 && rm /tmp/python.requirements.txt /tmp/poetry.requirements.txt
