# The Python clients for the acceptance features (decision 0015, #534): pip,
# twine, uv and poetry on the newest Python 3. The base is pinned by digest
# and moved by Dependabot's docker entry; the four tools are pinned here and
# moved by hand, since Dependabot reads only FROM lines. Poetry gets a virtual
# environment of its own, as its installation guide asks, so its dependencies
# never constrain twine's. The job builds it as artifactweave-accept-python.
FROM python:3.15.0-slim@sha256:2da676046630d7bf14c6764ae1143dc7fa0377a53f94bf5a642f0b214aca5bce
RUN pip install --no-cache-dir --root-user-action=ignore \
      pip==26.2.1 twine==7.0.0 uv==0.13.2 \
 && python -m venv /opt/poetry \
 && /opt/poetry/bin/pip install --no-cache-dir poetry==2.5.1 \
 && ln -s /opt/poetry/bin/poetry /usr/local/bin/poetry
