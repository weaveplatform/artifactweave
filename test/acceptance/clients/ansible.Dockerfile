# Client pins: ansible-core 2.22.0 on Python 3.15.2.
# Ansible 2.22 adds controller support for Python 3.15.
FROM python:3.15.2-slim@sha256:8c13a3f37a6a5b71498228e902c4b9f51c1f7900d49fabf71f9a650c6120e0d0
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.22.0 \
 && ansible-galaxy --version | head -1 | grep -F '2.22.0'
