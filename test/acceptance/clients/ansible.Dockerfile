# Client pins: ansible-core 2.22.0 on Python 3.15.2.
# Ansible 2.22 adds controller support for Python 3.15.
FROM python:3.15.2-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.22.0 \
 && ansible-galaxy --version | head -1 | grep -F '2.22.0'
