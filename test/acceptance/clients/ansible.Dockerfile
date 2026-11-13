# Client pins: ansible-core 2.22.0 on Python 3.15.2.
# Ansible 2.22 adds controller support for Python 3.15. ansible-core is pinned
# in ansible.requirements.in; pip installs the hashed file pip-lock.sh writes
# from it, from PyPI and then from TUNA's mirror of PyPI, so one PyPI fault
# does not fail the build and the mirror can serve only PyPI's bytes
# (#1321, #1439). python.Dockerfile says why TUNA and why the hashes.
#
# PyYAML 6.0.3 has no wheel for Python 3.15, so pip builds it from its sdist,
# as it did before #1439. The images that build nothing pass --only-binary;
# this one passes --build-constraint instead, with the build's setuptools and
# Cython hashed in ansible.build-requirements.txt. pip then installs the build
# requirements in hash-checking mode too, and refuses one the file does not
# list, which a build left to fetch its own requirements would not.
FROM python:3.15.2-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928
COPY ansible.requirements.txt ansible.build-requirements.txt /tmp/
RUN for index in https://pypi.org/simple https://pypi.tuna.tsinghua.edu.cn/simple; do \
      pip install --no-cache-dir --root-user-action=ignore --require-hashes \
        --build-constraint /tmp/ansible.build-requirements.txt \
        --index-url "$index" -r /tmp/ansible.requirements.txt && break; \
    done \
 && rm /tmp/ansible.requirements.txt /tmp/ansible.build-requirements.txt \
 && ansible-galaxy --version | head -1 | grep -F '2.22.0'
