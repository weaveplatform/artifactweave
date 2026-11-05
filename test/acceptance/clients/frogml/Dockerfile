# Client pins: FrogML 3.0.0; Python 3.13.18.
# FrogML 3.0.0, like its newest release, declares Python >=3.9,<3.14, so pip
# refuses it on Python 3.14 and later; Dependabot moved this image to Python
# 3.15 in #934. It has a directory of its own because Dependabot's entry for
# it keeps it below Python 3.14, which an entry shared with python.Dockerfile
# and ansible.Dockerfile would do to them too. The job builds it as
# artifactweave-accept-frogml.
FROM python:3.13.18-slim@sha256:ae14d4ce8771606477a81446e7ef429edd6762305fc50c84507c1041fa8285d1
RUN pip download --no-deps --dest /tmp frogml==3.0.0 \
 && echo 'b8e83651ad8303ba4970a439161b0ce8f3cd88d0f8dfdb966e2cba2316299f7e  /tmp/frogml-3.0.0-py3-none-any.whl' | sha256sum -c - \
 && pip install --no-cache-dir /tmp/frogml-3.0.0-py3-none-any.whl \
 && rm /tmp/frogml-3.0.0-py3-none-any.whl

# Retain the installed client identity in the native image build log.
RUN python -c 'import importlib.metadata; print("FrogML", importlib.metadata.version("frogml"))'
