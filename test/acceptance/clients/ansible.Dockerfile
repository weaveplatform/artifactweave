# Client pins: ansible-core 2.21.5 on Python 3.15.0.
# The Python 3.15 bump in #895 removes a dataclasses hook used by this client.
FROM python:3.15.0-slim@sha256:f5b92f2a669a4c347044f29367f31983280b648250ca94dea66f98c53bc87e58
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.21.5 \
 && ansible-galaxy --version | head -1 | grep -F '2.21.5'
