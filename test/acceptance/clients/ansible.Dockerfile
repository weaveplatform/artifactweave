# ansible-core 2.21.5 (current stable checked 2026-10-28), on supported Python 3.14.
FROM python:3.14.0-slim@sha256:0aecac02dc3d4c5dbb024b753af084cafe41f5416e02193f1ce345d671ec966e
RUN pip install --no-cache-dir --root-user-action=ignore ansible-core==2.21.5 \
 && ansible-galaxy --version | head -1 | grep -F '2.21.5'
