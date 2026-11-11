# Client pins: Python 3.15.2; pip 26.3; Twine 7.0.0; uv 0.13.7; Poetry 2.5.1.
# The Python clients for the acceptance features (decision 0015, #534): pip,
# twine, uv and poetry on the newest Python 3. The base is pinned by digest
# and moved by Dependabot's docker entry; the four tools are pinned here and
# moved by hand, since Dependabot reads only FROM lines. Poetry gets a virtual
# environment of its own, as its installation guide asks, so its dependencies
# never constrain twine's. The job builds it as artifactweave-accept-python.
FROM python:3.15.2-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
RUN pip install --no-cache-dir --root-user-action=ignore \
      pip==26.3 twine==7.0.0 uv==0.13.7 \
 && python -m venv /opt/poetry \
 && /opt/poetry/bin/pip install --no-cache-dir poetry==2.5.1 \
 && ln -s /opt/poetry/bin/poetry /usr/local/bin/poetry
