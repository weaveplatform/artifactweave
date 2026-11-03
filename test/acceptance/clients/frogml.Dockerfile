# Client pins: FrogML 3.0.0; Python 3.13.18.
# Python 3.13 image. Python 3.14/3.15 are outside the SDK's declared range.
FROM python:3.13.18-slim@sha256:ae14d4ce8771606477a81446e7ef429edd6762305fc50c84507c1041fa8285d1
RUN pip download --no-deps --dest /tmp frogml==3.0.0 \
 && echo 'b8e83651ad8303ba4970a439161b0ce8f3cd88d0f8dfdb966e2cba2316299f7e  /tmp/frogml-3.0.0-py3-none-any.whl' | sha256sum -c - \
 && pip install --no-cache-dir /tmp/frogml-3.0.0-py3-none-any.whl \
 && rm /tmp/frogml-3.0.0-py3-none-any.whl
