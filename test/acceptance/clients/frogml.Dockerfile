# Client pins: FrogML 3.0.0; Python 3.15.0.
# Python 3.13 image. Python 3.14/3.15 are outside the SDK's declared range.
FROM python:3.15.0-slim@sha256:f5b92f2a669a4c347044f29367f31983280b648250ca94dea66f98c53bc87e58
RUN pip download --no-deps --dest /tmp frogml==3.0.0 \
 && echo 'b8e83651ad8303ba4970a439161b0ce8f3cd88d0f8dfdb966e2cba2316299f7e  /tmp/frogml-3.0.0-py3-none-any.whl' | sha256sum -c - \
 && pip install --no-cache-dir /tmp/frogml-3.0.0-py3-none-any.whl \
 && rm /tmp/frogml-3.0.0-py3-none-any.whl

# Retain the installed client identity in the native image build log.
RUN python -c 'import importlib.metadata; print("FrogML", importlib.metadata.version("frogml"))'
