# FrogML 2.1.153 requires Python >=3.9,<3.14; use the newest supported
# Python 3.13 image. Python 3.14/3.15 are outside the SDK's declared range.
FROM python:3.13-slim@sha256:2d44738ebf4ce5f63b9d9ea871e9c7cfdc0426f7b0c97cac915fd88ded1d86bb
RUN pip download --no-deps --dest /tmp frogml==2.1.153 \
 && echo '7b2804539c5912410cdca55ea6ae89169afb4f1616d0777be0ac42f898bf8663  /tmp/frogml-2.1.153-py3-none-any.whl' | sha256sum -c - \
 && pip install --no-cache-dir /tmp/frogml-2.1.153-py3-none-any.whl \
 && rm /tmp/frogml-2.1.153-py3-none-any.whl
