# Client pins: ansible-core 2.22.0 on Python 3.15.2.
# Ansible 2.22 adds controller support for Python 3.15.
FROM python:3.16.0a1-slim@sha256:df293ba3b89f458e1aad6051b62ff1c4fad7b3b52b8147ae9cc4b667f9cf2741
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.22.0 \
 && ansible-galaxy --version | head -1 | grep -F '2.22.0'
