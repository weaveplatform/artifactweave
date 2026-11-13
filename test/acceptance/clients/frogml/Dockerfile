# Client pins: FrogML 3.0.0; Python 3.13.18.
# FrogML 3.0.0, like its newest release, declares Python >=3.9,<3.14, so pip
# refuses it on Python 3.14 and later; Dependabot moved this image to Python
# 3.15 in #934. It has a directory of its own because Dependabot's entry for
# it keeps it below Python 3.14, which an entry shared with python.Dockerfile
# and ansible.Dockerfile would do to them too. The job builds it as
# artifactweave-accept-frogml.
#
# FrogML is pinned in requirements.in; pip installs the hashed file
# ../pip-lock.sh writes from it, from PyPI and then from TUNA's mirror of PyPI,
# so one PyPI fault does not fail the build and the mirror can serve only
# PyPI's bytes (#1321, #1439). ../python.Dockerfile says why TUNA and why the
# hashes. The FrogML wheel's SHA-256 is in the file with its dependencies'.
FROM python:3.13.18-slim@sha256:e48f59cdba69a3cbd9ed2cf0f348d90d45a8d350d98134cf1070bc106ee9ed90
COPY requirements.txt /tmp/frogml.requirements.txt
RUN export https_proxy=http://127.0.0.1:9 no_proxy=pypi.tuna.tsinghua.edu.cn \
 && for index in https://pypi.org/simple https://pypi.tuna.tsinghua.edu.cn/simple; do \
      pip install --no-cache-dir --require-hashes --only-binary=:all: \
        --index-url "$index" -r /tmp/frogml.requirements.txt && break; \
    done \
 && rm /tmp/frogml.requirements.txt

# Retain the installed client identity in the native image build log.
RUN python -c 'import importlib.metadata; print("FrogML", importlib.metadata.version("frogml"))'
