# Client pins: ansible-core 2.21.5 on Python 3.14.0.
# The Python 3.15 bump in #895 removes a dataclasses hook used by this client.
FROM python:3.14.0-slim@sha256:0aecac02dc3d4c5dbb024b753af084cafe41f5416e02193f1ce345d671ec966e
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.21.5 \
 && ansible-galaxy --version | head -1 | grep -F '2.21.5'
