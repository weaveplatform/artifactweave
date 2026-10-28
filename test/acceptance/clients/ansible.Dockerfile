# ansible-core 2.21.5 (current stable checked 2026-10-28), on supported Python 3.14.
FROM python:3.15.0-slim@sha256:f5b92f2a669a4c347044f29367f31983280b648250ca94dea66f98c53bc87e58
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.21.5 \
 && ansible-galaxy --version | head -1 | grep -F '2.21.5'
